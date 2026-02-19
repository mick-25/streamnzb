package proxy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/usenet/nntp"
)

// poolGetTimeout limits how long a proxy command waits for an NNTP connection.
// Prevents indefinite hang when all pool connections are in use or stuck.
const poolGetTimeout = 60 * time.Second

// isClientWriteError reports whether err indicates the client connection failed while we were writing.
// In that case we must not retry other pools or send 430 — we already started the response and the client is gone.
func isClientWriteError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "forcibly closed") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "wsasend") ||
		strings.Contains(msg, "use of closed network connection")
}

// normalizeMessageID ensures the message ID has angle brackets as required by NNTP.
func normalizeMessageID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if !strings.HasPrefix(s, "<") {
		s = "<" + s
	}
	if !strings.HasSuffix(s, ">") {
		s = s + ">"
	}
	return s
}

// handleQuit handles the QUIT command
func (s *Session) handleQuit(args []string) error {
	s.shouldQuit = true
	return s.WriteLine("205 Closing connection")
}

// handleCapabilities handles the CAPABILITIES command
func (s *Session) handleCapabilities(args []string) error {
	capabilities := []string{
		"101 Capability list:",
		"VERSION 2",
		"READER",
		"POST",
		"IHAVE",
		"STREAMING",
	}

	if s.authUser != "" {
		capabilities = append(capabilities, "AUTHINFO USER")
	}

	return s.WriteMultiLine(capabilities)
}

// handleAuthInfo handles AUTHINFO USER/PASS commands
func (s *Session) handleAuthInfo(args []string) error {
	if len(args) < 2 {
		return s.WriteLine("501 Syntax error")
	}

	subCmd := strings.ToUpper(args[0])
	value := args[1]

	switch subCmd {
	case "USER":
		if s.authUser == "" {
			// No auth required
			s.authenticated = true
			return s.WriteLine("281 Authentication accepted")
		}

		if value == s.authUser {
			return s.WriteLine("381 Password required")
		}
		return s.WriteLine("481 Authentication failed")

	case "PASS":
		if value == s.authPass {
			s.authenticated = true
			return s.WriteLine("281 Authentication accepted")
		}
		return s.WriteLine("481 Authentication failed")

	default:
		return s.WriteLine("501 Syntax error")
	}
}

// handleGroup handles the GROUP command
func (s *Session) handleGroup(args []string) error {
	if len(args) < 1 {
		return s.WriteLine("501 Syntax error")
	}

	groupName := args[0]
	s.currentGroup = groupName

	// Return dummy group info (SABnzbd doesn't really use this)
	return s.WriteLine(fmt.Sprintf("211 0 1 1 %s", groupName))
}

// handleList handles the LIST command
func (s *Session) handleList(args []string) error {
	// Return minimal list (SABnzbd doesn't need full newsgroup list)
	lines := []string{
		"215 List of newsgroups follows",
		"alt.binaries.test 0 1 y",
	}
	return s.WriteMultiLine(lines)
}

// handleDate handles the DATE command
func (s *Session) handleDate(args []string) error {
	// Return current date in NNTP format: YYYYMMDDhhmmss
	now := time.Now().UTC()
	dateStr := now.Format("20060102150405")
	return s.WriteLine(fmt.Sprintf("111 %s", dateStr))
}

// ensureGroup selects the current group on the backend client if set (required by many NNTP servers for ARTICLE/BODY/HEAD/STAT).
func (s *Session) ensureGroup(client *nntp.Client, pool *nntp.ClientPool) bool {
	if s.currentGroup == "" {
		return true
	}
	if err := client.Group(s.currentGroup); err != nil {
		logger.Debug("NNTP proxy: GROUP failed on backend", "group", s.currentGroup, "err", err)
		pool.Put(client)
		return false
	}
	return true
}

// handleArticle handles the ARTICLE command (with failover)
func (s *Session) handleArticle(args []string) error {
	if len(args) < 1 {
		return s.WriteLine("501 Syntax error")
	}

	messageID := normalizeMessageID(args[0])

	ctx, cancel := context.WithTimeout(context.Background(), poolGetTimeout)
	defer cancel()
	for _, pool := range s.pools {
		client, err := pool.Get(ctx)
		if err != nil {
			logger.Debug("NNTP proxy: pool Get failed", "err", err)
			continue
		}
		if !s.ensureGroup(client, pool) {
			continue
		}

		article, err := client.GetArticle(messageID)
		pool.Put(client)

		if err != nil {
			if strings.Contains(err.Error(), "430") || strings.Contains(err.Error(), "No such article") {
				continue
			}
			logger.Debug("NNTP proxy: GetArticle failed", "messageID", messageID, "err", err)
			continue
		}

		// Success! Return article (normalize line endings for NNTP)
		lines := []string{fmt.Sprintf("220 0 %s", messageID)}
		for _, line := range strings.Split(strings.ReplaceAll(article, "\r\n", "\n"), "\n") {
			lines = append(lines, strings.TrimSuffix(line, "\r"))
		}
		return s.WriteMultiLine(lines)
	}

	logger.Info("NNTP proxy: ARTICLE failed (all pools)", "messageID", messageID)
	return s.WriteLine("430 No such article")
}

// handleBody handles the BODY command (with failover)
func (s *Session) handleBody(args []string) error {
	if len(args) < 1 {
		return s.WriteLine("501 Syntax error")
	}

	messageID := normalizeMessageID(args[0])

	ctx, cancel := context.WithTimeout(context.Background(), poolGetTimeout)
	defer cancel()
	for _, pool := range s.pools {
		client, err := pool.Get(ctx)
		if err != nil {
			logger.Debug("NNTP proxy: pool Get failed", "err", err)
			continue
		}
		if !s.ensureGroup(client, pool) {
			continue
		}

		_, err = client.StreamBody(messageID, s.conn)
		pool.Put(client)

		if err != nil {
			if isClientWriteError(err) {
				return err
			}
			if strings.Contains(err.Error(), "430") || strings.Contains(err.Error(), "No such article") {
				continue
			}
			logger.Debug("NNTP proxy: GetBody failed", "messageID", messageID, "err", err)
			continue
		}

		return nil
	}

	logger.Info("NNTP proxy: BODY failed (all pools)", "messageID", messageID)
	return s.WriteLine("430 No such article")
}

// handleHead handles the HEAD command (with failover)
func (s *Session) handleHead(args []string) error {
	if len(args) < 1 {
		return s.WriteLine("501 Syntax error")
	}

	messageID := normalizeMessageID(args[0])

	ctx, cancel := context.WithTimeout(context.Background(), poolGetTimeout)
	defer cancel()
	for _, pool := range s.pools {
		client, err := pool.Get(ctx)
		if err != nil {
			logger.Debug("NNTP proxy: pool Get failed", "err", err)
			continue
		}
		if !s.ensureGroup(client, pool) {
			continue
		}

		head, err := client.GetHead(messageID)
		pool.Put(client)

		if err != nil {
			if strings.Contains(err.Error(), "430") || strings.Contains(err.Error(), "No such article") {
				continue
			}
			logger.Debug("NNTP proxy: GetHead failed", "messageID", messageID, "err", err)
			continue
		}

		// Success (normalize line endings)
		lines := []string{fmt.Sprintf("221 0 %s", messageID)}
		for _, line := range strings.Split(strings.ReplaceAll(head, "\r\n", "\n"), "\n") {
			lines = append(lines, strings.TrimSuffix(line, "\r"))
		}
		return s.WriteMultiLine(lines)
	}

	logger.Info("NNTP proxy: HEAD failed (all pools)", "messageID", messageID)
	return s.WriteLine("430 No such article")
}

// handleStat handles the STAT command (with failover)
func (s *Session) handleStat(args []string) error {
	if len(args) < 1 {
		return s.WriteLine("501 Syntax error")
	}

	messageID := normalizeMessageID(args[0])

	ctx, cancel := context.WithTimeout(context.Background(), poolGetTimeout)
	defer cancel()
	for _, pool := range s.pools {
		client, err := pool.Get(ctx)
		if err != nil {
			logger.Debug("NNTP proxy: pool Get failed", "err", err)
			continue
		}
		if !s.ensureGroup(client, pool) {
			continue
		}

		exists, err := client.CheckArticle(messageID)
		pool.Put(client)

		if err != nil {
			logger.Debug("NNTP proxy: CheckArticle failed", "messageID", messageID, "err", err)
			continue
		}
		if exists {
			return s.WriteLine(fmt.Sprintf("223 0 %s", messageID))
		}
	}

	logger.Info("NNTP proxy: STAT failed (all pools)", "messageID", messageID)
	return s.WriteLine("430 No such article")
}
