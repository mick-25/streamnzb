package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"streamnzb/pkg/api"
	"streamnzb/pkg/auth"
	"streamnzb/pkg/availnzb"
	"streamnzb/pkg/env"
	"streamnzb/pkg/initialization"
	"streamnzb/pkg/logger"
	"streamnzb/pkg/nntp/proxy"
	"streamnzb/pkg/session"
	"streamnzb/pkg/stremio"
	"streamnzb/pkg/tmdb"
	"streamnzb/pkg/triage"
	"streamnzb/pkg/tvdb"
	"streamnzb/pkg/validation"
	"streamnzb/pkg/web"

	"github.com/joho/godotenv"
)

var (
	// AvailNZB configuration set at build time via -ldflags
	AvailNZBURL    = ""
	AvailNZBAPIKey = ""

	// TMDB Key via ldflags
	TMDBKey = ""
	// TVDB Key via ldflags
	TVDBKey = ""

	// Version set at build time via -ldflags (from release-please tag, e.g. v1.0.0)
	Version = "dev"
)

func main() {
	// Load environment variables for logger and bootstrap
	if err := godotenv.Load(); err != nil {
		fmt.Println("No .env file found, using environment variables")
	}

	// Initialize Logger early so bootstrap can use it
	logger.Init(env.LogLevel())

	logger.Info("Starting StreamNZB", "version", Version)

	// Bootstrap application
	comp, err := initialization.Bootstrap()
	if err != nil {
		initialization.WaitForInputAndExit(err)
	}

	cfg := comp.Config
	logger.SetLevel(cfg.LogLevel)

	// Initialize article validator
	cacheTTL := time.Duration(cfg.CacheTTLSeconds) * time.Second
	validator := validation.NewChecker(
		comp.ProviderPools,
		cacheTTL,
		cfg.ValidationSampleSize,
		6, // Hardcoded concurrency limit (not configurable)
	)

	// Initialize session manager
	sessionTTL := 30 * time.Minute
	sessionManager := session.NewManager(comp.StreamingPools, sessionTTL)
	logger.Info("Session manager initialized", "ttl", sessionTTL)

	// Initialize Triage Service
	triageService := triage.NewService(
		&cfg.Filters,
		cfg.Sorting,
	)

	availNZBUrl := cfg.AvailNZBURL
	if availNZBUrl == "" {
		availNZBUrl = AvailNZBURL
	}

	availNZBAPIKey := cfg.AvailNZBAPIKey
	if availNZBAPIKey == "" {
		availNZBAPIKey = AvailNZBAPIKey
	}

	// Initialize AvailNZB client
	availClient := availnzb.NewClient(availNZBUrl, availNZBAPIKey)

	// Data directory for state.json (TVDB token, devices, etc.)
	dataDir := filepath.Dir(cfg.LoadedPath)
	if dataDir == "" || dataDir == "." {
		dataDir, _ = os.Getwd()
	}

	// Initialize TMDB client
	// Prefer Env Var, fallback to ldflag
	tmdbKey := cfg.TMDBAPIKey
	if tmdbKey == "" {
		tmdbKey = TMDBKey
	}
	tmdbClient := tmdb.NewClient(tmdbKey)

	// Initialize TVDB client (fallback for TMDB when resolving IMDb -> TVDB ID)
	tvdbKey := cfg.TVDBAPIKey
	if tvdbKey == "" {
		tvdbKey = TVDBKey
	}
	tvdbClient := tvdb.NewClient(tvdbKey, dataDir)

	// Initialize User Manager (needed before Stremio server)
	deviceManager, err := auth.GetDeviceManager(dataDir)
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("Failed to initialize device manager: %v", err))
	}

	// Initialize Stremio addon server
	stremioServer, err := stremio.NewServer(cfg, cfg.AddonBaseURL, cfg.AddonPort, comp.Indexer, validator,
		sessionManager, triageService, availClient, tmdbClient, tvdbClient, deviceManager, Version)
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("Failed to initialize Stremio server: %v", err))
	}

	// Initialize API Server
	apiServer := api.NewServer(cfg, comp.ProviderPools, sessionManager, stremioServer, comp.Indexer, deviceManager)

	// Set embedded web handler
	stremioServer.SetWebHandler(web.Handler())
	stremioServer.SetAPIHandler(apiServer.Handler())

	// Setup HTTP routes
	mux := http.NewServeMux()
	stremioServer.SetupRoutes(mux)

	// Mount API routes (apiServer.Handler returns a mux with /api/...)
	// Since both are muxes, we need to merge or mount carefully.
	// StremioServer mounts "/" at the end.
	// We should mount /api/ before /.
	mux.Handle("/api/", apiServer.Handler())

	// Start NNTP proxy if enabled
	if cfg.ProxyEnabled {
		proxyServer, err := proxy.NewServer(cfg.ProxyHost, cfg.ProxyPort, comp.StreamingPools, cfg.ProxyAuthUser, cfg.ProxyAuthPass)
		if err != nil {
			initialization.WaitForInputAndExit(fmt.Errorf("Failed to initialize NNTP proxy: %v", err))
		}

		apiServer.SetProxyServer(proxyServer)

		go func() {
			logger.Info("Starting NNTP proxy", "host", cfg.ProxyHost, "port", cfg.ProxyPort)
			if err := proxyServer.Start(); err != nil {
				initialization.WaitForInputAndExit(fmt.Errorf("NNTP proxy failed: %v", err))
			}
		}()
	}

	// Start Stremio server
	addr := fmt.Sprintf(":%d", cfg.AddonPort)

	logger.Info("Stremio addon server starting", "base_url", cfg.AddonBaseURL, "port", cfg.AddonPort)
	logger.Info("Note: Access requires device authentication tokens")

	if err := http.ListenAndServe(addr, mux); err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("Server failed: %v", err))
	}
}
