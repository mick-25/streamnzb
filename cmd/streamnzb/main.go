package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"streamnzb/pkg/availnzb"
	"streamnzb/pkg/config"
	"streamnzb/pkg/logger"
	"streamnzb/pkg/nntp"
	"streamnzb/pkg/nntp/proxy"
	"streamnzb/pkg/nzbhydra"
	"streamnzb/pkg/session"
	"streamnzb/pkg/stremio"
	"streamnzb/pkg/triage"
	"streamnzb/pkg/validation"

	"github.com/joho/godotenv"
)

func main() {
	// Load environment variables
	if err := godotenv.Load(); err != nil {
		fmt.Println("No .env file found, using environment variables")
	}

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Initialize Logger
	logLevel := os.Getenv("LOG_LEVEL")
	if logLevel == "" {
		logLevel = "INFO"
	}
	logger.Init(logLevel)

	logger.Info("Starting StreamNZB Stremio Addon", "version", "v0.1.0")
	logger.Info("NZBHydra2 connected", "url", cfg.NZBHydra2URL)
	logger.Info("Providers configured", "count", len(cfg.Providers))

	// Initialize NZBHydra2 client
	hydraClient := nzbhydra.NewClient(cfg.NZBHydra2URL, cfg.NZBHydra2APIKey)

	// Initialize NNTP provider pools
	providerPools := make(map[string]*nntp.ClientPool)
	for _, provider := range cfg.Providers {
		logger.Info("Initializing NNTP pool", "provider", provider.Name, "host", provider.Host, "conns", provider.Connections)

		// Use Lazy Pool (Dynamic Scaling)
		// Starts with 0 connections, scales up to Max, drops idle ones.
		pool := nntp.NewClientPool(
			provider.Host,
			provider.Port,
			provider.UseSSL,
			provider.Username,
			provider.Password,
			provider.Connections,
		)

		// Validate credentials/connectivity (502 auth check)
		if err := pool.Validate(); err != nil {
			logger.Error("Failed to initialize provider", "name", provider.Name, "host", provider.Host, "err", err)
			continue
		}

		providerPools[provider.Name] = pool
	}

	if len(providerPools) == 0 {
		log.Fatalf("Critical: All %d configured providers failed to initialize. Check your credentials and connectivity.", len(cfg.Providers))
	}

	// Initialize article validator
	cacheTTL := time.Duration(cfg.CacheTTLSeconds) * time.Second
	validator := validation.NewChecker(
		providerPools,
		cacheTTL,
		cfg.ValidationSampleSize,
		cfg.MaxConcurrentValidations,
	)

	// Initialize session manager
	// Use ALL configured providers for failover and speed aggregation
	var streamingPools []*nntp.ClientPool
	for _, prov := range cfg.Providers {
		if pool, ok := providerPools[prov.Name]; ok {
			streamingPools = append(streamingPools, pool)
		}
	}
	
	if len(streamingPools) == 0 {
		log.Fatal("No NNTP providers configured for streaming")
	}
	
	sessionTTL := 30 * time.Minute // Sessions expire after 30 minutes of inactivity
	sessionManager := session.NewManager(streamingPools, sessionTTL)
	logger.Info("Session manager initialized", "ttl", sessionTTL)

	// Initialize Triage Service
	// 5 per group (20 total) strikes better balance between diversity and speed
	triageService := triage.NewService(5)

	// Initialize AvailNZB client
	availClient := availnzb.NewClient(cfg.AvailNZBURL, cfg.AvailNZBAPIKey)

	// Initialize Stremio addon server
	stremioServer := stremio.NewServer(cfg.AddonBaseURL, hydraClient, validator, sessionManager, triageService, availClient, cfg.SecurityToken)

	// Setup HTTP routes
	mux := http.NewServeMux()
	stremioServer.SetupRoutes(mux)

	// Start NNTP proxy if enabled
	if cfg.ProxyEnabled {
		proxyServer := proxy.NewServer(cfg.ProxyHost, cfg.ProxyPort, streamingPools, cfg.ProxyAuthUser, cfg.ProxyAuthPass)
		go func() {
			logger.Info("Starting NNTP proxy", "host", cfg.ProxyHost, "port", cfg.ProxyPort)
			if err := proxyServer.Start(); err != nil {
				log.Fatalf("NNTP proxy failed: %v", err)
			}
		}()
	}

	// Start Stremio server
	addr := fmt.Sprintf(":%d", cfg.AddonPort)
	logger.Info("Stremio addon listening", "addr", addr)
	logger.Info("Install addon", "url", fmt.Sprintf("%s/%s/manifest.json", cfg.AddonBaseURL, cfg.SecurityToken))

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
