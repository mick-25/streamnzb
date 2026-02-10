package initialization

import (
	"fmt"
	"os"
	"path/filepath"
	"streamnzb/pkg/config"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/indexer/newznab"
	"streamnzb/pkg/logger"
	"streamnzb/pkg/nntp"
	"streamnzb/pkg/nzbhydra"
	"streamnzb/pkg/persistence"
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

	// Initialize State Manager
	dataDir := filepath.Dir(cfg.LoadedPath)
	stateMgr, err := persistence.GetManager(dataDir)
	if err != nil {
		logger.Error("Failed to initialize state manager", "err", err)
	}

	// Initialize Usage Manager
	usageMgr, err := indexer.GetUsageManager(stateMgr)
	if err != nil {
		logger.Error("Failed to initialize usage manager", "err", err)
	}

	// Initialize Internal Indexers (unified list)
	for _, idxCfg := range cfg.Indexers {
		if idxCfg.URL == "" {
			continue
		}

		indexerType := idxCfg.Type
		if indexerType == "" {
			indexerType = "newznab" // Default
		}

		switch indexerType {
		case "nzbhydra":
			hydraClient, err := nzbhydra.NewClient(idxCfg.URL, idxCfg.APIKey, idxCfg.Name, usageMgr)
			if err != nil {
				logger.Error("Failed to initialize NZBHydra2 from indexer list", "name", idxCfg.Name, "err", err)
			} else {
				indexers = append(indexers, hydraClient)
				logger.Info("Initialized NZBHydra2 from indexer list", "name", idxCfg.Name)
			}
		case "prowlarr":
			discovered, err := prowlarr.GetConfiguredIndexers(idxCfg.URL, idxCfg.APIKey, usageMgr)
			if err != nil {
				logger.Error("Failed to initialize Prowlarr from indexer list", "name", idxCfg.Name, "err", err)
			} else {
				if len(discovered) > 0 {
					indexers = append(indexers, discovered...)
					logger.Info("Initialized Prowlarr from indexer list", "name", idxCfg.Name, "count", len(discovered))
				}
			}
		default: // newznab
			client := newznab.NewClient(idxCfg, usageMgr)
			indexers = append(indexers, client)
			logger.Info("Initialized Newznab indexer", "name", idxCfg.Name, "url", idxCfg.URL)
		}
	}

	if len(indexers) == 0 {
		logger.Warn("!! No indexers (Internal/Hydra/Prowlarr) configured. Add some via the web UI or config.json !!")
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
