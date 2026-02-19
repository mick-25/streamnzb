package proxy

import (
	"bufio"
	"fmt"
	"net"
	"streamnzb/pkg/core/logger"
	"strings"
	"sync"

	"streamnzb/pkg/usenet/nntp"
)

// Server represents the NNTP proxy server
type Server struct {
	host     string
	port     int
	pools    []*nntp.ClientPool
	authUser string
	authPass string

	listener net.Listener
	mu       sync.Mutex
	sessions map[string]*Session
}

// NewServer creates a new NNTP proxy server
func NewServer(host string, port int, pools []*nntp.ClientPool, authUser, authPass string) (*Server, error) {
	s := &Server{
		host:     host,
		port:     port,
		pools:    pools,
		authUser: authUser,
		authPass: authPass,
		sessions: make(map[string]*Session),
	}

	if err := s.Validate(); err != nil {
		return nil, err
	}

	return s, nil
}

// Validate checks if the proxy server can be started (port free, security checks)
func (s *Server) Validate() error {
	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("NNTP proxy port %d is already in use", s.port)
	}
	ln.Close()

	return nil
}

// Start starts the NNTP proxy server
func (s *Server) Start() error {
	addr := fmt.Sprintf("%s:%d", s.host, s.port)

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to start NNTP proxy: %w", err)
	}

	s.listener = listener
	logger.Info("NNTP proxy listening", "addr", addr)

	// Accept connections
	for {
		conn, err := listener.Accept()
		if err != nil {
			// Check if we are shutting down
			if strings.Contains(err.Error(), "use of closed network connection") || strings.Contains(err.Error(), "closed") {
				return nil
			}
			logger.Error("NNTP proxy accept error", "err", err)
			continue
		}

		go s.handleConnection(conn)
	}
}

// Stop stops the NNTP proxy server
func (s *Server) Stop() error {
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// handleConnection handles a single NNTP client connection
func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()

	// Create session
	session := NewSession(conn, s.pools, s.authUser, s.authPass)

	// Store session
	s.mu.Lock()
	s.sessions[conn.RemoteAddr().String()] = session
	s.mu.Unlock()

	// Remove session on exit
	defer func() {
		s.mu.Lock()
		delete(s.sessions, conn.RemoteAddr().String())
		s.mu.Unlock()
	}()

	// Send welcome banner
	session.WriteLine("200 StreamNZB NNTP Proxy ready (posting prohibited)")

	// Read and process commands
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)

		if line == "" {
			continue
		}

		// Parse command
		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}

		cmd := strings.ToUpper(parts[0])
		args := parts[1:]

		// Handle command
		if err := session.HandleCommand(cmd, args); err != nil {
			logger.Error("NNTP proxy command error", "remote", conn.RemoteAddr(), "err", err)
			session.WriteLine(fmt.Sprintf("500 %v", err))
		}

		// Check if session should quit
		if session.ShouldQuit() {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		logger.Error("NNTP proxy scanner error", "remote", conn.RemoteAddr(), "err", err)
	}
}

// ProxySessionInfo represents a snapshot of an active proxy session
type ProxySessionInfo struct {
	ID           string `json:"id"`
	RemoteAddr   string `json:"remote_addr"`
	CurrentGroup string `json:"current_group"`
}

// GetSessions returns a list of active proxy sessions
func (s *Server) GetSessions() []ProxySessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	var list []ProxySessionInfo
	for id, session := range s.sessions {
		list = append(list, ProxySessionInfo{
			ID:           id,
			RemoteAddr:   id,
			CurrentGroup: session.CurrentGroup(),
		})
	}
	return list
}
