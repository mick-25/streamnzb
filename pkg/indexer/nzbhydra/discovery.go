package nzbhydra

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/core/logger"
	"time"
)

// normalizeHostForAvailNZB returns hostname suitable for AvailNZB (lowercase, no api. prefix, no port).
func normalizeHostForAvailNZB(host string) string {
	h := strings.TrimSpace(host)
	if h == "" {
		return ""
	}
	// Strip scheme if present (e.g. "https://api.nzbgeek.info")
	if strings.HasPrefix(h, "http://") || strings.HasPrefix(h, "https://") {
		if u, err := url.Parse(h); err == nil && u.Host != "" {
			h = u.Host
		}
	}
	h = strings.ToLower(h)
	h = strings.TrimPrefix(h, "api.")
	// Remove port if present
	if idx := strings.Index(h, ":"); idx != -1 {
		h = h[:idx]
	}
	return strings.TrimSpace(h)
}

// GetConfiguredIndexers discovers active indexers from NZBHydra2 via a minimal search,
// extracts indexer names + hosts from hydraIndexerName/hydraIndexerHost attributes,
// and returns a single aggregated client that searches all indexers in one call (no
// indexers= filter). This is faster than per-indexer clients. Stream results still
// show the underlying indexer via hydraIndexerName. displayName is used for the client
// (e.g. "NZBHydra2" or config name).
func GetConfiguredIndexers(baseURL, apiKey, displayName string, um *indexer.UsageManager) ([]indexer.Indexer, []string, error) {
	baseURL = strings.TrimRight(baseURL, "/")

	params := url.Values{}
	params.Set("apikey", apiKey)
	params.Set("t", "search")
	params.Set("q", "sample")
	params.Set("limit", "10") // We only need one result per indexer
	params.Set("o", "xml")

	apiURL := fmt.Sprintf("%s/api?%s", baseURL, params.Encode())
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		return nil, nil, fmt.Errorf("NZBHydra2 discovery search failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, nil, fmt.Errorf("NZBHydra2 search returned %d: %s", resp.StatusCode, string(body))
	}

	var result indexer.SearchResponse
	if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, nil, fmt.Errorf("failed to parse NZBHydra2 response: %w", err)
	}

	// Collect unique (name, host) from hydraIndexerName and hydraIndexerHost attributes
	type nameHost struct {
		name string
		host string
	}
	seen := make(map[string]nameHost)

	for _, item := range result.Channel.Items {
		name := item.GetAttribute("hydraIndexerName")
		if name == "" {
			name = item.GetAttribute("indexer")
		}
		if name == "" {
			continue
		}
		name = strings.TrimSpace(name)
		host := strings.TrimSpace(item.GetAttribute("hydraIndexerHost"))
		if existing, ok := seen[name]; ok {
			if host != "" && existing.host == "" {
				seen[name] = nameHost{name: name, host: host}
			}
			continue
		}
		seen[name] = nameHost{name: name, host: host}
	}

	if len(seen) == 0 {
		return nil, nil, fmt.Errorf("NZBHydra2 search returned no indexer attributes (hydraIndexerName); check version")
	}

	if displayName == "" {
		displayName = "NZBHydra2"
	}

	// Single aggregated client: no indexers= filter, so Hydra queries all indexers in one call
	idx, err := NewClient(baseURL, apiKey, displayName, um)
	if err != nil {
		return nil, nil, fmt.Errorf("could not initialize NZBHydra2 client: %w", err)
	}

	indexerNames := make([]string, 0, len(seen))
	hostnames := make([]string, 0, len(seen))
	seenHost := make(map[string]bool)
	for _, nh := range seen {
		indexerNames = append(indexerNames, nh.name)
		if h := normalizeHostForAvailNZB(nh.host); h != "" && !seenHost[h] {
			seenHost[h] = true
			hostnames = append(hostnames, h)
		}
	}

	logger.Info("Initialized NZBHydra2 (aggregated search)", "indexers", indexerNames, "hosts", hostnames)
	logger.Debug("NZBHydra2 indexers", "indexers", indexerNames, "hosts", hostnames, "count", len(indexerNames))
	return []indexer.Indexer{idx}, hostnames, nil
}
