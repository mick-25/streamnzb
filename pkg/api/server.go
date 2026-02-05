package api

import (
	"net/http"
	"sync"

	"streamnzb/pkg/availnzb"
	"streamnzb/pkg/config"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/nntp"
	"streamnzb/pkg/session"
	"streamnzb/pkg/stremio"
	"streamnzb/pkg/triage"
	"streamnzb/pkg/validation"
)

// Server handles API requests and serves the frontend
type Server struct {
	mu            sync.RWMutex
	config        *config.Config
	providerPools map[string]*nntp.ClientPool
	sessionMgr    *session.Manager
	strmServer    *stremio.Server
}

// NewServer creates a new API server
func NewServer(cfg *config.Config, pools map[string]*nntp.ClientPool, sessMgr *session.Manager, strmServer *stremio.Server) *Server {
	return &Server{
		config:        cfg,
		providerPools: pools,
		sessionMgr:    sessMgr,
		strmServer:    strmServer,
	}
}

// Reload updates the server components at runtime
func (s *Server) Reload(cfg *config.Config, pools map[string]*nntp.ClientPool, indexers indexer.Indexer, validator *validation.Checker, triage *triage.Service, avail *availnzb.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Shutdown old pools
	for name, pool := range s.providerPools {
		if _, exists := pools[name]; !exists {
			pool.Shutdown()
		}
	}

	s.config = cfg
	s.providerPools = pools
	
	// Update dependencies
	s.sessionMgr.UpdatePools(s.getPoolList())
	
	if s.strmServer != nil {
		s.strmServer.Reload(cfg.AddonBaseURL, indexers, validator, triage, avail, cfg.SecurityToken)
	}
}

func (s *Server) getPoolList() []*nntp.ClientPool {
	var list []*nntp.ClientPool
	for _, p := range s.providerPools {
		list = append(list, p)
	}
	return list
}

// Handler returns the HTTP handler for the API
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// API Routes (WS only now)
	mux.HandleFunc("/api/ws", s.handleWebSocket)

	return mux
}

