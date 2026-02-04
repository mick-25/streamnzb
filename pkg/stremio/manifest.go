package stremio

import (
	"encoding/json"
)

// Manifest represents the Stremio addon manifest
type Manifest struct {
	ID          string       `json:"id"`
	Version     string       `json:"version"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Resources   []string     `json:"resources"`
	Types       []string     `json:"types"`
	Catalogs    []Catalog    `json:"catalogs"`
	IDPrefixes  []string     `json:"idPrefixes,omitempty"`
	Background  string       `json:"background,omitempty"`
	Logo        string       `json:"logo,omitempty"`
}

// Catalog represents a content catalog
type Catalog struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// NewManifest creates the addon manifest
func NewManifest() *Manifest {
	return &Manifest{
		ID:          "community.streamnzb",
		Version:     "0.1.0",
		Name:        "StreamNZB",
		Description: "Stream content directly from Usenet via NZBHydra2",
		Resources:   []string{"stream"},
		Types:       []string{"movie", "series"},
		Catalogs:    []Catalog{},
		IDPrefixes:  []string{"tt", "tmdb"},
		Background:  "https://via.placeholder.com/1280x720/1a1a2e/16213e?text=StreamNZB",
		Logo:        "https://via.placeholder.com/256x256/0f3460/16213e?text=NZB",
	}
}

// ToJSON converts manifest to JSON
func (m *Manifest) ToJSON() ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}
