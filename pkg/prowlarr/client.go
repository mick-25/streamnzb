package prowlarr

import (
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/logger"
	"strings"
	"time"
)

// Client represents a Prowlarr Newznab API client
type Client struct {
	baseURL string
	apiKey  string
	name    string
	client  *http.Client
}

// Name returns the name of this indexer
func (c *Client) Name() string {
	if c.name != "" {
		return c.name
	}
	return "Prowlarr"
}

// NewClient creates a new Prowlarr client and verifies connectivity
func NewClient(baseURL, apiKey string) (*Client, error) {
	// Create HTTP client with TLS skip verify for self-signed certs
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
		baseURL: baseURL,
		apiKey:  apiKey,
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
	}

	if err := c.Ping(); err != nil {
		return nil, err
	}

	return c, nil
}

// Ping checks if the Prowlarr server is reachable
func (c *Client) Ping() error {
	// Prowlarr health check endpoint or just root
	// We'll check the Newznab API capability endpoint
	apiURL := fmt.Sprintf("%s/api?t=caps&apikey=%s", c.baseURL, c.apiKey)
	resp, err := c.client.Get(apiURL)
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
	
	apiURL := fmt.Sprintf("%s/api?%s", c.baseURL, params.Encode())
	
	// Debug: Log the actual API URL being called
	logger.Debug("Prowlarr API URL: %s\n", apiURL)
	
	resp, err := c.client.Get(apiURL)
	if err != nil {
		return nil, fmt.Errorf("failed to query Prowlarr: %w", err)
	}
	defer resp.Body.Close()
	
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

// DownloadNZB downloads an NZB file by URL
func (c *Client) DownloadNZB(nzbURL string) ([]byte, error) {
	resp, err := c.client.Get(nzbURL)
	if err != nil {
		return nil, fmt.Errorf("failed to download NZB: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("NZB download returned status %d", resp.StatusCode)
	}
	
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read NZB data: %w", err)
	}
	
	return data, nil
}
