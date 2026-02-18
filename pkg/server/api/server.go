package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/app"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/server/stremio"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvdb"
	"streamnzb/pkg/session"
	"streamnzb/pkg/usenet/nntp"
	"streamnzb/pkg/usenet/nntp/proxy"
	"streamnzb/pkg/usenet/validation"
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
	deviceManager  *auth.DeviceManager
	app            *app.App

	availNZBURL    string
	availNZBAPIKey string
	tmdbAPIKey     string
	tvdbAPIKey     string

	// WebSocket Client Registry
	clients   map[*Client]bool
	clientsMu sync.Mutex
	logCh     chan string
}

type Client struct {
	conn   *websocket.Conn
	send   chan WSMessage
	device *auth.Device
	// user is an alias for device for backwards compatibility
	user *auth.Device
}

// NewServer creates a new API server
func NewServer(cfg *config.Config, pools map[string]*nntp.ClientPool, sessMgr *session.Manager, strmServer *stremio.Server, indexer indexer.Indexer, deviceManager *auth.DeviceManager, availNZBURL, availNZBAPIKey, tmdbAPIKey, tvdbAPIKey string) *Server {
	return NewServerWithApp(cfg, pools, sessMgr, strmServer, indexer, deviceManager, nil, availNZBURL, availNZBAPIKey, tmdbAPIKey, tvdbAPIKey)
}

// NewServerWithApp creates a new API server with App for granular reload
func NewServerWithApp(cfg *config.Config, pools map[string]*nntp.ClientPool, sessMgr *session.Manager, strmServer *stremio.Server, indexer indexer.Indexer, deviceManager *auth.DeviceManager, a *app.App, availNZBURL, availNZBAPIKey, tmdbAPIKey, tvdbAPIKey string) *Server {
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
		deviceManager:  deviceManager,
		app:            a,
		availNZBURL:    availNZBURL,
		availNZBAPIKey: availNZBAPIKey,
		tmdbAPIKey:     tmdbAPIKey,
		tvdbAPIKey:     tvdbAPIKey,
		clients:        make(map[*Client]bool),
		logCh:          make(chan string, 100),
	}

	// Start log broadcaster
	logger.SetBroadcast(s.logCh)
	go s.broadcastLogs()

	// Start background sync for provider usage stats
	go s.syncProviderUsageLoop()

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

// ReloadFromComponents updates server components. When fullReload is false (config-only),
// NNTP pools and proxy are not touched - active streams continue uninterrupted.
func (s *Server) ReloadFromComponents(comp *app.Components, fullReload bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if fullReload {
		// 1. Stop existing Proxy
		if s.proxyServer != nil {
			logger.Info("Stopping NNTP Proxy for reload...")
			if err := s.proxyServer.Stop(); err != nil {
				logger.Error("Failed to stop proxy", "err", err)
			}
			s.proxyServer = nil
		}

		// 2. Shutdown ALL old pools
		for _, pool := range s.providerPools {
			pool.Shutdown()
		}

		// 3. Update pools and indexer
		s.providerPools = comp.ProviderPools
		s.indexer = comp.Indexer
		s.streamingPools = make([]*nntp.ClientPool, 0, len(comp.ProviderPools))
		for _, p := range comp.ProviderPools {
			s.streamingPools = append(s.streamingPools, p)
		}
		s.sessionMgr.UpdatePools(s.streamingPools)

		// 4. Restart Proxy if enabled
		if comp.Config.ProxyEnabled {
			logger.Info("Restarting NNTP Proxy...", "host", comp.Config.ProxyHost, "port", comp.Config.ProxyPort)
			newProxy, err := proxy.NewServer(comp.Config.ProxyHost, comp.Config.ProxyPort, s.streamingPools, comp.Config.ProxyAuthUser, comp.Config.ProxyAuthPass)
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

		s.cleanupIndexerUsage()
		s.cleanupProviderUsage()
	}

	// Common: always update config, triage, stremio
	s.config = comp.Config
	logger.SetLevel(comp.Config.LogLevel)
	if s.strmServer != nil {
		s.strmServer.Reload(comp.Config, comp.Config.AddonBaseURL, comp.Indexer, comp.Validator, comp.Triage, comp.AvailClient, comp.AvailNZBIndexerHosts, comp.TMDBClient, comp.TVDBClient, s.deviceManager)
	}
}

// Reload updates the server components at runtime (full reload). Prefer ReloadFromComponents for granular reload.
func (s *Server) Reload(cfg *config.Config, pools map[string]*nntp.ClientPool, indexers indexer.Indexer,
	validator *validation.Checker, triage *triage.Service, avail *availnzb.Client, availNZBIndexerHosts []string,
	tmdbClient *tmdb.Client, tvdbClient *tvdb.Client) {
	comp := &app.Components{
		Config:               cfg,
		Indexer:              indexers,
		ProviderPools:        pools,
		StreamingPools:       nil, // will be built in ReloadFromComponents
		AvailNZBIndexerHosts: availNZBIndexerHosts,
		Validator:            validator,
		Triage:               triage,
		AvailClient:          avail,
		TMDBClient:           tmdbClient,
		TVDBClient:           tvdbClient,
	}
	var streamingPools []*nntp.ClientPool
	for _, p := range pools {
		streamingPools = append(streamingPools, p)
	}
	comp.StreamingPools = streamingPools
	comp.ProviderOrder = make([]string, 0, len(pools))
	for name := range pools {
		comp.ProviderOrder = append(comp.ProviderOrder, name)
	}
	s.ReloadFromComponents(comp, true)
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

// syncProviderUsageLoop periodically syncs provider usage stats to persistent storage
func (s *Server) syncProviderUsageLoop() {
	ticker := time.NewTicker(30 * time.Second) // Sync every 30 seconds
	defer ticker.Stop()

	for range ticker.C {
		s.mu.RLock()
		pools := s.providerPools
		s.mu.RUnlock()

		for _, pool := range pools {
			pool.SyncUsage()
		}
	}
}

// Handler returns the HTTP handler for the API
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public routes (no auth required)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/auth/check", s.handleAuthCheck)
	mux.HandleFunc("/api/info", s.handleInfo)

	// Protected routes (require auth)
	authMiddleware := auth.AuthMiddleware(s.deviceManager, func() string { return s.config.GetAdminUsername() }, func() string { return s.config.AdminToken })
	mux.Handle("/api/ws", authMiddleware(http.HandlerFunc(s.handleWebSocket)))

	return mux
}
