package app

import (
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/initialization"
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvdb"
	"streamnzb/pkg/usenet/nntp"
	"streamnzb/pkg/usenet/validation"
)

// BuildOpts holds external dependencies (env/ldflags) not stored in config
type BuildOpts struct {
	AvailNZBURL    string
	AvailNZBAPIKey string
	TMDBAPIKey     string
	TVDBAPIKey     string
	DataDir        string
	SessionTTL     time.Duration
}

// Components holds all application components built from config
type Components struct {
	Config               *config.Config
	Indexer              indexer.Indexer
	ProviderPools        map[string]*nntp.ClientPool
	ProviderOrder        []string
	StreamingPools       []*nntp.ClientPool
	AvailNZBIndexerHosts []string
	Validator            *validation.Checker
	Triage               *triage.Service
	AvailClient          *availnzb.Client
	TMDBClient           *tmdb.Client
	TVDBClient           *tvdb.Client
}

// App centralizes service construction and granular reload
type App struct {
	mu         sync.RWMutex
	components *Components
	opts       BuildOpts
}

// New creates an App. Call Build to initialize components.
func New() *App {
	return &App{}
}

// Build constructs all components from config. Use for initial bootstrap.
func (a *App) Build(cfg *config.Config, opts BuildOpts) (*Components, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.opts = opts

	comp, err := a.buildFull(cfg, opts)
	if err != nil {
		return nil, err
	}
	a.components = comp
	return comp, nil
}

// buildFull runs full BuildComponents and builds validator, triage, avail, tmdb, tvdb
func (a *App) buildFull(cfg *config.Config, opts BuildOpts) (*Components, error) {
	base, err := initialization.BuildComponents(cfg)
	if err != nil {
		return nil, err
	}

	cacheTTL := time.Duration(cfg.CacheTTLSeconds) * time.Second
	validator := validation.NewChecker(
		base.ProviderPools,
		base.ProviderOrder,
		cacheTTL,
		cfg.ValidationSampleSize,
		6,
	)
	triageSvc := triage.NewService(&cfg.Filters, cfg.Sorting)
	availClient := availnzb.NewClient(opts.AvailNZBURL, opts.AvailNZBAPIKey)
	dataDir := opts.DataDir
	if dataDir == "" {
		dataDir = filepath.Dir(cfg.LoadedPath)
	}
	if dataDir == "" || dataDir == "." {
		dataDir, _ = filepath.Abs(".")
	}
	tmdbClient := tmdb.NewClient(opts.TMDBAPIKey)
	tvdbClient := tvdb.NewClient(opts.TVDBAPIKey, dataDir)

	return &Components{
		Config:               base.Config,
		Indexer:              base.Indexer,
		ProviderPools:        base.ProviderPools,
		ProviderOrder:        base.ProviderOrder,
		StreamingPools:       base.StreamingPools,
		AvailNZBIndexerHosts: base.AvailNZBIndexerHosts,
		Validator:            validator,
		Triage:               triageSvc,
		AvailClient:          availClient,
		TMDBClient:           tmdbClient,
		TVDBClient:           tvdbClient,
	}, nil
}

// ReloadScope indicates what changed and needs reloading
type ReloadScope int

const (
	ReloadConfigOnly  ReloadScope = iota // Filters, Sorting, MaxStreams, LogLevel - no NNTP/indexer restart
	ReloadIndexers                       // Indexers changed
	ReloadProviders                      // Providers changed - full pool rebuild
	ReloadProxy                          // Proxy settings changed
	ReloadFull                           // Indexers or Providers changed - full rebuild
)

// ConfigChanged returns what scope of reload is needed
func ConfigChanged(old, new_ *config.Config) ReloadScope {
	if old == nil || new_ == nil {
		return ReloadFull
	}

	indexersChanged := !reflect.DeepEqual(old.Indexers, new_.Indexers)
	providersChanged := !reflect.DeepEqual(old.Providers, new_.Providers)
	proxyChanged := old.ProxyEnabled != new_.ProxyEnabled ||
		old.ProxyHost != new_.ProxyHost ||
		old.ProxyPort != new_.ProxyPort ||
		old.ProxyAuthUser != new_.ProxyAuthUser ||
		old.ProxyAuthPass != new_.ProxyAuthPass
	validationChanged := old.CacheTTLSeconds != new_.CacheTTLSeconds ||
		old.ValidationSampleSize != new_.ValidationSampleSize

	if providersChanged || indexersChanged {
		return ReloadFull
	}
	if proxyChanged || validationChanged {
		return ReloadFull // Validator depends on validation settings
	}
	return ReloadConfigOnly
}

// Reload performs granular reload based on config diff.
// Returns (components, fullReload). When fullReload is false, caller must NOT shutdown
// NNTP pools or restart proxy - only update config, triage, stremio.
func (a *App) Reload(newCfg *config.Config) (*Components, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	old := a.components
	scope := ConfigChanged(old.Config, newCfg)

	switch scope {
	case ReloadConfigOnly:
		// No pool/indexer rebuild - just config + triage
		logger.Info("Reload: config-only (filters, sorting, limits) - no NNTP/indexer restart")
		triageSvc := triage.NewService(&newCfg.Filters, newCfg.Sorting)
		comp := *old
		comp.Config = newCfg
		comp.Triage = triageSvc
		a.components = &comp
		return &comp, false, nil

	case ReloadFull:
		logger.Info("Reload: full rebuild (indexers or providers changed)")
		comp, err := a.buildFull(newCfg, a.opts)
		if err != nil {
			return nil, true, err
		}
		a.components = comp
		return comp, true, nil

	case ReloadProxy:
		logger.Info("Reload: proxy config changed")
		comp, err := a.buildFull(newCfg, a.opts)
		if err != nil {
			return nil, true, err
		}
		a.components = comp
		return comp, true, nil

	default:
		logger.Info("Reload: indexers changed - full rebuild")
		comp, err := a.buildFull(newCfg, a.opts)
		if err != nil {
			return nil, true, err
		}
		a.components = comp
		return comp, true, nil
	}
}

// Components returns the current components (read-only). Safe for concurrent read.
func (a *App) Components() *Components {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.components
}
