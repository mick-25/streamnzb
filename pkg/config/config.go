package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds application configuration
type Config struct {
	// NZBHydra2 settings
	NZBHydra2URL    string
	NZBHydra2APIKey string
	
	// Addon settings
	AddonPort    int
	AddonBaseURL string
	
	// Validation settings
	CacheTTLSeconds          int
	ValidationSampleSize     int
	MaxConcurrentValidations int
	
	// NNTP Providers
	Providers []Provider
	
	// NNTP Proxy
	ProxyEnabled  bool
	ProxyPort     int
	ProxyHost     string
	ProxyAuthUser string
	ProxyAuthPass string
	
	// Security
	SecurityToken string
}

// Provider represents a Usenet provider configuration
type Provider struct {
	Name        string
	Host        string
	Port        int
	Username    string
	Password    string
	Connections int
	UseSSL      bool
}

// Load loads configuration from environment variables
func Load() (*Config, error) {
	cfg := &Config{
		NZBHydra2URL:             getEnv("NZBHYDRA2_URL", "http://localhost:5076"),
		NZBHydra2APIKey:          getEnv("NZBHYDRA2_API_KEY", ""),
		AddonPort:                getEnvInt("ADDON_PORT", 7000),
		AddonBaseURL:             getEnv("ADDON_BASE_URL", "http://localhost:7000"),
		CacheTTLSeconds:          getEnvInt("CACHE_TTL_SECONDS", 3600),
		ValidationSampleSize:     getEnvInt("VALIDATION_SAMPLE_SIZE", 5),
		MaxConcurrentValidations: getEnvInt("MAX_CONCURRENT_VALIDATIONS", 20),
		SecurityToken:            os.Getenv("SECURITY_TOKEN"),
	}
	
	// Load providers
	cfg.Providers = loadProviders()
	
	// Load proxy settings
	cfg.ProxyEnabled = getEnvBool("NNTP_PROXY_ENABLED", false)
	cfg.ProxyPort = getEnvInt("NNTP_PROXY_PORT", 1119)
	cfg.ProxyHost = getEnv("NNTP_PROXY_HOST", "0.0.0.0")
	cfg.ProxyAuthUser = getEnv("NNTP_PROXY_AUTH_USER", "")
	cfg.ProxyAuthPass = getEnv("NNTP_PROXY_AUTH_PASS", "")
	
	// Validate required fields
	if cfg.NZBHydra2APIKey == "" {
		return nil, fmt.Errorf("NZBHYDRA2_API_KEY is required")
	}
	
	if len(cfg.Providers) == 0 {
		return nil, fmt.Errorf("at least one NNTP provider must be configured")
	}
	
	return cfg, nil
}

// loadProviders loads provider configurations from environment
func loadProviders() []Provider {
	var providers []Provider
	
	// Support up to 10 providers
	for i := 1; i <= 10; i++ {
		prefix := fmt.Sprintf("PROVIDER_%d_", i)
		
		host := os.Getenv(prefix + "HOST")
		if host == "" {
			continue // Provider not configured
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

// Helper functions
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
