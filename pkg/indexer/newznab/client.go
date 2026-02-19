package newznab

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/env"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"strings"
	"sync"
	"time"
)

// Client represents a Newznab API client for a single indexer
type Client struct {
	baseURL string
	apiPath string // API path (e.g., "/api" or "/api/v1")
	apiKey  string
	name    string
	client  *http.Client

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

// APIError represents a Newznab API error response
type APIError struct {
	XMLName     xml.Name `xml:"error"`
	Code        int      `xml:"code,attr"`
	Description string   `xml:"description,attr"`
}

// Name returns the name of this indexer
func (c *Client) Name() string {
	if c.name != "" {
		return c.name
	}
	return "Newznab"
}

// GetUsage returns the current usage stats
func (c *Client) GetUsage() indexer.Usage {
	c.mu.RLock()
	u := indexer.Usage{
		APIHitsLimit:       c.apiLimit,
		APIHitsUsed:        c.apiUsed,
		APIHitsRemaining:   c.apiRemaining,
		DownloadsLimit:     c.downloadLimit,
		DownloadsUsed:      c.downloadUsed,
		DownloadsRemaining: c.downloadRemaining,
	}
	c.mu.RUnlock()
	if c.usageManager != nil {
		ud := c.usageManager.GetIndexerUsage(c.name)
		u.AllTimeAPIHitsUsed = ud.AllTimeAPIHitsUsed
		u.AllTimeDownloadsUsed = ud.AllTimeDownloadsUsed
	}
	return u
}

// NewClient creates a new Newznab client
func NewClient(cfg config.IndexerConfig, um *indexer.UsageManager) *Client {
	// Create HTTP client with TLS skip verify for self-signed certs (common in local setups)
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		MaxConnsPerHost:     100,
		IdleConnTimeout:     90 * time.Second,
	}

	// Default API path to "/api" if not specified
	apiPath := cfg.APIPath
	if apiPath == "" {
		apiPath = "/api"
	}
	// Ensure it starts with "/"
	if !strings.HasPrefix(apiPath, "/") {
		apiPath = "/" + apiPath
	}

	c := &Client{
		name:    cfg.Name,
		baseURL: strings.TrimRight(cfg.URL, "/"),
		apiPath: apiPath,
		apiKey:  cfg.APIKey,
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
		apiLimit:          cfg.APIHitsDay,
		apiUsed:           0,
		apiRemaining:      cfg.APIHitsDay,
		downloadLimit:     cfg.DownloadsDay,
		downloadUsed:      0,
		downloadRemaining: cfg.DownloadsDay,
		usageManager:      um,
	}

	// Load initial usage if manager is provided
	if um != nil {
		usage := um.GetIndexerUsage(cfg.Name)
		c.apiUsed = usage.APIHitsUsed
		c.downloadUsed = usage.DownloadsUsed

		c.apiRemaining = cfg.APIHitsDay - usage.APIHitsUsed
		c.downloadRemaining = cfg.DownloadsDay - usage.DownloadsUsed

		// Ensure remaining isn't negative if limits were lowered
		if c.apiRemaining < 0 && cfg.APIHitsDay > 0 {
			c.apiRemaining = 0
		}
		if c.downloadRemaining < 0 && cfg.DownloadsDay > 0 {
			c.downloadRemaining = 0
		}
	}

	return c
}

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

// updateUsageFromHeaders updates remaining counts from Newznab headers
func (c *Client) updateUsageFromHeaders(h http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Newznab Standard Headers
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

	// Grab limits (Downloads)
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

	// Some indexers use non-standard headers
	if val := h.Get("x-api-remaining"); val != "" && h.Get("X-RateLimit-Daily-Remaining") == "" {
		if remaining, err := strconv.Atoi(val); err == nil {
			c.apiRemaining = remaining
		}
	}
	if val := h.Get("x-grab-remaining"); val != "" && h.Get("X-DNZBLimit-Daily-Remaining") == "" {
		if remaining, err := strconv.Atoi(val); err == nil {
			c.downloadRemaining = remaining
		}
	}

	// Update persistent storage
	if c.usageManager != nil {
		// Calculate used from headers if possible
		if c.apiLimit > 0 {
			c.apiUsed = c.apiLimit - c.apiRemaining
		} else {
			// If no limit, we can't derive "used" from "remaining".
			// We should have incremented it locally.
		}

		if c.downloadLimit > 0 {
			c.downloadUsed = c.downloadLimit - c.downloadRemaining
		}

		c.usageManager.UpdateUsage(c.name, c.apiUsed, c.downloadUsed)
	}
}

// Ping checks if the indexer is reachable
func (c *Client) Ping() error {
	apiURL := fmt.Sprintf("%s%s?t=caps&apikey=%s", c.baseURL, c.apiPath, c.apiKey)
	resp, err := c.client.Get(apiURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s indexer returned error status: %d", c.Name(), resp.StatusCode)
	}
	return nil
}

// checkNewznabError checks for Newznab error responses and returns appropriate errors
func (c *Client) checkNewznabError(bodyBytes []byte) error {
	var apiErr APIError
	if err := xml.Unmarshal(bodyBytes, &apiErr); err == nil && apiErr.Description != "" {
		// Parse error code to determine error type
		switch {
		case apiErr.Code >= 100 && apiErr.Code <= 199:
			return fmt.Errorf("%s authentication error (code %d): %s", c.Name(), apiErr.Code, apiErr.Description)
		case apiErr.Code == 201:
			return fmt.Errorf("%s request limit reached (code %d): %s", c.Name(), apiErr.Code, apiErr.Description)
		case apiErr.Code >= 200 && apiErr.Code <= 299:
			return fmt.Errorf("%s request error (code %d): %s", c.Name(), apiErr.Code, apiErr.Description)
		case apiErr.Code >= 300 && apiErr.Code <= 399:
			return fmt.Errorf("%s server error (code %d): %s", c.Name(), apiErr.Code, apiErr.Description)
		default:
			return fmt.Errorf("%s API error (code %d): %s", c.Name(), apiErr.Code, apiErr.Description)
		}
	}
	return nil
}

// Search queries the Newznab indexer
// The indexer handles pagination internally, we just request what we need
func (c *Client) Search(req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	if err := c.checkAPILimit(); err != nil {
		return nil, err
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 100 // Default limit
	}
	// Cap at 1000 as most indexers support this max
	if limit > 1000 {
		limit = 1000
	}

	params := url.Values{}
	params.Set("apikey", c.apiKey)
	params.Set("o", "xml")
	params.Set("limit", fmt.Sprintf("%d", limit))
	params.Set("offset", "0") // Start from beginning

	// Map categories to Newznab search types
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
	if req.Season != "" {
		params.Set("season", req.Season)
	}
	if req.Episode != "" {
		params.Set("ep", req.Episode)
	}

	apiURL := fmt.Sprintf("%s%s?%s", c.baseURL, c.apiPath, params.Encode())
	logger.Debug("Newznab search request", "indexer", c.Name(), "url", apiURL, "limit", limit)

	httpReq, err := http.NewRequestWithContext(context.Background(), "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if ua := env.IndexerQueryHeader(); ua != "" {
		httpReq.Header.Set("User-Agent", ua)
	}
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to query %s: %w", c.Name(), err)
	}
	defer resp.Body.Close()

	// Local increment as fallback
	c.mu.Lock()
	c.apiUsed++
	if c.apiRemaining > 0 {
		c.apiRemaining--
	}
	c.mu.Unlock()

	c.updateUsageFromHeaders(resp.Header)

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s response: %w", c.Name(), err)
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		// Try to parse Newznab error response
		if err := c.checkNewznabError(bodyBytes); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%s returned status %d: %s", c.Name(), resp.StatusCode, string(bodyBytes))
	}

	// Check for Newznab API errors in successful HTTP responses
	if err := c.checkNewznabError(bodyBytes); err != nil {
		return nil, err
	}

	var result indexer.SearchResponse
	if err := xml.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("failed to parse %s response: %w", c.Name(), err)
	}

	// Populate SourceIndexer and fix metadata for each item
	for i := range result.Channel.Items {
		item := &result.Channel.Items[i]
		item.SourceIndexer = c

		// Fallback size extraction
		if item.Size <= 0 {
			if item.Enclosure.Length > 0 {
				item.Size = item.Enclosure.Length
			} else if sizeAttr := item.GetAttribute("size"); sizeAttr != "" {
				fmt.Sscanf(sizeAttr, "%d", &item.Size)
			}
		}
	}

	// Truncate to requested limit if indexer returned more
	if len(result.Channel.Items) > limit {
		result.Channel.Items = result.Channel.Items[:limit]
	}

	return &result, nil
}

func (c *Client) DownloadNZB(ctx context.Context, nzbURL string) ([]byte, error) {
	if err := c.checkDownloadLimit(); err != nil {
		logger.Warn("Download limit reached for indexer", "indexer", c.Name())
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
		return nil, fmt.Errorf("failed to download NZB from %s: %w", c.Name(), err)
	}
	defer resp.Body.Close()

	// Local increment as fallback
	c.mu.Lock()
	c.apiUsed++ // Download also counts as API hit usually
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
		return nil, fmt.Errorf("%s NZB download returned status %d", c.Name(), resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read NZB data from %s: %w", c.Name(), err)
	}

	return data, nil
}
