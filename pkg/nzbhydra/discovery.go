package nzbhydra

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/logger"
	"time"
)

// normalizeHostForAvailNZB returns hostname suitable for AvailNZB indexers param (lowercase, no api. prefix).
func normalizeHostForAvailNZB(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	h = strings.TrimPrefix(h, "api.")
	// Remove port if present
	if idx := strings.Index(h, ":"); idx != -1 {
		h = h[:idx]
	}
	return h
}

// IndexerDefinition represents an NZBHydra2 indexer configuration
type IndexerDefinition struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	State       interface{} `json:"state"` // Could be string "ENABLED"/"DISABLED" or boolean true/false
	Enabled     *bool  `json:"enabled"`   // Alternative boolean field
	Config      struct {
		Host string `json:"host"`
		Path string `json:"path"`
	} `json:"config"`
	IndexerType      string `json:"indexerType"`      // "NEWZNAB", "TORZNAB", etc.
	Type             string `json:"type"`              // Alternative field name
	IndexerCategory  string `json:"indexerCategory"`  // Alternative field name
	IndexerCategoryType string `json:"indexerCategoryType"` // Alternative field name
}

// BaseConfigResponse represents the response from /internalapi/config
// We only need the indexers field
type BaseConfigResponse struct {
	Indexers []IndexerDefinition `json:"indexers"`
}

// endpointExists checks if an endpoint exists by making a HEAD request
func endpointExists(client *http.Client, apiURL, apiKey string) bool {
	req, err := http.NewRequest("HEAD", apiURL, nil)
	if err != nil {
		return false
	}

	req.Header.Set("X-Api-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	// Endpoint exists if it's not 404 (could be 200, 401, 403, etc.)
	return resp.StatusCode != http.StatusNotFound
}

// GetConfiguredIndexers discovers and returns all active Usenet indexers from NZBHydra2.
// Also returns underlying indexer hostnames for AvailNZB (Config.Host per indexer) so
// GetReleases can filter by the correct indexers (e.g. nzbgeek.info, drunkenslug.com).
func GetConfiguredIndexers(baseURL, apiKey string, um *indexer.UsageManager) ([]indexer.Indexer, []string, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	apiURL := fmt.Sprintf("%s/internalapi/config", baseURL)
	if !endpointExists(client, apiURL, apiKey) {
		return nil, nil, fmt.Errorf("NZBHydra2 config endpoint not available")
	}

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("X-Api-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch config: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, nil, fmt.Errorf("NZBHydra2 config endpoint returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read config response: %w", err)
	}

	var configResponse BaseConfigResponse
	if err := json.Unmarshal(bodyBytes, &configResponse); err != nil {
		return nil, nil, fmt.Errorf("failed to decode NZBHydra2 config: %w", err)
	}

	definitions := configResponse.Indexers
	var indexers []indexer.Indexer
	var hostnames []string
	seenHost := make(map[string]bool)

	for _, def := range definitions {
		isEnabled := false
		if def.Enabled != nil {
			isEnabled = *def.Enabled
		} else if def.State != nil {
			switch v := def.State.(type) {
			case bool:
				isEnabled = v
			case string:
				stateUpper := strings.ToUpper(v)
				isEnabled = stateUpper == "ENABLED" || stateUpper == "TRUE"
			}
		}

		indexerType := def.IndexerType
		if indexerType == "" {
			indexerType = def.Type
		}
		if indexerType == "" {
			indexerType = def.IndexerCategory
		}
		if indexerType == "" {
			indexerType = def.IndexerCategoryType
		}
		typeUpper := strings.ToUpper(indexerType)
		isNewznab := indexerType == "" ||
			typeUpper == "NEWZNAB" ||
			typeUpper == "NEWZNABINDEXER" ||
			strings.Contains(typeUpper, "NEWZNAB")

		if isEnabled && isNewznab {
			if h := normalizeHostForAvailNZB(def.Config.Host); h != "" && !seenHost[h] {
				seenHost[h] = true
				hostnames = append(hostnames, h)
			}
			name := fmt.Sprintf("NZBHydra2:%s", def.Name)
			idx, err := NewClientWithIndexer(baseURL, apiKey, name, def.Name, um)
			if err != nil {
				logger.Error("Failed to init NZBHydra2 indexer", "name", def.Name, "err", err)
				continue
			}
			indexers = append(indexers, idx)
			logger.Info("Initialized NZBHydra2 indexer", "name", def.Name)
		}
	}

	if len(indexers) == 0 {
		return nil, nil, fmt.Errorf("no enabled Newznab indexers found in NZBHydra2 (found %d total indexers)", len(definitions))
	}

	return indexers, hostnames, nil
}
