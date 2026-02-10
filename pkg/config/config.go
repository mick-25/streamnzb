package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"streamnzb/pkg/logger"
	"strings"
)

// Provider represents a Usenet provider configuration
type Provider struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	Connections int    `json:"connections"`
	UseSSL      bool   `json:"use_ssl"`
}

// FilterConfig holds user filtering preferences for PTT-based release filtering
type FilterConfig struct {
	// Quality filters
	AllowedQualities []string `json:"allowed_qualities"` // e.g., ["BluRay", "WEB-DL", "HDTV"]
	BlockedQualities []string `json:"blocked_qualities"` // e.g., ["CAM", "TeleSync"]
	
	// Resolution filters
	MinResolution string `json:"min_resolution"` // e.g., "720p"
	MaxResolution string `json:"max_resolution"` // e.g., "2160p"
	
	// Codec filters
	AllowedCodecs []string `json:"allowed_codecs"` // e.g., ["HEVC", "AVC"]
	BlockedCodecs []string `json:"blocked_codecs"` // e.g., ["MPEG-2"]
	
	// Audio filters
	RequiredAudio []string `json:"required_audio"` // e.g., ["Atmos", "TrueHD"]
	AllowedAudio  []string `json:"allowed_audio"`  // e.g., ["DTS", "DD", "AAC"]
	MinChannels   string   `json:"min_channels"`   // e.g., "5.1"
	
	// HDR filters
	RequireHDR bool     `json:"require_hdr"` // Require any HDR
	AllowedHDR []string `json:"allowed_hdr"` // e.g., [" DV", "HDR10+"]
	BlockedHDR []string `json:"blocked_hdr"` // e.g., ["DV"] to block Dolby Vision
	BlockSDR   bool     `json:"block_sdr"`   // Block SDR releases
	
	// Language filters
	RequiredLanguages []string `json:"required_languages"` // e.g., ["en"]
	AllowedLanguages  []string `json:"allowed_languages"`  // e.g., ["en", "multi"]
	BlockDubbed       bool     `json:"block_dubbed"`
	
	// Other filters
	BlockCam       bool   `json:"block_cam"`        // Block CAM/TS/TC
	RequireProper  bool   `json:"require_proper"`
	AllowRepack    bool   `json:"allow_repack"`
	BlockHardcoded bool   `json:"block_hardcoded"`
	MinBitDepth    string `json:"min_bit_depth"` // e.g., "10bit"
	
	// Size filters
	MinSizeGB float64 `json:"min_size_gb"`
	MaxSizeGB float64 `json:"max_size_gb"`
	
	// Group filters (blocking only)
	BlockedGroups   []string `json:"blocked_groups"`
}

// SortConfig holds weights for triage scoring
type SortConfig struct {
	ResolutionWeights map[string]int `json:"resolution_weights"`
	CodecWeights      map[string]int `json:"codec_weights"`
	AudioWeights      map[string]int `json:"audio_weights"`
	QualityWeights    map[string]int `json:"quality_weights"`
	GrabWeight        float64        `json:"grab_weight"`
	AgeWeight         float64        `json:"age_weight"`
	
	// Preference boosts (prioritization, not filtering)
	PreferredGroups    []string `json:"preferred_groups"`    // e.g., ["FLUX", "NTb"]
	PreferredLanguages []string `json:"preferred_languages"` // e.g., ["en", "multi"]
}

// IndexerConfig represents an internal Newznab indexer configuration
type IndexerConfig struct {
	Name         string `json:"name"`
	URL          string `json:"url"`
	APIKey       string `json:"api_key"`
	APIPath      string `json:"api_path"` // API path (default: "/api"), e.g., "/api" or "/api/v1"
	Type         string `json:"type"`      // "newznab", "prowlarr", "nzbhydra", "easynews"
	APIHitsDay   int    `json:"api_hits_day"`
	DownloadsDay int    `json:"downloads_day"`
	// Easynews-specific fields
	Username   string `json:"username"`    // Easynews username
	Password   string `json:"password"`    // Easynews password
}

// Config holds application configuration
type Config struct {
	// NZBHydra2 settings
	NZBHydra2URL    string `json:"nzbhydra_url"`
	NZBHydra2APIKey string `json:"nzbhydra_api_key"`

	// Prowlarr settings
	ProwlarrURL    string `json:"prowlarr_url"`
	ProwlarrAPIKey string `json:"prowlarr_api_key"`

	// Internal Indexers
	Indexers []IndexerConfig `json:"indexers"`

	// Addon settings
	AddonPort    int    `json:"addon_port"`
	AddonBaseURL string `json:"addon_base_url"`
	LogLevel     string `json:"log_level"`

	// Validation settings
	CacheTTLSeconds      int `json:"cache_ttl_seconds"`
	ValidationSampleSize int `json:"validation_sample_size"`
	MaxStreams           int `json:"max_streams"` // Max successful streams to return per search

	// NNTP Providers
	Providers []Provider `json:"providers"`

	// NNTP Proxy
	ProxyEnabled  bool   `json:"proxy_enabled"`
	ProxyPort     int    `json:"proxy_port"`
	ProxyHost     string `json:"proxy_host"`
	ProxyAuthUser string `json:"proxy_auth_user"`
	ProxyAuthPass string `json:"proxy_auth_pass"`

	// Security
	SecurityToken string `json:"security_token"`

	// AvailNZB (Internal/Community)
	AvailNZBURL    string `json:"-"`
	AvailNZBAPIKey string `json:"-"`

	// TMDB Settings
	TMDBAPIKey string `json:"-"`
	
	// Filtering
	Filters FilterConfig `json:"filters"`

	// Sorting
	Sorting SortConfig `json:"sorting"`

	// Internal - where was this config loaded from?
	LoadedPath string `json:"-"`
}

// Load loads configuration from config.json, overrides with env vars, and saves
// Priority: Environment variables (if not empty) > config.json > defaults
func Load() (*Config, error) {
	// 1. Determine config path
	// If running in Docker (/.dockerenv exists), use /app/data/config.json
	// Otherwise use ./config.json (local development)
	configPath := "config.json"
	if _, err := os.Stat("/.dockerenv"); err == nil {
		// Running in Docker container
		configPath = "/app/data/config.json"
		// Ensure /app/data directory exists
		if err := os.MkdirAll("/app/data", 0755); err != nil {
			logger.Warn("Failed to create /app/data directory", "err", err)
		}
	}

	// 2. Load config.json (or create with defaults if it doesn't exist)
	cfg := &Config{
		// Set defaults
		NZBHydra2URL:             "",
		AddonPort:                7000,
		AddonBaseURL:             "http://localhost:7000",
		LogLevel:                 "INFO",
		CacheTTLSeconds:          300,
		ValidationSampleSize:     5,
		MaxStreams:               6,
		ProxyPort:                119,
		ProxyHost:                "0.0.0.0",
		Sorting: SortConfig{
			ResolutionWeights: map[string]int{
				"4k":    4000000,
				"1080p": 3000000,
				"720p":  2000000,
				"sd":    1000000,
			},
			CodecWeights: map[string]int{
				"HEVC": 1000,
				"x265": 1000,
				"x264": 500,
				"AVC":  500,
			},
			AudioWeights: map[string]int{
				"Atmos":   1500,
				"TrueHD":  1200,
				"DTS-HD":  1000,
				"DTS-X":   1000,
				"DTS":     500,
				"DD+":     400,
				"DD":      300,
				"AC3":     200,
				"5.1":     500,
				"7.1":     1000,
			},
			QualityWeights: map[string]int{
				"BluRay":  2000,
				"WEB-DL":  1500,
				"WEBRip":  1200,
				"HDTV":    1000,
				"Blu-ray": 2000,
			},
			GrabWeight: 0.5,
			AgeWeight:  1.0,
		},
		LoadedPath: configPath,
	}

	// Try to load existing config
	if err := cfg.LoadFile(configPath); err != nil {
		if os.IsNotExist(err) {
			logger.Info("No config found, creating new one", "path", configPath)
		} else {
			logger.Warn("Failed to load config, using defaults", "path", configPath, "err", err)
		}
	} else {
		logger.Info("Loaded configuration", "path", configPath)
	}

	// 3. Override with environment variables (if not empty)
	if val := os.Getenv("NZBHYDRA2_URL"); val != "" {
		cfg.NZBHydra2URL = val
	}
	if val := os.Getenv("NZBHYDRA2_API_KEY"); val != "" {
		cfg.NZBHydra2APIKey = val
	}
	if val := os.Getenv("PROWLARR_URL"); val != "" {
		cfg.ProwlarrURL = val
	}
	if val := os.Getenv("PROWLARR_API_KEY"); val != "" {
		cfg.ProwlarrAPIKey = val
	}
	if val := os.Getenv("ADDON_PORT"); val != "" {
		if port, err := strconv.Atoi(val); err == nil {
			cfg.AddonPort = port
		}
	}
	if val := os.Getenv("ADDON_BASE_URL"); val != "" {
		cfg.AddonBaseURL = val
	}
	if val := os.Getenv("LOG_LEVEL"); val != "" {
		cfg.LogLevel = val
	}
	if val := os.Getenv("CACHE_TTL_SECONDS"); val != "" {
		if ttl, err := strconv.Atoi(val); err == nil {
			cfg.CacheTTLSeconds = ttl
		}
	}
	if val := os.Getenv("VALIDATION_SAMPLE_SIZE"); val != "" {
		if size, err := strconv.Atoi(val); err == nil {
			cfg.ValidationSampleSize = size
		}
	}
	if val := os.Getenv("SECURITY_TOKEN"); val != "" {
		cfg.SecurityToken = val
	}
	if val := os.Getenv("AVAILNZB_URL"); val != "" {
		cfg.AvailNZBURL = val
	}
	if val := os.Getenv("AVAILNZB_API_KEY"); val != "" {
		cfg.AvailNZBAPIKey = val
	}
	if val := os.Getenv("TMDB_API_KEY"); val != "" {
		cfg.TMDBAPIKey = val
	}

	// Proxy settings
	if val := os.Getenv("NNTP_PROXY_ENABLED"); val != "" {
		cfg.ProxyEnabled = val == "true" || val == "1"
	}
	if val := os.Getenv("NNTP_PROXY_PORT"); val != "" {
		if port, err := strconv.Atoi(val); err == nil {
			cfg.ProxyPort = port
		}
	}
	if val := os.Getenv("NNTP_PROXY_HOST"); val != "" {
		cfg.ProxyHost = val
	}
	if val := os.Getenv("NNTP_PROXY_AUTH_USER"); val != "" {
		cfg.ProxyAuthUser = val
	}
	if val := os.Getenv("NNTP_PROXY_AUTH_PASS"); val != "" {
		cfg.ProxyAuthPass = val
	}

	// Load providers from env vars (if any)
	envProviders := loadProviders()
	if len(envProviders) > 0 {
		cfg.Providers = envProviders
	}

	// Load internal indexers from env vars (if any)
	envIndexers := loadIndexers()
	if len(envIndexers) > 0 {
		cfg.Indexers = envIndexers
	}

	// 4. Migrate legacy indexers
	cfg.MigrateLegacyIndexers()

	// 5. Save the merged configuration
	if err := cfg.Save(); err != nil {
		logger.Warn("Failed to save config on startup", "err", err)
	} else {
		logger.Info("Saved merged configuration", "path", configPath)
	}

	// Warn if no providers configured
	if len(cfg.Providers) == 0 {
		logger.Warn("No NNTP providers configured. Add some via the web UI")
	}

	return cfg, nil
}

// LoadFile overrides config with values from a JSON file
func (c *Config) LoadFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(c); err != nil {
		return err
	}
	return nil
}

// MigrateLegacyIndexers moves old Prowlarr/Hydra settings into the unified Indexers list
func (c *Config) MigrateLegacyIndexers() {
	migrated := false

	// Migrate NZBHydra2
	if c.NZBHydra2APIKey != "" {
		migratedURL := strings.TrimRight(c.NZBHydra2URL, "/")
		exists := false
		for _, idx := range c.Indexers {
			if idx.Type == "nzbhydra" && strings.TrimRight(idx.URL, "/") == migratedURL {
				exists = true
				break
			}
		}
		if !exists && migratedURL != "" {
			c.Indexers = append(c.Indexers, IndexerConfig{
				Name:   "NZBHydra2 (Migrated)",
				URL:    migratedURL,
				APIKey: c.NZBHydra2APIKey,
				Type:   "nzbhydra",
			})
			logger.Debug("Migrated NZBHydra2", "url", migratedURL)
			migrated = true
		}
		c.NZBHydra2APIKey = "" // Clear legacy
		c.NZBHydra2URL = ""

	}

	// Migrate Prowlarr
	if c.ProwlarrAPIKey != "" {
		migratedURL := strings.TrimRight(c.ProwlarrURL, "/")
		exists := false
		for _, idx := range c.Indexers {
			if idx.Type == "prowlarr" && strings.TrimRight(idx.URL, "/") == migratedURL {
				exists = true
				break
			}
		}
		if !exists && migratedURL != "" {
			c.Indexers = append(c.Indexers, IndexerConfig{
				Name:   "Prowlarr (Migrated)",
				URL:    migratedURL,
				APIKey: c.ProwlarrAPIKey,
				Type:   "prowlarr",
			})
			logger.Debug("Migrated Prowlarr", "url", migratedURL)
			migrated = true
		}
		c.ProwlarrAPIKey = "" // Clear legacy
		c.ProwlarrURL = ""

	}

	if migrated {
		logger.Info("Migrated legacy meta-indexers to unified Indexers list")
	}
}

// Save saves the current configuration to the file it was loaded from
func (c *Config) Save() error {
	path := c.LoadedPath
	if path == "" {
		path = "config.json"
	}
	return c.SaveFile(path)
}

// SaveFile saves the current configuration to a JSON file
func (c *Config) SaveFile(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(c)
}

// loadProviders loads provider configurations from environment
func loadProviders() []Provider {
	var providers []Provider
	for i := 1; i <= 10; i++ {
		prefix := fmt.Sprintf("PROVIDER_%d_", i)
		host := os.Getenv(prefix + "HOST")
		if host == "" {
			continue
		}
		provider := Provider{
			Name:        getEnv(prefix+"NAME", fmt.Sprintf("Provider %d", i)),
			Host:        host,
			Port:        getEnvInt(prefix+"PORT", 563),
			Username:    os.Getenv(prefix + "USERNAME"),
			Password:    os.Getenv(prefix + "PASSWORD"),
			Connections: getEnvInt(prefix+"CONNECTIONS", 10),
			UseSSL:      getEnvBool(prefix+"SSL", true),
		}
		providers = append(providers, provider)
	}
	return providers
}

// loadIndexers loads indexer configurations from environment
func loadIndexers() []IndexerConfig {
	var indexers []IndexerConfig
	for i := 1; i <= 10; i++ {
		prefix := fmt.Sprintf("INDEXER_%d_", i)
		url := os.Getenv(prefix + "URL")
		if url == "" {
			continue
		}
		indexer := IndexerConfig{
			Name:   getEnv(prefix+"NAME", fmt.Sprintf("Indexer %d", i)),
			URL:    url,
			APIKey: os.Getenv(prefix + "API_KEY"),
		}
		indexers = append(indexers, indexer)
	}
	return indexers
}

// Helper functions (Unchanged)
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		return strings.ToLower(value) == "true" || value == "1"
	}
	return defaultValue
}
