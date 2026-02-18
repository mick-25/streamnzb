package stremio

import (
	"encoding/json"
	"strings"
)

// ManifestBehaviorHints controls Stremio addon UI (e.g. configure button)
type ManifestBehaviorHints struct {
	Configurable          bool `json:"configurable,omitempty"`
	ConfigurationRequired bool `json:"configurationRequired,omitempty"`
}

// Manifest represents the Stremio addon manifest
type Manifest struct {
	ID            string                 `json:"id"`
	Version       string                 `json:"version"`
	Name          string                 `json:"name"`
	Description   string                 `json:"description"`
	Resources     []string               `json:"resources"`
	Types         []string               `json:"types"`
	Catalogs      []Catalog              `json:"catalogs"`
	IDPrefixes    []string               `json:"idPrefixes,omitempty"`
	Background    string                 `json:"background,omitempty"`
	Logo          string                 `json:"logo,omitempty"`
	BehaviorHints *ManifestBehaviorHints `json:"behaviorHints,omitempty"`
}

// Catalog represents a content catalog
type Catalog struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// manifestVersion converts version to Stremio-compatible semver.
// Stremio requires semver (e.g. 1.0.0); dev builds like "dev-abc1234" become "0.0.0-dev.abc1234".
func manifestVersion(version string) string {
	if version == "" {
		version = "dev"
	}
	// If it already looks like semver (starts with digit), use as-is
	if len(version) > 0 && version[0] >= '0' && version[0] <= '9' {
		return version
	}
	// Convert dev builds to valid semver prerelease format
	return "0.0.0-" + strings.ReplaceAll(version, "-", ".")
}

// NewManifest creates the addon manifest
func NewManifest(version string) *Manifest {
	if version == "" {
		version = "dev"
	}
	return &Manifest{
		ID:          "community.streamnzb",
		Version:     manifestVersion(version),
		Name:        "StreamNZB",
		Description: "Stream content directly from Usenet",
		Resources:   []string{"stream"},
		Types:       []string{"movie", "series"},
		Catalogs:    []Catalog{},
		IDPrefixes:  []string{"tt", "tmdb"},
		Logo:        "https://cdn.discordapp.com/icons/1470288400157380710/6f397b4a2e9561dc7ad43526588cfd67.png",
	}
}

// ToJSONForDevice returns manifest JSON with behaviorHints set for the given device.
// Configurable is true only for admin users (shows configure button in Stremio).
func (m *Manifest) ToJSONForDevice(isAdmin bool) ([]byte, error) {
	// Copy base manifest
	out := *m
	out.BehaviorHints = &ManifestBehaviorHints{
		Configurable:          isAdmin,
		ConfigurationRequired: false,
	}
	return json.MarshalIndent(out, "", "  ")
}
