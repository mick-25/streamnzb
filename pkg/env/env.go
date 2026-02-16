// Package env consolidates all environment variable reading for the application.
// Config overrides are applied only at startup (see config.Load).
package env

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Environment variable names (single source of truth)
const (
	NZBHYDRA2URL            = "NZBHYDRA2_URL"
	NZBHYDRA2APIKey         = "NZBHYDRA2_API_KEY"
	PROWLARRURL             = "PROWLARR_URL"
	PROWLARRAPIKey          = "PROWLARR_API_KEY"
	ADDONPort                = "ADDON_PORT"
	ADDONBaseURL             = "ADDON_BASE_URL"
	LOGLevel                 = "LOG_LEVEL"
	CacheTTLSeconds          = "CACHE_TTL_SECONDS"
	ValidationSampleSize     = "VALIDATION_SAMPLE_SIZE"
	AvailNZBURL              = "AVAILNZB_URL"
	AvailNZBAPIKey           = "AVAILNZB_API_KEY"
	TMDBAPIKey               = "TMDB_API_KEY"
	TVDBAPIKey               = "TVDB_API_KEY"
	NNTPProxyEnabled         = "NNTP_PROXY_ENABLED"
	NNTPProxyPort            = "NNTP_PROXY_PORT"
	NNTPProxyHost            = "NNTP_PROXY_HOST"
	NNTPProxyAuthUser        = "NNTP_PROXY_AUTH_USER"
	NNTPProxyAuthPass        = "NNTP_PROXY_AUTH_PASS"
	TZVar                    = "TZ"
	ProviderPrefix           = "PROVIDER_"
	IndexerPrefix            = "INDEXER_"
)

// Config JSON keys returned by OverrideKeys (for UI warnings)
const (
	KeyNZBHydraURL    = "nzbhydra_url"
	KeyNZBHydraAPIKey = "nzbhydra_api_key"
	KeyProwlarrURL    = "prowlarr_url"
	KeyProwlarrAPIKey = "prowlarr_api_key"
	KeyAddonPort      = "addon_port"
	KeyAddonBaseURL   = "addon_base_url"
	KeyLogLevel       = "log_level"
	KeyCacheTTL       = "cache_ttl_seconds"
	KeyValidationSize = "validation_sample_size"
	KeyProxyEnabled   = "proxy_enabled"
	KeyProxyPort      = "proxy_port"
	KeyProxyHost      = "proxy_host"
	KeyProxyAuthUser  = "proxy_auth_user"
	KeyProxyAuthPass  = "proxy_auth_pass"
	KeyProviders      = "providers"
	KeyIndexers       = "indexers"
	KeyAvailNZBURL    = "availnzb_url"
	KeyAvailNZBAPIKey = "availnzb_api_key"
	KeyTMDBAPIKey     = "tmdb_api_key"
	KeyTVDBAPIKey     = "tvdb_api_key"
	KeyAdminUsername  = "admin_username"
)

const AdminUsernameEnv = "ADMIN_USERNAME"

// TZ returns the TZ environment variable (e.g. for logger timezone).
func TZ() string {
	return os.Getenv(TZVar)
}

// LogLevel returns LOG_LEVEL with default "INFO" (for early logger init before config).
func LogLevel() string {
	if v := os.Getenv(LOGLevel); v != "" {
		return v
	}
	return "INFO"
}

// Provider and Indexer mirror config types so this package does not depend on config.
type Provider struct {
	Name        string
	Host        string
	Port        int
	Username    string
	Password    string
	Connections int
	UseSSL      bool
	Priority    *int
	Enabled     *bool
}

type Indexer struct {
	Name   string
	URL    string
	APIKey string
}

// ConfigOverrides holds all config values that can be set via environment variables.
// Used at startup by config.Load to apply overrides.
type ConfigOverrides struct {
	NZBHydra2URL            string
	NZBHydra2APIKey         string
	ProwlarrURL            string
	ProwlarrAPIKey         string
	AddonPort               int
	AddonBaseURL            string
	LogLevel                string
	CacheTTLSeconds        int
	ValidationSampleSize    int
	AvailNZBURL             string
	AvailNZBAPIKey          string
	TMDBAPIKey              string
	TVDBAPIKey              string
	ProxyEnabled            bool
	ProxyPort               int
	ProxyHost               string
	ProxyAuthUser           string
	ProxyAuthPass           string
	AdminUsername           string
	Providers               []Provider
	Indexers                []Indexer
}

// ReadConfigOverrides reads all relevant environment variables once and returns
// overrides to apply to config plus the list of config JSON keys that were set
// (for UI "overwritten on restart" warnings).
func ReadConfigOverrides() (ConfigOverrides, []string) {
	var o ConfigOverrides
	var keys []string

	if v := os.Getenv(NZBHYDRA2URL); v != "" {
		o.NZBHydra2URL = v
		keys = append(keys, KeyNZBHydraURL)
	}
	if v := os.Getenv(NZBHYDRA2APIKey); v != "" {
		o.NZBHydra2APIKey = v
		keys = append(keys, KeyNZBHydraAPIKey)
	}
	if v := os.Getenv(PROWLARRURL); v != "" {
		o.ProwlarrURL = v
		keys = append(keys, KeyProwlarrURL)
	}
	if v := os.Getenv(PROWLARRAPIKey); v != "" {
		o.ProwlarrAPIKey = v
		keys = append(keys, KeyProwlarrAPIKey)
	}
	if v := os.Getenv(ADDONPort); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			o.AddonPort = port
			keys = append(keys, KeyAddonPort)
		}
	}
	if v := os.Getenv(ADDONBaseURL); v != "" {
		o.AddonBaseURL = v
		keys = append(keys, KeyAddonBaseURL)
	}
	if v := os.Getenv(LOGLevel); v != "" {
		o.LogLevel = v
		keys = append(keys, KeyLogLevel)
	}
	if v := os.Getenv(CacheTTLSeconds); v != "" {
		if ttl, err := strconv.Atoi(v); err == nil {
			o.CacheTTLSeconds = ttl
			keys = append(keys, KeyCacheTTL)
		}
	}
	if v := os.Getenv(ValidationSampleSize); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			o.ValidationSampleSize = n
			keys = append(keys, KeyValidationSize)
		}
	}
	// Note: AvailNZBURL, AvailNZBAPIKey, TMDBAPIKey, TVDBAPIKey are not read from env vars.
	// They are build-time constants set via ldflags and should never be modifiable at runtime.
	if v := os.Getenv(NNTPProxyEnabled); v != "" {
		o.ProxyEnabled = v == "true" || v == "1"
		keys = append(keys, KeyProxyEnabled)
	}
	if v := os.Getenv(NNTPProxyPort); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			o.ProxyPort = port
			keys = append(keys, KeyProxyPort)
		}
	}
	if v := os.Getenv(NNTPProxyHost); v != "" {
		o.ProxyHost = v
		keys = append(keys, KeyProxyHost)
	}
	if v := os.Getenv(NNTPProxyAuthUser); v != "" {
		o.ProxyAuthUser = v
		keys = append(keys, KeyProxyAuthUser)
	}
	if v := os.Getenv(NNTPProxyAuthPass); v != "" {
		o.ProxyAuthPass = v
		keys = append(keys, KeyProxyAuthPass)
	}
	if v := os.Getenv(AdminUsernameEnv); v != "" {
		o.AdminUsername = v
		keys = append(keys, KeyAdminUsername)
	}

	o.Providers = readProvidersFromEnv()
	if len(o.Providers) > 0 {
		keys = append(keys, KeyProviders)
	}
	o.Indexers = readIndexersFromEnv()
	if len(o.Indexers) > 0 {
		keys = append(keys, KeyIndexers)
	}

	return o, keys
}

// OverrideKeys returns the config JSON keys that have environment overrides set.
// Used by the API to tell the UI which settings show "overwritten on restart" warnings.
func OverrideKeys() []string {
	_, keys := ReadConfigOverrides()
	return keys
}

func readProvidersFromEnv() []Provider {
	var list []Provider
	for i := 1; i <= 10; i++ {
		prefix := fmt.Sprintf("%s%d_", ProviderPrefix, i)
		host := os.Getenv(prefix + "HOST")
		if host == "" {
			continue
		}
		priority := getEnvInt(prefix+"PRIORITY", i) // Default priority matches provider number
		enabled := getEnvBool(prefix+"ENABLED", true) // Default to enabled
		list = append(list, Provider{
			Name:        getEnv(prefix+"NAME", fmt.Sprintf("Provider %d", i)),
			Host:        host,
			Port:        getEnvInt(prefix+"PORT", 563),
			Username:    os.Getenv(prefix + "USERNAME"),
			Password:    os.Getenv(prefix + "PASSWORD"),
			Connections: getEnvInt(prefix+"CONNECTIONS", 10),
			UseSSL:      getEnvBool(prefix+"SSL", true),
			Priority:    &priority,
			Enabled:     &enabled,
		})
	}
	return list
}

func readIndexersFromEnv() []Indexer {
	var list []Indexer
	for i := 1; i <= 10; i++ {
		prefix := fmt.Sprintf("%s%d_", IndexerPrefix, i)
		url := os.Getenv(prefix + "URL")
		if url == "" {
			continue
		}
		list = append(list, Indexer{
			Name:   getEnv(prefix+"NAME", fmt.Sprintf("Indexer %d", i)),
			URL:    url,
			APIKey: os.Getenv(prefix + "API_KEY"),
		})
	}
	return list
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	if v := os.Getenv(key); v != "" {
		return strings.ToLower(v) == "true" || v == "1"
	}
	return defaultVal
}
