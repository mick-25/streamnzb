package prowlarr

import (
	"encoding/json"
	"fmt"
	"net/http"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/logger"
	"strings"
	"time"
)

// IndexerDefinition represents a Prowlarr indexer configuration
type IndexerDefinition struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Enable   bool   `json:"enable"`
}

// GetConfiguredIndexers discovers and returns all active Usenet indexers from Prowlarr
func GetConfiguredIndexers(baseURL, apiKey string, um *indexer.UsageManager) ([]indexer.Indexer, error) {
	apiURL := fmt.Sprintf("%s/api/v1/indexer?apikey=%s", baseURL, apiKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch indexers from Prowlarr: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Prowlarr returned status %d", resp.StatusCode)
	}

	var definitions []IndexerDefinition
	if err := json.NewDecoder(resp.Body).Decode(&definitions); err != nil {
		return nil, fmt.Errorf("failed to decode Prowlarr indexers: %w", err)
	}

	var indexers []indexer.Indexer
	for _, def := range definitions {
		if def.Enable && def.Protocol == "usenet" {
			// Use base Prowlarr URL and indexer ID
			// The client will construct paths as /api/v1/indexer/{id}/newznab/api
			base := strings.TrimRight(baseURL, "/")

			name := fmt.Sprintf("Prowlarr:%s", def.Name)
			idx, err := NewClient(base, def.ID, apiKey, name, um)
			if err != nil {
				logger.Error("Failed to init Prowlarr indexer", "name", def.Name, "err", err)
				continue
			}

			indexers = append(indexers, idx)
		}
	}

	return indexers, nil
}
