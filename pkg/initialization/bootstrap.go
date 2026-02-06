package initialization

import (
	"fmt"
	"os"
	"streamnzb/pkg/config"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/logger"
	"streamnzb/pkg/nntp"
	"streamnzb/pkg/nzbhydra"
	"streamnzb/pkg/prowlarr"
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
	logger.Error("CRITICAL ERROR", "err", err)
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

	return BuildComponents(cfg)
}

// BuildComponents builds all system modules from the provided configuration
func BuildComponents(cfg *config.Config) (*InitializedComponents, error) {
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
		logger.Warn("!! No indexers (Hydra/Prowlarr) configured. Add some via the web UI !!")
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

		// Use Host as fallback if Name is empty (common for UI-added providers)
		poolName := provider.Name
		if poolName == "" {
			poolName = provider.Host
		}

		providerPools[poolName] = pool
		streamingPools = append(streamingPools, pool)
	}

	if len(providerPools) == 0 {
		logger.Warn("!! No valid NNTP providers initialized. Check your credentials in the web UI !!")
	}

	return &InitializedComponents{
		Config:         cfg,
		Indexer:        aggregator,
		ProviderPools:  providerPools,
		StreamingPools: streamingPools,
	}, nil
}
