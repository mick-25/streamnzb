package proxy

import (
	"fmt"
	"net"
	"strings"

	"streamnzb/pkg/nntp"
)

// Session represents a single NNTP client session
type Session struct {
	conn         net.Conn
	pools        []*nntp.ClientPool
	authUser     string
	authPass     string
	
	authenticated bool
	currentGroup  string
	shouldQuit    bool
}

// NewSession creates a new NNTP session
func NewSession(conn net.Conn, pools []*nntp.ClientPool, authUser, authPass string) *Session {
	return &Session{
		conn:          conn,
		pools:         pools,
		authUser:      authUser,
		authPass:      authPass,
		authenticated: authUser == "", // Auto-auth if no credentials required
	}
}

// WriteLine writes a line to the client
func (s *Session) WriteLine(line string) error {
	_, err := fmt.Fprintf(s.conn, "%s\r\n", line)
	return err
}

// WriteMultiLine writes multiple lines ending with a dot
func (s *Session) WriteMultiLine(lines []string) error {
	for _, line := range lines {
		// Escape lines starting with dot
		if strings.HasPrefix(line, ".") {
			line = "." + line
		}
		if err := s.WriteLine(line); err != nil {
			return err
		}
	}
	// End with single dot
	return s.WriteLine(".")
}

// ShouldQuit returns whether the session should terminate
func (s *Session) ShouldQuit() bool {
	return s.shouldQuit
}

// HandleCommand processes an NNTP command
func (s *Session) HandleCommand(cmd string, args []string) error {
	// Commands that don't require auth
	switch cmd {
	case "QUIT":
		return s.handleQuit(args)
	case "CAPABILITIES":
		return s.handleCapabilities(args)
	case "AUTHINFO":
		return s.handleAuthInfo(args)
	}
	
	// Check authentication for other commands
	if !s.authenticated {
		return s.WriteLine("480 Authentication required")
	}
	
	// Authenticated commands
	switch cmd {
	case "GROUP":
		return s.handleGroup(args)
	case "ARTICLE":
		return s.handleArticle(args)
	case "BODY":
		return s.handleBody(args)
	case "HEAD":
		return s.handleHead(args)
	case "STAT":
		return s.handleStat(args)
	case "LIST":
		return s.handleList(args)
	case "DATE":
		return s.handleDate(args)
	default:
		return s.WriteLine(fmt.Sprintf("500 Unknown command: %s", cmd))
	}
}
