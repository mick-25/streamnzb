package nzbhydra

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"streamnzb/pkg/core/env"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"strings"
	"sync"
	"time"
)

// Client represents an NZBHydra2 API client
type Client struct {
	baseURL     string
	apiKey      string
	name        string
	indexerName string // Specific indexer name to query (empty = query all)
	client      *http.Client

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

// Ensure Client implements indexer.Indexer at compile time.
var _ indexer.Indexer = (*Client)(nil)

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

// APIError represents a Newznab API error
type APIError struct {
	XMLName     xml.Name `xml:"error"`
	Code        string   `xml:"code,attr"`
	Description string   `xml:"description,attr"`
}

// Ping checks if the NZBHydra2 server is reachable and the API key is valid
func (c *Client) Ping() error {
	apiURL := fmt.Sprintf("%s/api?t=caps&apikey=%s", c.baseURL, c.apiKey)
	req, err := http.NewRequestWithContext(context.Background(), "GET", apiURL, nil)
	if err != nil {
		return err
	}
	if ua := env.IndexerQueryHeader(); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("NZBHydra2 error: invalid API key")
	}

	body, _ := io.ReadAll(resp.Body)

	// Check if the response is an XML error
	var apiErr APIError
	if err := xml.Unmarshal(body, &apiErr); err == nil && apiErr.Description != "" {
		return fmt.Errorf("NZBHydra2 error: %s", apiErr.Description)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("NZBHydra2 returned error status: %d", resp.StatusCode)
	}
	return nil
}

// NewClient creates a new NZBHydra2 client and verifies connectivity
// This creates an aggregated client that queries all indexers
func NewClient(baseURL, apiKey, name string, um *indexer.UsageManager) (*Client, error) {
	return NewClientWithIndexer(baseURL, apiKey, name, "", um)
}

// NewClientWithIndexer creates a new NZBHydra2 client for a specific indexer
// If indexerName is empty, it queries all indexers (aggregated mode)
func NewClientWithIndexer(baseURL, apiKey, name, indexerName string, um *indexer.UsageManager) (*Client, error) {
	// Create HTTP client with TLS skip verify for self-signed certs
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100, // Allow high parallelism for NZB downloads
		MaxConnsPerHost:     100,
		IdleConnTimeout:     90 * time.Second,
	}

	c := &Client{
		baseURL:     baseURL,
		apiKey:      apiKey,
		name:        name,
		indexerName: indexerName,
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

	if err := c.Ping(); err != nil {
		return nil, err
	}

	return c, nil
}

// Name returns the name of this indexer
func (c *Client) Name() string {
	if c.name != "" {
		return c.name
	}
	return "NZBHydra2"
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

// Search queries NZBHydra2 for content
func (c *Client) Search(req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	if err := c.checkAPILimit(); err != nil {
		return nil, err
	}

	// Build Newznab API URL
	params := url.Values{}
	params.Set("apikey", c.apiKey)
	params.Set("o", "xml")

	// Use appropriate search type based on category
	// 2000 = Movies -> use t=movie
	// 5000 = TV -> use t=tvsearch
	// Default = generic search
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
		// Newznab API expects IMDb ID without 'tt' prefix
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
		params.Set("limit", "10") // Default limit
	}

	// Add season/episode for TV searches
	if req.Season != "" {
		params.Set("season", req.Season)
	}
	if req.Episode != "" {
		params.Set("ep", req.Episode)
	}

	// If this client is for a specific indexer, add the indexers parameter
	// This allows querying individual indexers through NZBHydra2
	if c.indexerName != "" {
		params.Set("indexers", c.indexerName)
	}

	apiURL := fmt.Sprintf("%s/api?%s", c.baseURL, params.Encode())
	logger.Debug("NZBHydra2 API URL", "url", apiURL)

	httpReq, err := http.NewRequestWithContext(context.Background(), "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	if ua := env.IndexerQueryHeader(); ua != "" {
		httpReq.Header.Set("User-Agent", ua)
	}
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to query NZBHydra2: %w", err)
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

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("NZBHydra2 returned status %d: %s", resp.StatusCode, string(body))
	}

	var result indexer.SearchResponse
	if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse NZBHydra2 response: %w", err)
	}

	// Populate SourceIndexer for each item
	for i := range result.Channel.Items {
		result.Channel.Items[i].SourceIndexer = c

		// Extract actual indexer name from Newznab attributes
		// NZBHydra2 includes the underlying indexer information in attributes
		if indexerName := result.Channel.Items[i].GetAttribute("indexer"); indexerName != "" {
			result.Channel.Items[i].ActualIndexer = indexerName
		} else if indexerName := result.Channel.Items[i].GetAttribute("hydraIndexerName"); indexerName != "" {
			result.Channel.Items[i].ActualIndexer = indexerName
		}
	}

	// Resolve all details_links in one batch call (NZBHydra2 has no public API for this; ResolveDetailsLinks fails)
	detailsLinks, err := c.ResolveDetailsLinks(req)
	if err != nil {
		logger.Debug("NZBHydra2 details resolution not available", "err", err)
		// Continue without details_links - we'll fall back to using the hash
	} else {
		// Populate ActualGUID for each item
		for i := range result.Channel.Items {
			if detailsLink, ok := detailsLinks[result.Channel.Items[i].GUID]; ok {
				result.Channel.Items[i].ActualGUID = detailsLink
			}
		}
	}

	return &result, nil
}

// DownloadNZB downloads an NZB file by URL.
// Use a context with timeout: 60s for resolve/lazy load, 5s for validation.
func (c *Client) DownloadNZB(ctx context.Context, nzbURL string) ([]byte, error) {
	if err := c.checkDownloadLimit(); err != nil {
		logger.Warn("Download limit reached for %s", "indexer", c.Name())
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	req, err := http.NewRequestWithContext(ctx, "GET", nzbURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if ua := env.IndexerGrabHeader(); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
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

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("NZB download returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read NZB data: %w", err)
	}

	return data, nil
}

func (c *Client) updateUsageFromHeaders(h http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Newznab Standard Headers often proximal through NZBHydra2
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
