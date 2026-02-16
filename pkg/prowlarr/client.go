package prowlarr

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/logger"
	"strings"
	"sync"
	"time"
)

// Client represents a Prowlarr Newznab API client
type Client struct {
	baseURL   string
	indexerID int
	apiKey    string
	name      string
	client    *http.Client

	// Usage tracking
	apiLimit          int
	apiUsed           int
	apiRemaining      int
	downloadLimit     int
	downloadUsed      int
	downloadRemaining int
	usageManager      *indexer.UsageManager
	mu                sync.RWMutex
}

// Ensure Client implements indexer.Indexer and indexer.IndexerWithResolve at compile time.
var _ indexer.Indexer = (*Client)(nil)
var _ indexer.IndexerWithResolve = (*Client)(nil)

// checkAPILimit returns error if API limit is reached
func (c *Client) checkAPILimit() error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.apiLimit > 0 && c.apiRemaining <= 0 {
		return fmt.Errorf("API limit reached for %s", c.Name())
	}
	return nil
}

// checkDownloadLimit returns error if download limit is reached
func (c *Client) checkDownloadLimit() error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.downloadLimit > 0 && c.downloadRemaining <= 0 {
		return fmt.Errorf("download limit reached for %s", c.Name())
	}
	return nil
}

// Name returns the name of this indexer
func (c *Client) Name() string {
	if c.name != "" {
		return c.name
	}
	return "Prowlarr"
}

// GetUsage returns the current usage stats
func (c *Client) GetUsage() indexer.Usage {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return indexer.Usage{
		APIHitsLimit:       c.apiLimit,
		APIHitsUsed:        c.apiUsed,
		APIHitsRemaining:   c.apiRemaining,
		DownloadsLimit:     c.downloadLimit,
		DownloadsUsed:      c.downloadUsed,
		DownloadsRemaining: c.downloadRemaining,
	}
}

// NewClient creates a new Prowlarr client.
func NewClient(baseURL string, indexerID int, apiKey, name string, um *indexer.UsageManager) (*Client, error) {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		MaxConnsPerHost:     100,
		IdleConnTimeout:     90 * time.Second,
	}

	c := &Client{
		baseURL:      strings.TrimRight(baseURL, "/"),
		indexerID:    indexerID,
		apiKey:       apiKey,
		name:         name,
		usageManager: um,
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
	}

	// Load initial usage if manager is provided
	if um != nil && name != "" {
		usage := um.GetIndexerUsage(name)
		c.apiUsed = usage.APIHitsUsed
		c.downloadUsed = usage.DownloadsUsed
	}

	// Skip ping during initialization - the newznab endpoint may return 404 during ping
	// but work fine for actual searches. We'll get proper errors during searches if there's an issue.
	// if err := c.Ping(); err != nil {
	// 	return nil, err
	// }

	return c, nil
}

// Ping checks if the Prowlarr server is reachable
func (c *Client) Ping() error {
	// Try the search endpoint with a minimal query instead of caps
	// Some Prowlarr setups don't expose caps endpoint properly
	apiURL := fmt.Sprintf("%s/%d/api?t=search&limit=1&apikey=%s", c.baseURL, c.indexerID, c.apiKey)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.apiKey)

	logger.Debug("Prowlarr Ping URL", "url", apiURL)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Prowlarr returned error status: %d", resp.StatusCode)
	}
	return nil
}

// Search queries Prowlarr for content
func (c *Client) Search(req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	if err := c.checkAPILimit(); err != nil {
		return nil, err
	}

	// Build Newznab API URL
	params := url.Values{}
	params.Set("apikey", c.apiKey)
	params.Set("o", "xml")

	// Use appropriate search type based on category
	if req.Cat == "2000" {
		params.Set("t", "movie")
	} else if req.Cat == "5000" {
		params.Set("t", "tvsearch")
	} else {
		params.Set("t", "search")
	}

	if req.Query != "" {
		params.Set("q", req.Query)
	}
	if req.IMDbID != "" {
		imdbID := strings.TrimPrefix(req.IMDbID, "tt")
		params.Set("imdbid", imdbID)
	}
	if req.TMDBID != "" {
		params.Set("tmdbid", req.TMDBID)
	}
	if req.TVDBID != "" {
		params.Set("tvdbid", req.TVDBID)
	}
	if req.Cat != "" {
		params.Set("cat", req.Cat)
	}
	if req.Limit > 0 {
		params.Set("limit", fmt.Sprintf("%d", req.Limit))
	} else {
		params.Set("limit", "10")
	}

	if req.Season != "" {
		params.Set("season", req.Season)
	}
	if req.Episode != "" {
		params.Set("ep", req.Episode)
	}

	// Use Prowlarr's newznab proxy endpoint
	// Format: http://prowlarr:port/{indexerid}/api
	apiURL := fmt.Sprintf("%s/%d/api?%s", c.baseURL, c.indexerID, params.Encode())

	// Debug: Log the actual API URL being called
	logger.Debug("Prowlarr Search URL", "url", apiURL)

	reqBod, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	reqBod.Header.Set("X-Api-Key", c.apiKey)

	resp, err := c.client.Do(reqBod)
	if err != nil {
		return nil, fmt.Errorf("failed to query Prowlarr: %w", err)
	}
	defer resp.Body.Close()

	// Local increment
	c.mu.Lock()
	c.apiUsed++
	if c.apiRemaining > 0 {
		c.apiRemaining--
	}
	c.mu.Unlock()

	c.updateUsageFromHeaders(resp.Header)

	// Read body first to debug
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read Prowlarr response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Prowlarr returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	if len(bodyBytes) == 0 {
		return nil, fmt.Errorf("Prowlarr returned empty body")
	}

	var result indexer.SearchResponse
	if err := xml.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("failed to parse Prowlarr response: %w", err)
	}

	// Populate SourceIndexer for each item
	for i := range result.Channel.Items {
		result.Channel.Items[i].SourceIndexer = c
	}

	return &result, nil
}

// ResolveDownloadURL finds the same release via Prowlarr search and returns the result's Link
// (proxy URL) so DownloadNZB works when the original URL is a direct indexer URL from AvailNZB.
func (c *Client) ResolveDownloadURL(ctx context.Context, directURL, title string, size int64) (string, error) {
	if title == "" {
		return "", fmt.Errorf("title required to resolve download URL")
	}
	req := indexer.SearchRequest{Query: title, Limit: 30}
	resp, err := c.Search(req)
	if err != nil {
		return "", fmt.Errorf("search for resolve: %w", err)
	}
	if resp == nil || len(resp.Channel.Items) == 0 {
		return "", fmt.Errorf("no search results for title")
	}
	normTitle := strings.ToLower(strings.TrimSpace(title))
	for _, item := range resp.Channel.Items {
		itemNorm := strings.ToLower(strings.TrimSpace(item.Title))
		if itemNorm != normTitle {
			continue
		}
		if size > 0 && item.Size > 0 && item.Size != size {
			continue
		}
		if item.Link == "" {
			continue
		}
		return item.Link, nil
	}
	return "", fmt.Errorf("no matching release for title in Prowlarr results")
}

// fileFromNZBURL derives a safe filename for Prowlarr's file= parameter from an NZB URL
// (e.g. id from ?t=get&id=... or "download" as fallback).
func fileFromNZBURL(nzbURL string) string {
	parsed, err := url.Parse(nzbURL)
	if err != nil {
		return "download"
	}
	if id := parsed.Query().Get("id"); id != "" {
		// Keep only safe chars for filename
		var b strings.Builder
		for _, r := range id {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
				b.WriteRune(r)
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	return "download"
}

// DownloadNZB downloads an NZB file by URL.
// When the URL is a direct indexer link (e.g. api.nzbgeek.info), we rewrite to Prowlarr's
// download endpoint GET /{id}/download?link={encodedUrl} so Prowlarr can add the indexer's API key.
func (c *Client) DownloadNZB(nzbURL string) ([]byte, error) {
	if err := c.checkDownloadLimit(); err != nil {
		logger.Warn("Download limit reached for %s", "indexer", c.Name())
		return nil, err
	}

	// Rewrite direct indexer URL to Prowlarr download endpoint: /{indexerId}/download?link=...&file=...
	// Prowlarr requires both link and file; link must be Base64-encoded. Note: Prowlarr's "normalize" step
	// only accepts links it generated (proxy links). If search returns direct indexer URLs, Prowlarr will
	// respond with "Failed to normalize provided link". Users should enable Redirect so search returns Prowlarr proxy URLs.
	parsed, err := url.Parse(nzbURL)
	if err == nil {
		baseParsed, _ := url.Parse(c.baseURL)
		if baseParsed != nil && parsed.Host != "" && baseParsed.Host != "" && parsed.Host != baseParsed.Host {
			params := url.Values{}
			params.Set("link", base64.StdEncoding.EncodeToString([]byte(nzbURL)))
			params.Set("file", fileFromNZBURL(nzbURL))
			params.Set("apikey", c.apiKey)
			downloadURL := fmt.Sprintf("%s/%d/download?%s", c.baseURL, c.indexerID, params.Encode())
			nzbURL = downloadURL
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", nzbURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("X-Api-Key", c.apiKey)

	logger.Debug("Prowlarr Download URL", "url", nzbURL)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download NZB: %w", err)
	}
	defer resp.Body.Close()

	// Local increment
	c.mu.Lock()
	c.apiUsed++ // Download also counts as hit
	c.downloadUsed++
	if c.apiRemaining > 0 {
		c.apiRemaining--
	}
	if c.downloadRemaining > 0 {
		c.downloadRemaining--
	}
	c.mu.Unlock()

	c.updateUsageFromHeaders(resp.Header)

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read NZB data: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		bodyStr := string(data)
		if strings.Contains(bodyStr, "Failed to normalize provided link") {
			return nil, fmt.Errorf("Prowlarr rejected the download link: it only accepts links it generated (proxy links), not direct indexer URLs. Ensure Prowlarr returns proxy/redirect URLs in search (e.g. enable Redirect for the app or indexer) so the stored link is a Prowlarr download URL")
		}
		if len(bodyStr) > 200 {
			bodyStr = bodyStr[:200] + "..."
		}
		return nil, fmt.Errorf("NZB download returned status %d: %s", resp.StatusCode, bodyStr)
	}

	return data, nil
}

func (c *Client) updateUsageFromHeaders(h http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Newznab Standard Headers often proximal through Prowlarr
	if val := h.Get("X-RateLimit-Daily-Limit"); val != "" {
		if limit, err := strconv.Atoi(val); err == nil {
			c.apiLimit = limit
		}
	}
	if val := h.Get("X-RateLimit-Daily-Remaining"); val != "" {
		if remaining, err := strconv.Atoi(val); err == nil {
			c.apiRemaining = remaining
		}
	}
	if val := h.Get("X-DNZBLimit-Daily-Limit"); val != "" {
		if limit, err := strconv.Atoi(val); err == nil {
			c.downloadLimit = limit
		}
	}
	if val := h.Get("X-DNZBLimit-Daily-Remaining"); val != "" {
		if remaining, err := strconv.Atoi(val); err == nil {
			c.downloadRemaining = remaining
		}
	}

	// Update persistent storage
	if c.usageManager != nil {
		if c.apiLimit > 0 {
			c.apiUsed = c.apiLimit - c.apiRemaining
		}
		if c.downloadLimit > 0 {
			c.downloadUsed = c.downloadLimit - c.downloadRemaining
		}
		c.usageManager.UpdateUsage(c.name, c.apiUsed, c.downloadUsed)
	}
}
