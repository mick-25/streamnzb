package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"

	"streamnzb/pkg/availnzb"
	"streamnzb/pkg/config"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/logger"
	"streamnzb/pkg/nntp"
	"streamnzb/pkg/nntp/proxy"
	"streamnzb/pkg/session"
	"streamnzb/pkg/stremio"
	"streamnzb/pkg/tmdb"
	"streamnzb/pkg/triage"
	"streamnzb/pkg/validation"
)

// Server handles API requests and serves the frontend
type Server struct {
	mu             sync.RWMutex
	config         *config.Config
	providerPools  map[string]*nntp.ClientPool // Map for easy lookup/management
	streamingPools []*nntp.ClientPool          // Slice for session manager/proxy (superset or same underlying pools)
	sessionMgr     *session.Manager
	strmServer     *stremio.Server
	proxyServer    *proxy.Server
	indexer        indexer.Indexer

	// WebSocket Client Registry
	clients   map[*Client]bool
	clientsMu sync.Mutex
	logCh     chan string
}

type Client struct {
	conn *websocket.Conn
	send chan WSMessage
}

// NewServer creates a new API server
func NewServer(cfg *config.Config, pools map[string]*nntp.ClientPool, sessMgr *session.Manager, strmServer *stremio.Server, indexer indexer.Indexer) *Server {
	// Build streaming pools list from map (initial)
	var list []*nntp.ClientPool
	for _, p := range pools {
		list = append(list, p)
	}

	s := &Server{
		config:         cfg,
		providerPools:  pools,
		streamingPools: list,
		sessionMgr:     sessMgr,
		strmServer:     strmServer,
		indexer:        indexer,
		clients:        make(map[*Client]bool),
		logCh:          make(chan string, 100),
	}

	// Start log broadcaster
	logger.SetBroadcast(s.logCh)
	go s.broadcastLogs()

	return s
}

// ... (SetProxyServer and Reload remain same)

func (s *Server) broadcastLogs() {
	for msgStr := range s.logCh {
		msg := WSMessage{Type: "log_entry", Payload: json.RawMessage(fmt.Sprintf("%q", msgStr))}

		s.clientsMu.Lock()
		for client := range s.clients {
			select {
			case client.send <- msg:
			default:
				// Drop message if client buffer is full
			}
		}
		s.clientsMu.Unlock()
	}
}

// AddClient registers a new websocket client
func (s *Server) AddClient(client *Client) {
	s.clientsMu.Lock()
	s.clients[client] = true
	s.clientsMu.Unlock()
}

// RemoveClient unregisters a websocket client
func (s *Server) RemoveClient(client *Client) {
	s.clientsMu.Lock()
	delete(s.clients, client)
	s.clientsMu.Unlock()
	close(client.send)
}

// SetProxyServer sets the proxy server instance
func (s *Server) SetProxyServer(p *proxy.Server) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proxyServer = p
}

// Reload updates the server components at runtime
func (s *Server) Reload(cfg *config.Config, pools map[string]*nntp.ClientPool, indexers indexer.Indexer,
	validator *validation.Checker, triage *triage.Service, avail *availnzb.Client, tmdbClient *tmdb.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Stop existing Proxy (it uses old pools)
	if s.proxyServer != nil {
		logger.Info("Stopping NNTP Proxy for reload...")
		if err := s.proxyServer.Stop(); err != nil {
			logger.Error("Failed to stop proxy", "err", err)
		}
		s.proxyServer = nil
	}

	// 2. Shutdown ALL old pools
	// Since Bootstrap creates completely new pool instances, we must close the old ones to release connections.
	for _, pool := range s.providerPools {
		pool.Shutdown()
	}

	// 3. Update State
	s.config = cfg
	s.providerPools = pools
	s.indexer = indexers

	// Rebuild streaming pools list
	var newStreamingPools []*nntp.ClientPool
	for _, p := range pools {
		newStreamingPools = append(newStreamingPools, p)
	}
	s.streamingPools = newStreamingPools

	// 4. Update dependencies
	logger.SetLevel(cfg.LogLevel)
	s.sessionMgr.UpdatePools(newStreamingPools)

	if s.strmServer != nil {
		s.strmServer.Reload(cfg.AddonBaseURL, indexers, validator, triage, avail, tmdbClient, cfg.SecurityToken)
	}

	// 5. Restart Proxy if enabled
	if cfg.ProxyEnabled {
		logger.Info("Restarting NNTP Proxy...", "host", cfg.ProxyHost, "port", cfg.ProxyPort)
		// Note: We use the NEW streaming pools
		newProxy, err := proxy.NewServer(cfg.ProxyHost, cfg.ProxyPort, newStreamingPools, cfg.ProxyAuthUser, cfg.ProxyAuthPass)
		if err != nil {
			logger.Error("Failed to create new proxy during reload", "err", err)
		} else {
			s.proxyServer = newProxy
			go func() {
				if err := newProxy.Start(); err != nil {
					logger.Error("Proxy server failed to start", "err", err)
				}
			}()
		}
	}

	// 6. Cleanup Orphaned Usage Data
	s.cleanupIndexerUsage()
	s.cleanupProviderUsage()
}

func (s *Server) cleanupIndexerUsage() {
	usageMgr, err := indexer.GetUsageManager(nil)
	if err != nil || usageMgr == nil {
		return
	}

	var activeNames []string
	if agg, ok := s.indexer.(*indexer.Aggregator); ok {
		for _, idx := range agg.GetIndexers() {
			activeNames = append(activeNames, idx.Name())
		}
	} else if s.indexer != nil {
		activeNames = append(activeNames, s.indexer.Name())
	}

	usageMgr.SyncUsage(activeNames)
}

func (s *Server) cleanupProviderUsage() {
	usageMgr, err := nntp.GetProviderUsageManager(nil)
	if err != nil || usageMgr == nil {
		return
	}

	var activeNames []string
	for name := range s.providerPools {
		activeNames = append(activeNames, name)
	}

	usageMgr.SyncUsage(activeNames)
}

func (s *Server) getPoolList() []*nntp.ClientPool {
	return s.streamingPools
}

// Handler returns the HTTP handler for the API
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// API Routes (WS only now)
	mux.HandleFunc("/api/ws", s.handleWebSocket)

	return mux
}
