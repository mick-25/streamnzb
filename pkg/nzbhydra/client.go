package nzbhydra

import (
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"streamnzb/pkg/indexer"
	"strings"
	"time"
)

// Client represents an NZBHydra2 API client
type Client struct {
	baseURL string
	apiKey  string
	client  *http.Client
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
	resp, err := c.client.Get(apiURL)
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
func NewClient(baseURL, apiKey string) (*Client, error) {
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

// Name returns the name of this indexer
func (c *Client) Name() string {
	return "NZBHydra2"
}

// Search queries NZBHydra2 for content
func (c *Client) Search(req indexer.SearchRequest) (*indexer.SearchResponse, error) {
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
	
	apiURL := fmt.Sprintf("%s/api?%s", c.baseURL, params.Encode())
	
	// Debug: Log the actual API URL being called
	fmt.Printf("NZBHydra2 API URL: %s\n", apiURL)
	
	resp, err := c.client.Get(apiURL)
	if err != nil {
		return nil, fmt.Errorf("failed to query NZBHydra2: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("NZBHydra2 returned status %d: %s", resp.StatusCode, string(body))
	}
	
	var result indexer.SearchResponse
	if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse NZBHydra2 response: %w", err)
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
