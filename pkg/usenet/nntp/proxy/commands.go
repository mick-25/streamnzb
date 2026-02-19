package proxy

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// poolGetTimeout limits how long a proxy command waits for an NNTP connection.
// Prevents indefinite hang when all pool connections are in use or stuck.
const poolGetTimeout = 60 * time.Second

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
			continue
		}

		article, err := client.GetArticle(messageID)
		pool.Put(client)

		if err != nil {
			// If article not found, try next provider
			if strings.Contains(err.Error(), "430") || strings.Contains(err.Error(), "No such article") {
				continue
			}
			// Other error, try next provider
			continue
		}

		// Success! Return article
		lines := []string{fmt.Sprintf("220 0 %s", messageID)}
		lines = append(lines, strings.Split(article, "\n")...)
		return s.WriteMultiLine(lines)
	}

	// All providers failed
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
			continue
		}

		body, err := client.GetBody(messageID)
		pool.Put(client)

		if err != nil {
			if strings.Contains(err.Error(), "430") || strings.Contains(err.Error(), "No such article") {
				continue
			}
			continue
		}

		// Success
		lines := []string{fmt.Sprintf("222 0 %s", messageID)}
		lines = append(lines, strings.Split(body, "\n")...)
		return s.WriteMultiLine(lines)
	}

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
			continue
		}

		head, err := client.GetHead(messageID)
		pool.Put(client)

		if err != nil {
			if strings.Contains(err.Error(), "430") || strings.Contains(err.Error(), "No such article") {
				continue
			}
			continue
		}

		// Success
		lines := []string{fmt.Sprintf("221 0 %s", messageID)}
		lines = append(lines, strings.Split(head, "\n")...)
		return s.WriteMultiLine(lines)
	}

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
			continue
		}

		exists, err := client.CheckArticle(messageID)
		pool.Put(client)

		if err != nil {
			continue
		}

		if exists {
			return s.WriteLine(fmt.Sprintf("223 0 %s", messageID))
		}
	}

	return s.WriteLine("430 No such article")
}
