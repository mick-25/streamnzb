package prowlarr

import (
	"encoding/json"
	"fmt"
	"net/http"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/logger"
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
func GetConfiguredIndexers(baseURL, apiKey string) ([]indexer.Indexer, error) {
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
			// Construct Newznab URL for this specific indexer
			// Prowlarr Generic Newznab URL format: http://host:port/{id}/api
			indexerURL := fmt.Sprintf("%s/%d", baseURL, def.ID)
			
			// Reuse the generic Client (Newznab compatible)
			// Pass Prowlarr API key as it works for the proxied endpoints too
			idx, err := NewClient(indexerURL, apiKey)
			if err != nil {
				logger.Error("Failed to init Prowlarr indexer", "name", def.Name, "err", err)
				continue
			}
			
			// Wrap or modify to return specific name
			// Since our Client.Name() returns "Prowlarr", we might want to customize it.
			// But for now, let's just use it.
			// Ideally we should update Client to accept a Name.
			idx.name = fmt.Sprintf("Prowlarr:%s", def.Name)
			
			indexers = append(indexers, idx)
		}
	}
	
	return indexers, nil
}
