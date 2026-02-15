package initialization

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"streamnzb/pkg/config"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/indexer/easynews"
	"streamnzb/pkg/indexer/newznab"
	"streamnzb/pkg/logger"
	"streamnzb/pkg/nntp"
	"streamnzb/pkg/nzbhydra"
	"streamnzb/pkg/paths"
	"streamnzb/pkg/persistence"
	"streamnzb/pkg/prowlarr"
)

// InitializedComponents holds all the components initialized during bootstrap
type InitializedComponents struct {
	Config                *config.Config
	Indexer               indexer.Indexer
	ProviderPools         map[string]*nntp.ClientPool
	StreamingPools        []*nntp.ClientPool
	AvailNZBIndexerHosts  []string // Underlying indexer hostnames for AvailNZB GetReleases filter (e.g. nzbgeek.info)
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

// hostFromIndexerURL returns hostname for AvailNZB (lowercase, no api. prefix).
func hostFromIndexerURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	h := strings.ToLower(strings.TrimSpace(u.Hostname()))
	return strings.TrimPrefix(h, "api.")
}

// BuildComponents builds all system modules from the provided configuration
func BuildComponents(cfg *config.Config) (*InitializedComponents, error) {
	// 2. Initialize Indexers
	var indexers []indexer.Indexer
	var availNzbHosts []string
	seenHost := make(map[string]bool)

	// Initialize State Manager
	dataDir := paths.GetDataDir()
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
			// Try to discover individual indexers first
			discovered, hydraHosts, err := nzbhydra.GetConfiguredIndexers(idxCfg.URL, idxCfg.APIKey, usageMgr)
			if err != nil {
				// Fall back to single aggregated client if discovery fails
				logger.Debug("NZBHydra2 indexer discovery failed, using aggregated client", "err", err)
				hydraClient, err := nzbhydra.NewClient(idxCfg.URL, idxCfg.APIKey, idxCfg.Name, usageMgr)
				if err != nil {
					logger.Error("Failed to initialize NZBHydra2 from indexer list", "name", idxCfg.Name, "err", err)
				} else {
					indexers = append(indexers, hydraClient)
					logger.Info("Initialized NZBHydra2 aggregated client", "name", idxCfg.Name)
					// Fallback: use Hydra host so AvailNZB may still return something
					if h := hostFromIndexerURL(idxCfg.URL); h != "" && !seenHost[h] {
						seenHost[h] = true
						availNzbHosts = append(availNzbHosts, h)
					}
				}
			} else {
				if len(discovered) > 0 {
					indexers = append(indexers, discovered...)
					logger.Info("Initialized NZBHydra2 indexers from discovery", "name", idxCfg.Name, "count", len(discovered))
					for _, h := range hydraHosts {
						if h != "" && !seenHost[h] {
							seenHost[h] = true
							availNzbHosts = append(availNzbHosts, h)
						}
					}
				} else {
					// Fall back to aggregated client if no indexers discovered
					hydraClient, err := nzbhydra.NewClient(idxCfg.URL, idxCfg.APIKey, idxCfg.Name, usageMgr)
					if err != nil {
						logger.Error("Failed to initialize NZBHydra2 from indexer list", "name", idxCfg.Name, "err", err)
					} else {
						indexers = append(indexers, hydraClient)
						logger.Info("Initialized NZBHydra2 aggregated client (no indexers discovered)", "name", idxCfg.Name)
						if h := hostFromIndexerURL(idxCfg.URL); h != "" && !seenHost[h] {
							seenHost[h] = true
							availNzbHosts = append(availNzbHosts, h)
						}
					}
				}
			}
		case "prowlarr":
			discovered, prowlarrHosts, err := prowlarr.GetConfiguredIndexers(idxCfg.URL, idxCfg.APIKey, usageMgr)
			if err != nil {
				logger.Error("Failed to initialize Prowlarr from indexer list", "name", idxCfg.Name, "err", err)
			} else {
				if len(discovered) > 0 {
					indexers = append(indexers, discovered...)
					logger.Info("Initialized Prowlarr from indexer list", "name", idxCfg.Name, "count", len(discovered))
				}
				for _, h := range prowlarrHosts {
					if h != "" && !seenHost[h] {
						seenHost[h] = true
						availNzbHosts = append(availNzbHosts, h)
					}
				}
				// Fallback if API didn't return IndexerUrls
				if len(prowlarrHosts) == 0 {
					if h := hostFromIndexerURL(idxCfg.URL); h != "" && !seenHost[h] {
						seenHost[h] = true
						availNzbHosts = append(availNzbHosts, h)
					}
				}
			}
		case "easynews":
			// Determine download base URL (for proxying NZB downloads)
			downloadBase := cfg.AddonBaseURL
			if downloadBase == "" {
				downloadBase = "http://127.0.0.1:7000"
			}
			// Remove trailing slash
			if len(downloadBase) > 0 && downloadBase[len(downloadBase)-1] == '/' {
				downloadBase = downloadBase[:len(downloadBase)-1]
			}

			easynewsClient, err := easynews.NewClient(idxCfg.Username, idxCfg.Password, idxCfg.Name, downloadBase, idxCfg.APIHitsDay, idxCfg.DownloadsDay, usageMgr)
			if err != nil {
				logger.Error("Failed to initialize Easynews from indexer list", "name", idxCfg.Name, "err", err)
			} else {
				indexers = append(indexers, easynewsClient)
				logger.Info("Initialized Easynews indexer", "name", idxCfg.Name)
			}
			if h := "members.easynews.com"; !seenHost[h] {
				seenHost[h] = true
				availNzbHosts = append(availNzbHosts, h)
			}
		default: // newznab
			client := newznab.NewClient(idxCfg, usageMgr)
			indexers = append(indexers, client)
			logger.Info("Initialized Newznab indexer", "name", idxCfg.Name, "url", idxCfg.URL)
			if h := hostFromIndexerURL(idxCfg.URL); h != "" && !seenHost[h] {
				seenHost[h] = true
				availNzbHosts = append(availNzbHosts, h)
			}
		}
	}

	if len(indexers) == 0 {
		logger.Warn("!! No indexers (Internal/Hydra/Prowlarr) configured. Add some via the web UI or config.json !!")
	}

	aggregator := indexer.NewAggregator(indexers...)

	// 3. Initialize NNTP provider pools
	providerPools := make(map[string]*nntp.ClientPool)
	var streamingPools []*nntp.ClientPool

	// Initialize provider usage manager (may be nil if stateMgr failed)
	var providerUsageMgr *nntp.ProviderUsageManager
	if stateMgr != nil {
		if mgr, err := nntp.GetProviderUsageManager(stateMgr); err != nil {
			logger.Error("Failed to initialize provider usage manager", "err", err)
		} else {
			providerUsageMgr = mgr
		}
	}

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

		// Restore persisted usage if available and configure persistence
		if providerUsageMgr != nil {
			if usage := providerUsageMgr.GetUsage(poolName); usage != nil {
				pool.RestoreTotalBytes(usage.TotalBytes)
			}
			pool.SetUsageManager(poolName, providerUsageMgr)
		}

		providerPools[poolName] = pool
		streamingPools = append(streamingPools, pool)
	}

	if len(providerPools) == 0 {
		logger.Warn("!! No valid NNTP providers initialized. Check your credentials in the web UI !!")
	}

	return &InitializedComponents{
		Config:               cfg,
		Indexer:              aggregator,
		ProviderPools:        providerPools,
		StreamingPools:       streamingPools,
		AvailNZBIndexerHosts: availNzbHosts,
	}, nil
}
