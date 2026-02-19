package initialization

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/paths"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/indexer/easynews"
	"streamnzb/pkg/indexer/newznab"
	"streamnzb/pkg/usenet/nntp"
	"strings"
)

// InitializedComponents holds all the components initialized during bootstrap
type InitializedComponents struct {
	Config               *config.Config
	Indexer              indexer.Indexer
	ProviderPools        map[string]*nntp.ClientPool
	ProviderOrder        []string // Provider names in priority order (for single-provider validation)
	StreamingPools       []*nntp.ClientPool
	AvailNZBIndexerHosts []string // Underlying indexer hostnames for AvailNZB GetReleases filter (e.g. nzbgeek.info)
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
		if indexerType == "nzbhydra" || indexerType == "prowlarr" {
			logger.Warn("NZBHydra and Prowlarr are no longer supported; skipping indexer", "name", idxCfg.Name)
			continue
		}

		switch indexerType {
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
		logger.Warn("!! No indexers configured. Add some via the web UI or config.json !!")
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

	// Sort providers by priority (lower number = higher priority) and filter disabled ones
	// Note: Migration from old config format happens in config.Load(), not here
	providers := make([]config.Provider, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		// Only include enabled providers (check pointer)
		if p.Enabled != nil && *p.Enabled {
			providers = append(providers, p)
		}
	}
	// Sort by priority (ascending: 1, 2, 3...)
	sort.Slice(providers, func(i, j int) bool {
		priI := 999
		priJ := 999
		if providers[i].Priority != nil {
			priI = *providers[i].Priority
		}
		if providers[j].Priority != nil {
			priJ = *providers[j].Priority
		}
		return priI < priJ
	})

	providerOrder := make([]string, 0, len(providers))
	for _, provider := range providers {
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
		providerOrder = append(providerOrder, poolName)
		streamingPools = append(streamingPools, pool)
	}

	if len(providerPools) == 0 {
		logger.Warn("!! No valid NNTP providers initialized. Check your credentials in the web UI !!")
	}

	return &InitializedComponents{
		Config:               cfg,
		Indexer:              aggregator,
		ProviderPools:        providerPools,
		ProviderOrder:        providerOrder,
		StreamingPools:       streamingPools,
		AvailNZBIndexerHosts: availNzbHosts,
	}, nil
}
