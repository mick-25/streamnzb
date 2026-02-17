package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"streamnzb/pkg/env"
	"streamnzb/pkg/logger"
	"streamnzb/pkg/paths"
)

// defaultAdminPasswordHash is SHA256("admin") - used when no admin password is set
const defaultAdminPasswordHash = "8c6976e5b5410415bde908bd4dee15dfb167a9c873fc4bb8a81f6f2ab448a918"

// Provider represents a Usenet provider configuration
type Provider struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	Connections int    `json:"connections"`
	UseSSL      bool   `json:"use_ssl"`
	Priority    *int   `json:"priority,omitempty"`    // Lower number = higher priority (1 = first, 2 = backup, etc.). nil = not set (old config)
	Enabled     *bool  `json:"enabled,omitempty"`     // Whether this provider is enabled. nil = not set (old config)
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

	// Visual tag filters (HDR and 3D)
	RequireHDR bool     `json:"require_hdr"` // Require any visual tag (HDR or 3D)
	AllowedHDR []string `json:"allowed_hdr"` // Allowed visual tags e.g., ["DV", "HDR10+", "3D"]
	BlockedHDR []string `json:"blocked_hdr"` // Blocked visual tags e.g., ["DV"] to block Dolby Vision, ["3D"] to block 3D
	BlockSDR   bool     `json:"block_sdr"`   // Block SDR releases

	// Language filters
	RequiredLanguages []string `json:"required_languages"` // e.g., ["en"]
	AllowedLanguages  []string `json:"allowed_languages"`  // e.g., ["en", "multi"]
	BlockDubbed       bool     `json:"block_dubbed"`

	// Other filters
	BlockCam       bool   `json:"block_cam"` // Block CAM/TS/TC
	RequireProper  bool   `json:"require_proper"`
	AllowRepack    bool   `json:"allow_repack"`
	BlockHardcoded bool   `json:"block_hardcoded"`
	MinBitDepth    string `json:"min_bit_depth"` // e.g., "10bit"

	// Size filters
	MinSizeGB float64 `json:"min_size_gb"`
	MaxSizeGB float64 `json:"max_size_gb"`

	// Group filters (blocking only)
	BlockedGroups []string `json:"blocked_groups"`
}

// DefaultFilterConfig returns built-in filter defaults for fresh devices.
func DefaultFilterConfig() FilterConfig {
	return FilterConfig{
		BlockedQualities: []string{"CAM", "TeleSync"},
		BlockCam:         true,
	}
}

// SortConfig holds weights for triage scoring
type SortConfig struct {
	ResolutionWeights map[string]int `json:"resolution_weights"`
	CodecWeights      map[string]int `json:"codec_weights"`
	AudioWeights      map[string]int `json:"audio_weights"`
	QualityWeights    map[string]int `json:"quality_weights"`
	VisualTagWeights  map[string]int `json:"visual_tag_weights"` // e.g., {"DV": 1500, "HDR10+": 1200, "HDR": 1000, "3D": 800}
	GrabWeight        float64        `json:"grab_weight"`
	AgeWeight         float64        `json:"age_weight"`

	// Preference boosts (prioritization, not filtering)
	PreferredGroups    []string `json:"preferred_groups"`    // e.g., ["FLUX", "NTb"]
	PreferredLanguages []string `json:"preferred_languages"` // e.g., ["en", "multi"]
}

// DefaultSortConfig returns built-in sort weights used when config has empty values.
func DefaultSortConfig() SortConfig {
	return SortConfig{
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
			"Atmos":  1500,
			"TrueHD": 1200,
			"DTS-HD": 1000,
			"DTS-X":  1000,
			"DTS":    500,
			"DD+":    400,
			"DD":     300,
			"AC3":    200,
			"5.1":    500,
			"7.1":    1000,
		},
		QualityWeights: map[string]int{
			"BluRay":  2000,
			"WEB-DL":  1500,
			"WEBRip":  1200,
			"HDTV":    1000,
			"Blu-ray": 2000,
		},
		VisualTagWeights: map[string]int{
			"DV":     1500,
			"HDR10+": 1200,
			"HDR":    1000,
			"3D":     800,
		},
		GrabWeight: 0.5,
		AgeWeight:  1.0,
	}
}

// IndexerConfig represents an internal Newznab indexer configuration
type IndexerConfig struct {
	Name         string `json:"name"`
	URL          string `json:"url"`
	APIKey       string `json:"api_key"`
	APIPath      string `json:"api_path"` // API path (default: "/api"), e.g., "/api" or "/api/v1"
	Type         string `json:"type"`     // "newznab", "prowlarr", "nzbhydra", "easynews"
	APIHitsDay   int    `json:"api_hits_day"`
	DownloadsDay int    `json:"downloads_day"`
	// Easynews-specific fields
	Username string `json:"username"` // Easynews username
	Password string `json:"password"` // Easynews password
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

	// Dashboard admin: stored in config.json (never send hash/token to frontend)
	AdminUsername           string `json:"admin_username"`
	AdminPasswordHash       string `json:"admin_password_hash"` // SHA256 hash; do not send to API clients
	AdminMustChangePassword bool   `json:"admin_must_change_password"`
	AdminToken              string `json:"admin_token"` // Single token for dashboard + streaming; do not send to API clients

	// Validation settings
	CacheTTLSeconds          int `json:"cache_ttl_seconds"`
	ValidationSampleSize     int `json:"validation_sample_size"`
	MaxStreams               int `json:"max_streams"`               // Max successful streams to return per search
	MaxStreamsPerResolution  int `json:"max_streams_per_resolution"` // Max streams per resolution (0 = disabled, use MaxStreams behavior)

	// NNTP Providers
	Providers []Provider `json:"providers"`

	// NNTP Proxy
	ProxyEnabled  bool   `json:"proxy_enabled"`
	ProxyPort     int    `json:"proxy_port"`
	ProxyHost     string `json:"proxy_host"`
	ProxyAuthUser string `json:"proxy_auth_user"`
	ProxyAuthPass string `json:"proxy_auth_pass"`

	// AvailNZB (Internal/Community)
	AvailNZBURL    string `json:"-"`
	AvailNZBAPIKey string `json:"-"`

	// TMDB Settings
	TMDBAPIKey string `json:"-"`

	// TVDB Settings
	TVDBAPIKey string `json:"-"`

	// Filtering
	Filters FilterConfig `json:"filters"`

	// Sorting
	Sorting SortConfig `json:"sorting"`

	// Internal - where was this config loaded from?
	LoadedPath string `json:"-"`
}

// GetAdminUsername returns the dashboard admin login username (default "admin").
func (c *Config) GetAdminUsername() string {
	if c != nil && c.AdminUsername != "" {
		return c.AdminUsername
	}
	return "admin"
}

// Load is intended for startup only. It loads configuration from config.json,
// applies environment variable overrides once, then saves the merged config.
// Environment variables are not read again after startup; subsequent reloads
// use only the saved config.
//
// Priority: Environment variables (if set) > config.json > defaults.
// When saving from the UI, values for keys that have an env override are preserved
// from the current effective config (so the file is not overwritten with form values
// that would be overridden by env on next restart). See CopyEnvOverridesFrom.
func Load() (*Config, error) {
	// 1. Determine config path using common data directory function
	dataDir := paths.GetDataDir()
	configPath := filepath.Join(dataDir, "config.json")
	// Ensure data directory exists
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		logger.Warn("Failed to create data directory", "dir", dataDir, "err", err)
	}

	// 2. Load config.json (or create with defaults if it doesn't exist)
	cfg := &Config{
		// Set defaults
		NZBHydra2URL:         "",
		AddonPort:            7000,
		AddonBaseURL:         "http://localhost:7000",
		LogLevel:             "INFO",
		AdminUsername:        "admin",
		CacheTTLSeconds:      300,
		ValidationSampleSize: 5,
		MaxStreams:              6,
		MaxStreamsPerResolution: 0, // 0 = disabled
		ProxyPort:            119,
		ProxyHost:            "0.0.0.0",
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
				"Atmos":  1500,
				"TrueHD": 1200,
				"DTS-HD": 1000,
				"DTS-X":  1000,
				"DTS":    500,
				"DD+":    400,
				"DD":     300,
				"AC3":    200,
				"5.1":    500,
				"7.1":    1000,
			},
			QualityWeights: map[string]int{
				"BluRay":  2000,
				"WEB-DL":  1500,
				"WEBRip":  1200,
				"HDTV":    1000,
				"Blu-ray": 2000,
			},
			VisualTagWeights: map[string]int{
				"DV":     1500,
				"HDR10+": 1200,
				"HDR":    1000,
				"3D":     800,
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

	// 3. Override with environment variables (single source: pkg/env)
	overrides, keys := env.ReadConfigOverrides()
	ApplyEnvOverrides(cfg, overrides, keys)

	// 4. Migrate legacy indexers
	cfg.MigrateLegacyIndexers()

	// 4.3. Apply provider defaults (priority and enabled)
	needSave := cfg.ApplyProviderDefaults()

	// 4.5. Ensure admin token and password hash defaults (do not overwrite if already set)
	if cfg.AdminToken == "" {
		bytes := make([]byte, 32)
		if _, err := rand.Read(bytes); err == nil {
			hash := sha256.Sum256(bytes)
			cfg.AdminToken = hex.EncodeToString(hash[:])
			needSave = true
		}
	}
	if cfg.AdminPasswordHash == "" {
		cfg.AdminPasswordHash = defaultAdminPasswordHash
		cfg.AdminMustChangePassword = true
		needSave = true
	}
	if needSave {
		logger.Info("Set default admin token/password in config")
	}

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

// ApplyProviderDefaults migrates old provider configs to new format (priority and enabled).
// This is a one-time migration: if priority is nil (not set), treat as old config and set defaults.
// Returns true if any changes were made (indicating config should be saved).
func (c *Config) ApplyProviderDefaults() bool {
	changed := false
	for i := range c.Providers {
		p := &c.Providers[i]
		// Migration: if priority is nil (not set in JSON), it's an old config - migrate to new format
		if p.Priority == nil {
			priority := i + 1
			p.Priority = &priority
			enabled := true
			p.Enabled = &enabled
			changed = true
		} else if p.Enabled == nil {
			// Priority was set but enabled is nil - also migrate enabled
			enabled := true
			p.Enabled = &enabled
			changed = true
		}
		// If both priority and enabled are set, it's already migrated - respect values as-is
	}
	return changed
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

// keySet returns true if s is in list.
func keySet(list []string, s string) bool {
	for _, k := range list {
		if k == s {
			return true
		}
	}
	return false
}

// ApplyEnvOverrides applies environment-derived overrides to cfg (used at startup only).
// Only fields present in keys are applied, so env vars override file values per setting.
func ApplyEnvOverrides(cfg *Config, o env.ConfigOverrides, keys []string) {
	if keySet(keys, env.KeyNZBHydraURL) {
		cfg.NZBHydra2URL = o.NZBHydra2URL
	}
	if keySet(keys, env.KeyNZBHydraAPIKey) {
		cfg.NZBHydra2APIKey = o.NZBHydra2APIKey
	}
	if keySet(keys, env.KeyProwlarrURL) {
		cfg.ProwlarrURL = o.ProwlarrURL
	}
	if keySet(keys, env.KeyProwlarrAPIKey) {
		cfg.ProwlarrAPIKey = o.ProwlarrAPIKey
	}
	if keySet(keys, env.KeyAddonPort) {
		cfg.AddonPort = o.AddonPort
	}
	if keySet(keys, env.KeyAddonBaseURL) {
		cfg.AddonBaseURL = o.AddonBaseURL
	}
	if keySet(keys, env.KeyLogLevel) {
		cfg.LogLevel = o.LogLevel
	}
	if keySet(keys, env.KeyCacheTTL) {
		cfg.CacheTTLSeconds = o.CacheTTLSeconds
	}
	if keySet(keys, env.KeyValidationSize) {
		cfg.ValidationSampleSize = o.ValidationSampleSize
	}
	if keySet(keys, env.KeyProxyEnabled) {
		cfg.ProxyEnabled = o.ProxyEnabled
	}
	if keySet(keys, env.KeyProxyPort) {
		cfg.ProxyPort = o.ProxyPort
	}
	if keySet(keys, env.KeyProxyHost) {
		cfg.ProxyHost = o.ProxyHost
	}
	if keySet(keys, env.KeyProxyAuthUser) {
		cfg.ProxyAuthUser = o.ProxyAuthUser
	}
	if keySet(keys, env.KeyProxyAuthPass) {
		cfg.ProxyAuthPass = o.ProxyAuthPass
	}
	if keySet(keys, env.KeyAdminUsername) {
		cfg.AdminUsername = o.AdminUsername
	}
	if keySet(keys, env.KeyProviders) {
		cfg.Providers = make([]Provider, len(o.Providers))
		for i, p := range o.Providers {
			var priority *int
			var enabled *bool
			if p.Priority != nil {
				priority = p.Priority
			}
			if p.Enabled != nil {
				enabled = p.Enabled
			}
			cfg.Providers[i] = Provider{
				Name:        p.Name,
				Host:        p.Host,
				Port:        p.Port,
				Username:    p.Username,
				Password:    p.Password,
				Connections: p.Connections,
				UseSSL:      p.UseSSL,
				Priority:    priority,
				Enabled:     enabled,
			}
		}
	}
	if keySet(keys, env.KeyIndexers) {
		cfg.Indexers = make([]IndexerConfig, len(o.Indexers))
		for i, idx := range o.Indexers {
			cfg.Indexers[i] = IndexerConfig{
				Name:   idx.Name,
				URL:    idx.URL,
				APIKey: idx.APIKey,
				Type:   "newznab",
			}
		}
	}
}

// GetEnvOverrideKeys returns config JSON keys that have environment variable overrides set.
// Used by the UI to show "overwritten on restart" warnings and when saving to preserve
// those values so the file is not overwritten with form data that env would override.
func GetEnvOverrideKeys() []string {
	return env.OverrideKeys()
}

// RedactForAPI returns a copy of the config with AdminPasswordHash and AdminToken cleared.
// Use when sending config to the frontend so sensitive values are never exposed.
func (c *Config) RedactForAPI() Config {
	out := *c
	out.AdminPasswordHash = ""
	out.AdminToken = ""
	return out
}

// CopyEnvOverridesFrom copies into dst the effective values for any key that has an
// environment override (from GetEnvOverrideKeys). Call before saving config from the UI
// so that env/ldflag-derived values are not overwritten by the form payload; the file
// then keeps the current effective values for those keys and env still wins on restart.
func CopyEnvOverridesFrom(src, dst *Config) {
	if src == nil || dst == nil {
		return
	}
	keys := env.OverrideKeys()
	for _, k := range keys {
		switch k {
		case env.KeyNZBHydraURL:
			dst.NZBHydra2URL = src.NZBHydra2URL
		case env.KeyNZBHydraAPIKey:
			dst.NZBHydra2APIKey = src.NZBHydra2APIKey
		case env.KeyProwlarrURL:
			dst.ProwlarrURL = src.ProwlarrURL
		case env.KeyProwlarrAPIKey:
			dst.ProwlarrAPIKey = src.ProwlarrAPIKey
		case env.KeyAddonPort:
			dst.AddonPort = src.AddonPort
		case env.KeyAddonBaseURL:
			dst.AddonBaseURL = src.AddonBaseURL
		case env.KeyLogLevel:
			dst.LogLevel = src.LogLevel
		case env.KeyCacheTTL:
			dst.CacheTTLSeconds = src.CacheTTLSeconds
		case env.KeyValidationSize:
			dst.ValidationSampleSize = src.ValidationSampleSize
		case env.KeyProxyEnabled:
			dst.ProxyEnabled = src.ProxyEnabled
		case env.KeyProxyPort:
			dst.ProxyPort = src.ProxyPort
		case env.KeyProxyHost:
			dst.ProxyHost = src.ProxyHost
		case env.KeyProxyAuthUser:
			dst.ProxyAuthUser = src.ProxyAuthUser
		case env.KeyProxyAuthPass:
			dst.ProxyAuthPass = src.ProxyAuthPass
		case env.KeyAdminUsername:
			dst.AdminUsername = src.AdminUsername
		case env.KeyProviders:
			dst.Providers = make([]Provider, len(src.Providers))
			for i, p := range src.Providers {
				var priority *int
				var enabled *bool
				if p.Priority != nil {
					priorityVal := *p.Priority
					priority = &priorityVal
				}
				if p.Enabled != nil {
					enabledVal := *p.Enabled
					enabled = &enabledVal
				}
				dst.Providers[i] = Provider{
					Name:        p.Name,
					Host:        p.Host,
					Port:        p.Port,
					Username:    p.Username,
					Password:    p.Password,
					Connections: p.Connections,
					UseSSL:      p.UseSSL,
					Priority:    priority,
					Enabled:     enabled,
				}
			}
		case env.KeyIndexers:
			dst.Indexers = make([]IndexerConfig, len(src.Indexers))
			copy(dst.Indexers, src.Indexers)
		}
	}
}
