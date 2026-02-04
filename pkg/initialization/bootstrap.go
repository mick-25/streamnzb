package initialization

import (
	"fmt"
	"os"
	"streamnzb/pkg/config"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/logger"
	"streamnzb/pkg/nntp"
	"streamnzb/pkg/nntp/proxy"
	"streamnzb/pkg/nzbhydra"
	"streamnzb/pkg/prowlarr"
	"streamnzb/pkg/stremio"
)

// InitializedComponents holds all the components initialized during bootstrap
type InitializedComponents struct {
	Config         *config.Config
	Indexer        indexer.Indexer
	ProviderPools  map[string]*nntp.ClientPool
	StreamingPools []*nntp.ClientPool
}

// WaitForInputAndExit prints an error and waits for user input before exiting
func WaitForInputAndExit(err error) {
	fmt.Printf("\n❌ CRITICAL ERROR: %v\n", err)
	fmt.Println("\nPress Enter to exit...")
	var input string
	fmt.Scanln(&input)
	os.Exit(1)
}

// Bootstrap coordinates the application startup sequence
func Bootstrap() (*InitializedComponents, error) {
	// 1. Load configuration
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("configuration error: %w", err)
	}

	// 2. Initialize Indexers
	var indexers []indexer.Indexer

	// Initialize NZBHydra2
	if cfg.NZBHydra2APIKey != "" {
		hydraClient, err := nzbhydra.NewClient(cfg.NZBHydra2URL, cfg.NZBHydra2APIKey)
		if err != nil {
			logger.Error("Failed to initialize NZBHydra2", "err", err)
		} else {
			indexers = append(indexers, hydraClient)
			logger.Info("Initialized NZBHydra2 client", "url", cfg.NZBHydra2URL)
		}
	}

	// Initialize Prowlarr
	if cfg.ProwlarrAPIKey != "" {
		discovered, err := prowlarr.GetConfiguredIndexers(cfg.ProwlarrURL, cfg.ProwlarrAPIKey)
		if err != nil {
			logger.Error("Failed to initialize Prowlarr", "err", err)
		} else {
			if len(discovered) > 0 {
				indexers = append(indexers, discovered...)
				logger.Info("Initialized Prowlarr indexers", "count", len(discovered))
			} else {
				logger.Warn("Connected to Prowlarr but found no active Usenet indexers")
			}
		}
	}

	if len(indexers) == 0 {
		return nil, fmt.Errorf("no indexers configured/initialized")
	}

	aggregator := indexer.NewAggregator(indexers...)

	// 3. Initialize NNTP provider pools
	providerPools := make(map[string]*nntp.ClientPool)
	var streamingPools []*nntp.ClientPool

	for _, provider := range cfg.Providers {
		logger.Info("Initializing NNTP pool", "provider", provider.Name, "host", provider.Host, "conns", provider.Connections)

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
		streamingPools = append(streamingPools, pool)
	}

	if len(providerPools) == 0 {
		return nil, fmt.Errorf("all %d configured providers failed to initialize. Check your credentials and connectivity", len(cfg.Providers))
	}

	// 4. Validate Server Ports
	// Stremio Server validation
	// Note: We create a temporary server instance just to check the port during bootstrap
	// The real server instance is created in main.go with full dependencies
	_, err = stremio.NewServer(cfg.AddonBaseURL, cfg.AddonPort, aggregator, nil, nil, nil, nil, cfg.SecurityToken)
	if err != nil {
		return nil, fmt.Errorf("stremio server init check failed: %w", err)
	}

	// Proxy Server validation
	if cfg.ProxyEnabled {
		_, err := proxy.NewServer(cfg.ProxyHost, cfg.ProxyPort, streamingPools, cfg.ProxyAuthUser, cfg.ProxyAuthPass)
		if err != nil {
			return nil, fmt.Errorf("proxy server init failed: %w", err)
		}
	}

	// Security token warning
	if cfg.SecurityToken == "" {
		logger.Warn("!! SECURITY WARNING: SECURITY_TOKEN is not set. Your addon is accessible without authentication !!")
	}

	return &InitializedComponents{
		Config:         cfg,
		Indexer:        aggregator,
		ProviderPools:  providerPools,
		StreamingPools: streamingPools,
	}, nil
}
