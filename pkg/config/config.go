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

// Config holds application configuration
type Config struct {
	// NZBHydra2 settings
	NZBHydra2URL    string `json:"nzbhydra_url"`
	NZBHydra2APIKey string `json:"nzbhydra_api_key"`
	
	// Prowlarr settings
	ProwlarrURL    string `json:"prowlarr_url"`
	ProwlarrAPIKey string `json:"prowlarr_api_key"`
	
	// Addon settings
	AddonPort    int    `json:"addon_port"`
	AddonBaseURL string `json:"addon_base_url"`
	LogLevel     string `json:"log_level"`
	
	// Validation settings
	CacheTTLSeconds          int `json:"cache_ttl_seconds"`
	ValidationSampleSize     int `json:"validation_sample_size"`
	MaxConcurrentValidations int `json:"max_concurrent_validations"`
	
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
	
	// Internal - where was this config loaded from?
	LoadedPath string `json:"-"`
}

// Load loads configuration from environment variables AND config.json if present
func Load() (*Config, error) {
    // 1. Initialize with Env Vars (Defaults)
	cfg := &Config{
		NZBHydra2URL:             getEnv("NZBHYDRA2_URL", "http://localhost:5076"),
		NZBHydra2APIKey:          getEnv("NZBHYDRA2_API_KEY", ""),
		ProwlarrURL:              getEnv("PROWLARR_URL", ""),
		ProwlarrAPIKey:           getEnv("PROWLARR_API_KEY", ""),
		AddonPort:                getEnvInt("ADDON_PORT", 7000),
		AddonBaseURL:             getEnv("ADDON_BASE_URL", "http://localhost:7000"),
		LogLevel:                 getEnv("LOG_LEVEL", "INFO"),
		CacheTTLSeconds:          getEnvInt("CACHE_TTL_SECONDS", 3600),
		ValidationSampleSize:     getEnvInt("VALIDATION_SAMPLE_SIZE", 5),
		MaxConcurrentValidations: getEnvInt("MAX_CONCURRENT_VALIDATIONS", 20),
		SecurityToken:            getEnv("SECURITY_TOKEN", ""),
		AvailNZBURL:              getEnv("AVAILNZB_URL", ""),
		AvailNZBAPIKey:           getEnv("AVAILNZB_API_KEY", ""),
		TMDBAPIKey:               getEnv("TMDB_API_KEY", ""),
	}
	
	cfg.Providers = loadProviders()
	
	cfg.ProxyEnabled = getEnvBool("NNTP_PROXY_ENABLED", false)
	cfg.ProxyPort = getEnvInt("NNTP_PROXY_PORT", 119)
	cfg.ProxyHost = getEnv("NNTP_PROXY_HOST", "0.0.0.0")
	cfg.ProxyAuthUser = getEnv("NNTP_PROXY_AUTH_USER", "")
	cfg.ProxyAuthPass = getEnv("NNTP_PROXY_AUTH_PASS", "")

    // 2. Override with config.json if it exists
    // Priority: /app/data/config.json (Docker volume) > ./config.json (Local)
    configPath := "config.json"
    if _, err := os.Stat("/app/data/config.json"); err == nil {
        configPath = "/app/data/config.json"
        logger.Info("Loading configuration", "path", "/app/data/config.json")
    } else if _, err := os.Stat("config.json"); err == nil {
        logger.Info("Loading configuration", "path", "./config.json")
    } else {
        // No config file found.
        // If /app/data directory exists (Docker volume), default to saving there.
        if info, err := os.Stat("/app/data"); err == nil && info.IsDir() {
            configPath = "/app/data/config.json"
            logger.Info("No config found. Will save new configuration", "path", "/app/data/config.json")
        }
    }

    // Set the loaded path so we know where to save back to
    cfg.LoadedPath = configPath

    if err := cfg.LoadFile(configPath); err != nil && !os.IsNotExist(err) {
        logger.Warn("Failed to load config", "path", configPath, "err", err)
    }
	
	// Note: We no longer enforce at least one provider during Load to allow
	// the application to start "empty" and be configured via the web UI.
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
		if host == "" { continue }
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
