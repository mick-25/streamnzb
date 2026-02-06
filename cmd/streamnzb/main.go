package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"streamnzb/pkg/api"
	"streamnzb/pkg/availnzb"
	"streamnzb/pkg/initialization"
	"streamnzb/pkg/logger"
	"streamnzb/pkg/nntp/proxy"
	"streamnzb/pkg/session"
	"streamnzb/pkg/stremio"
	"streamnzb/pkg/triage"
	"streamnzb/pkg/validation"
	"streamnzb/pkg/web"

	"github.com/joho/godotenv"
)

var (
	// AvailNZB configuration set at build time via -ldflags
	AvailNZBURL    = ""
	AvailNZBAPIKey = ""
)

func main() {
	// Load environment variables for logger and bootstrap
	if err := godotenv.Load(); err != nil {
		fmt.Println("No .env file found, using environment variables")
	}

	// Initialize Logger early so bootstrap can use it
	logLevel := os.Getenv("LOG_LEVEL")
	if logLevel == "" {
		logLevel = "INFO"
	}
	logger.Init(logLevel)

	logger.Info("Starting StreamNZB", "version", "v0.1.0")

	// Bootstrap application
	comp, err := initialization.Bootstrap()
	if err != nil {
		initialization.WaitForInputAndExit(err)
	}

	cfg := comp.Config

	// Initialize article validator
	cacheTTL := time.Duration(cfg.CacheTTLSeconds) * time.Second
	validator := validation.NewChecker(
		comp.ProviderPools,
		cacheTTL,
		cfg.ValidationSampleSize,
		cfg.MaxConcurrentValidations,
	)

	// Initialize session manager
	sessionTTL := 30 * time.Minute
	sessionManager := session.NewManager(comp.StreamingPools, sessionTTL)
	logger.Info("Session manager initialized", "ttl", sessionTTL)

	// Initialize Triage Service
	triageService := triage.NewService(5)

	// Initialize AvailNZB client
	availClient := availnzb.NewClient(AvailNZBURL, AvailNZBAPIKey)

	// Initialize Stremio addon server
	stremioServer, err := stremio.NewServer(cfg.AddonBaseURL, cfg.AddonPort, comp.Indexer, validator, sessionManager, triageService, availClient, cfg.SecurityToken)
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("Failed to initialize Stremio server: %v", err))
	}
	
	// Initialize API Server
	apiServer := api.NewServer(cfg, comp.ProviderPools, sessionManager, stremioServer)
	
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

	logger.Info("Stremio manifest URL", "url", fmt.Sprintf("%s/%s/manifest.json", cfg.AddonBaseURL, cfg.SecurityToken))
	logger.Info("Frontend url", "url", fmt.Sprintf("%s/%s/", cfg.AddonBaseURL, cfg.SecurityToken))

	if err := http.ListenAndServe(addr, mux); err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("Server failed: %v", err))
	}
}
