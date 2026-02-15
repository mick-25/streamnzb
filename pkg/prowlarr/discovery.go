package prowlarr

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"streamnzb/pkg/indexer"
	"streamnzb/pkg/logger"
)

// IndexerDefinition represents a Prowlarr indexer configuration (API response)
type IndexerDefinition struct {
	ID          int      `json:"id"`
	Name        string   `json:"name"`
	Protocol    string   `json:"protocol"`
	Enable      bool     `json:"enable"`
	IndexerUrls []string `json:"indexerUrls,omitempty"` // Base URLs for this indexer (e.g. https://api.nzbgeek.info)
}

// hostFromIndexerURL returns hostname for AvailNZB (lowercase, no api. prefix, no port).
func hostFromIndexerURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	h := strings.ToLower(strings.TrimSpace(u.Hostname()))
	h = strings.TrimPrefix(h, "api.")
	return h
}

// GetConfiguredIndexers discovers and returns all active Usenet indexers from Prowlarr.
// Also returns underlying indexer hostnames for AvailNZB (from IndexerUrls) so GetReleases
// can filter by the correct indexers (e.g. nzbgeek.info, drunkenslug.com).
func GetConfiguredIndexers(baseURL, apiKey string, um *indexer.UsageManager) ([]indexer.Indexer, []string, error) {
	apiURL := fmt.Sprintf("%s/api/v1/indexer?apikey=%s", baseURL, apiKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch indexers from Prowlarr: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("Prowlarr returned status %d", resp.StatusCode)
	}

	var definitions []IndexerDefinition
	if err := json.NewDecoder(resp.Body).Decode(&definitions); err != nil {
		return nil, nil, fmt.Errorf("failed to decode Prowlarr indexers: %w", err)
	}

	var indexers []indexer.Indexer
	var hostnames []string
	seenHost := make(map[string]bool)

	for _, def := range definitions {
		if def.Enable && def.Protocol == "usenet" {
			for _, u := range def.IndexerUrls {
				if h := hostFromIndexerURL(u); h != "" && !seenHost[h] {
					seenHost[h] = true
					hostnames = append(hostnames, h)
				}
			}
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

	return indexers, hostnames, nil
}
