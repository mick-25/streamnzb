package easynews

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/indexer"
)

const (
	easynewsBaseURL     = "https://members.easynews.com"
	maxResultsPerPage   = 250
	searchTimeout       = 15 * time.Second
	downloadTimeout     = 30 * time.Second
)

// Client represents an Easynews API client
type Client struct {
	username      string
	password      string
	name          string
	client        *http.Client
	downloadBase  string // Base URL for NZB download proxying

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

// NewClient creates a new Easynews client
func NewClient(username, password, name string, downloadBase string, apiLimit, downloadLimit int, um *indexer.UsageManager) (*Client, error) {
	if username == "" || password == "" {
		return nil, fmt.Errorf("Easynews username and password are required")
	}

	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		MaxConnsPerHost:     100,
		IdleConnTimeout:     90 * time.Second,
	}

	c := &Client{
		username:      username,
		password:      password,
		name:          name,
		downloadBase:  downloadBase,
		usageManager:  um,
		apiLimit:      apiLimit,
		apiUsed:       0,
		apiRemaining:  apiLimit,
		downloadLimit: downloadLimit,
		downloadUsed:  0,
		downloadRemaining: downloadLimit,
		client: &http.Client{
			Timeout:   searchTimeout,
			Transport: transport,
		},
	}

	// Load initial usage if manager is provided
	if um != nil && name != "" {
		usage := um.GetIndexerUsage(name)
		c.apiUsed = usage.APIHitsUsed
		c.downloadUsed = usage.DownloadsUsed

		c.apiRemaining = apiLimit - usage.APIHitsUsed
		c.downloadRemaining = downloadLimit - usage.DownloadsUsed

		// Ensure remaining isn't negative if limits were lowered
		if c.apiRemaining < 0 && apiLimit > 0 {
			c.apiRemaining = 0
		}
		if c.downloadRemaining < 0 && downloadLimit > 0 {
			c.downloadRemaining = 0
		}
	}

	return c, nil
}


// Name returns the name of this indexer
func (c *Client) Name() string {
	if c.name != "" {
		return c.name
	}
	return "Easynews"
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

// Ping checks if Easynews credentials are valid
func (c *Client) Ping() error {
	// Test with a simple search
	testQuery := "dune"
	_, err := c.searchInternal(testQuery, "", "", "", false)
	if err != nil {
		return fmt.Errorf("Easynews credentials invalid: %w", err)
	}
	return nil
}

// Search queries Easynews for content
func (c *Client) Search(req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	if err := c.checkAPILimit(); err != nil {
		return nil, err
	}

	// Build query from request
	query := req.Query
	if req.IMDbID != "" {
		// Remove 'tt' prefix if present
		imdbID := strings.TrimPrefix(req.IMDbID, "tt")
		query = fmt.Sprintf("%s %s", query, imdbID)
	}
	if req.TMDBID != "" {
		query = fmt.Sprintf("%s %s", query, req.TMDBID)
	}

	season := req.Season
	episode := req.Episode

	// Perform search
	results, err := c.searchInternal(query, season, episode, req.Cat, false)
	if err != nil {
		return nil, fmt.Errorf("Easynews search failed: %w", err)
	}

	// Increment API usage
	c.mu.Lock()
	c.apiUsed++
	if c.apiRemaining > 0 {
		c.apiRemaining--
	}
	c.mu.Unlock()

	// Save usage if manager is provided
	if c.usageManager != nil && c.name != "" {
		c.usageManager.IncrementUsed(c.name, 1, 0)
	}

	// Convert Easynews results to Newznab format
	items := make([]indexer.Item, 0, len(results))
	for _, result := range results {
		item := indexer.Item{
			Title:       result.Title,
			Link:        result.DownloadURL,
			GUID:        result.GUID,
			PubDate:     result.PubDate,
			Size:        result.Size,
			SourceIndexer: c,
		}
		items = append(items, item)
	}

	return &indexer.SearchResponse{
		Channel: indexer.Channel{
			Items: items,
		},
	}, nil
}

// DownloadNZB downloads an NZB file.
// ctx is used for timeout; use 60s for resolve/lazy load, 5s for validation.
func (c *Client) DownloadNZB(ctx context.Context, nzbURL string) ([]byte, error) {
	if err := c.checkDownloadLimit(); err != nil {
		return nil, err
	}

	// Easynews URLs are proxied through our server
	// Format: {downloadBase}/easynews/nzb?payload={token}
	// We need to extract the payload and download from Easynews
	parsedURL, err := url.Parse(nzbURL)
	if err != nil {
		return nil, fmt.Errorf("invalid NZB URL: %w", err)
	}

	payloadToken := parsedURL.Query().Get("payload")
	if payloadToken == "" {
		return nil, fmt.Errorf("missing payload token in URL")
	}

	// Decode payload to get hash, filename, ext, sig
	payload, err := decodePayload(payloadToken)
	if err != nil {
		return nil, fmt.Errorf("invalid payload token: %w", err)
	}

	// Build NZB download request
	nzbData, err := c.downloadNZBInternal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to download NZB: %w", err)
	}

	// Increment download usage
	c.mu.Lock()
	c.apiUsed++ // Download also counts as API hit
	c.downloadUsed++
	if c.apiRemaining > 0 {
		c.apiRemaining--
	}
	if c.downloadRemaining > 0 {
		c.downloadRemaining--
	}
	c.mu.Unlock()

	// Save usage if manager is provided
	if c.usageManager != nil && c.name != "" {
		c.usageManager.IncrementUsed(c.name, 1, 1)
	}

	return nzbData, nil
}

// searchInternal performs the actual Easynews search
func (c *Client) searchInternal(query, season, episode, category string, strictMode bool) ([]easynewsResult, error) {
	params := url.Values{}
	params.Set("fly", "2")
	params.Set("sb", "1")
	params.Set("pno", "1")
	params.Set("pby", strconv.Itoa(maxResultsPerPage))
	params.Set("u", "1")
	params.Set("chxu", "1")
	params.Set("chxgx", "1")
	params.Set("st", "basic")
	params.Set("gps", query)
	params.Set("vv", "1")
	params.Set("safeO", "0")
	params.Set("s1", "relevance")
	params.Set("s1d", "-")
	params.Add("fty[]", "VIDEO")

	// Add category filters if specified
	if category == "2000" {
		// Movies - could add specific filters
	} else if category == "5000" {
		// TV - add season/episode to query if provided
		if season != "" && episode != "" {
			params.Set("gps", fmt.Sprintf("%s S%sE%s", query, season, episode))
		}
	}

	searchURL := fmt.Sprintf("%s/2.0/search/solr-search/?%s", easynewsBaseURL, params.Encode())

	ctx, cancel := context.WithTimeout(context.Background(), searchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, err
	}

	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("User-Agent", "StreamNZB-Easynews/1.0")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Easynews search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("Easynews rejected credentials")
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Easynews search failed with status %d: %s", resp.StatusCode, string(body))
	}

	var data easynewsSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to parse Easynews response: %w", err)
	}

	// Filter and map results
	results := c.filterAndMapResults(data, query, season, episode, strictMode)

	return results, nil
}

// downloadNZBInternal downloads NZB from Easynews
func (c *Client) downloadNZBInternal(payload map[string]interface{}) ([]byte, error) {
	hash, _ := payload["hash"].(string)
	filename, _ := payload["filename"].(string)
	ext, _ := payload["ext"].(string)
	sig, _ := payload["sig"].(string)
	title, _ := payload["title"].(string)

	if hash == "" {
		return nil, fmt.Errorf("missing hash in payload")
	}

	// Build NZB payload
	nzbEntries := buildNZBPayload([]easynewsItem{
		{Hash: hash, Filename: filename, Ext: ext, Sig: sig},
	}, title)

	form := url.Values{}
	for key, value := range nzbEntries {
		form.Set(key, value)
	}

	ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", easynewsBaseURL+"/2.0/api/dl-nzb", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}

	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "StreamNZB-Easynews/1.0")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Easynews NZB download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Easynews NZB download failed with status %d: %s", resp.StatusCode, string(body))
	}

	nzbData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read NZB data: %w", err)
	}

	return nzbData, nil
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

// easynewsSearchResponse represents the Easynews search API response
type easynewsSearchResponse struct {
	Data   []interface{} `json:"data"`
	Total  int           `json:"total"`
	ThumbURL string      `json:"thumbURL"`
}

// easynewsResult represents a filtered Easynews search result
type easynewsResult struct {
	Title       string
	DownloadURL string
	GUID        string
	PubDate     string
	Size        int64
}

// easynewsItem represents an item in the Easynews data array
type easynewsItem struct {
	Hash     string
	Filename string
	Ext      string
	Sig      string
	Size     int64
	Subject  string
	Poster   string
	Posted   string
	Duration interface{}
}

// filterAndMapResults filters and maps Easynews results
func (c *Client) filterAndMapResults(data easynewsSearchResponse, query, season, episode string, strictMode bool) []easynewsResult {
	results := make([]easynewsResult, 0)

	disallowedExts := map[string]bool{
		".rar": true, ".zip": true, ".exe": true, ".jpg": true, ".png": true,
	}
	allowedVideoExts := map[string]bool{
		".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".ts": true,
		".mov": true, ".wmv": true, ".mpg": true, ".mpeg": true, ".flv": true, ".webm": true,
	}

	for _, entry := range data.Data {
		var item easynewsItem

		// Handle array format: [hash, ..., subject, poster, posted, ..., filename, ext, size, ..., duration]
		// Based on Easynews API: [0:hash, 6:subject, 7:poster, 8:posted, 10:filename, 11:ext, 12:size, 14:duration]
		if arr, ok := entry.([]interface{}); ok && len(arr) >= 12 {
			if hash, ok := arr[0].(string); ok {
				item.Hash = hash
			}
			if subject, ok := arr[6].(string); ok {
				item.Subject = subject
			}
			if filename, ok := arr[10].(string); ok {
				item.Filename = filename
			}
			if ext, ok := arr[11].(string); ok {
				item.Ext = ext
			}
			if poster, ok := arr[7].(string); ok {
				item.Poster = poster
			}
			if posted, ok := arr[8].(string); ok {
				item.Posted = posted
			}
			// Extract size (position 12)
			if len(arr) > 12 {
				if sizeVal, ok := arr[12].(float64); ok {
					item.Size = int64(sizeVal)
				} else if sizeVal, ok := arr[12].(int64); ok {
					item.Size = sizeVal
				} else if sizeVal, ok := arr[12].(int); ok {
					item.Size = int64(sizeVal)
				}
			}
			if len(arr) > 14 {
				item.Duration = arr[14]
			}
		} else if obj, ok := entry.(map[string]interface{}); ok {
			// Handle object format
			if hash, ok := obj["hash"].(string); ok {
				item.Hash = hash
			}
			if subject, ok := obj["subject"].(string); ok {
				item.Subject = subject
			}
			if filename, ok := obj["filename"].(string); ok {
				item.Filename = filename
			}
			if ext, ok := obj["ext"].(string); ok {
				item.Ext = ext
			}
			if size, ok := obj["size"].(float64); ok {
				item.Size = int64(size)
			}
			if sig, ok := obj["sig"].(string); ok {
				item.Sig = sig
			}
		}

		if item.Hash == "" {
			continue
		}

		// Filter by extension
		extLower := strings.ToLower(item.Ext)
		if !strings.HasPrefix(extLower, ".") {
			extLower = "." + extLower
		}
		if disallowedExts[extLower] {
			continue
		}
		if extLower != "" && !allowedVideoExts[extLower] {
			continue
		}

		// Parse duration and filter short videos (< 60 seconds)
		durationSeconds := parseDuration(item.Duration)
		if durationSeconds != nil && *durationSeconds < 60 {
			continue
		}

		// Build title
		title := item.Filename
		if item.Ext != "" {
			if !strings.HasPrefix(item.Ext, ".") {
				title += "." + item.Ext
			} else {
				title += item.Ext
			}
		}
		if title == "" {
			title = item.Subject
		}
		if title == "" {
			title = item.Hash
		}

		// Filter samples
		titleLower := strings.ToLower(title)
		if strings.Contains(titleLower, "sample") {
			continue
		}

		// Use filename+ext for final title if available, otherwise subject
		finalTitle := title
		if finalTitle == "" {
			finalTitle = item.Subject
		}
		if finalTitle == "" {
			finalTitle = fmt.Sprintf("Easynews-%s", item.Hash[:min(8, len(item.Hash))])
		}

		// Build download URL using payload token
		payload := map[string]interface{}{
			"hash":     item.Hash,
			"filename": item.Filename,
			"ext":      item.Ext,
			"sig":      item.Sig,
			"title":    finalTitle,
		}
		payloadToken := encodePayload(payload)
		downloadURL := fmt.Sprintf("%s/easynews/nzb?payload=%s", c.downloadBase, url.QueryEscape(payloadToken))

		// Parse posted date
		pubDate := time.Now().Format(time.RFC1123Z)
		if item.Posted != "" {
			if t, err := time.Parse("2006-01-02 15:04:05", item.Posted); err == nil {
				pubDate = t.Format(time.RFC1123Z)
			}
		}

		results = append(results, easynewsResult{
			Title:       finalTitle,
			DownloadURL: downloadURL,
			GUID:        fmt.Sprintf("easynews-%s", item.Hash),
			PubDate:     pubDate,
			Size:        item.Size,
		})
	}

	return results
}

// parseDuration parses duration from various formats and returns seconds
func parseDuration(raw interface{}) *int64 {
	if raw == nil {
		return nil
	}

	switch v := raw.(type) {
	case float64:
		if v > 0 {
			sec := int64(v)
			return &sec
		}
	case int64:
		if v > 0 {
			return &v
		}
	case int:
		if v > 0 {
			sec := int64(v)
			return &sec
		}
	case string:
		// Try parsing as number
		if num, err := strconv.ParseInt(v, 10, 64); err == nil && num > 0 {
			return &num
		}
		// Try parsing duration formats like "1h23m45s" or "1:23:45"
		if strings.Contains(v, ":") {
			parts := strings.Split(v, ":")
			if len(parts) == 3 {
				h, _ := strconv.Atoi(parts[0])
				m, _ := strconv.Atoi(parts[1])
				s, _ := strconv.Atoi(parts[2])
				total := int64(h*3600 + m*60 + s)
				if total > 0 {
					return &total
				}
			} else if len(parts) == 2 {
				m, _ := strconv.Atoi(parts[0])
				s, _ := strconv.Atoi(parts[1])
				total := int64(m*60 + s)
				if total > 0 {
					return &total
				}
			}
		}
	}

	return nil
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// encodePayload encodes a payload map to a base64 URL-safe token
func encodePayload(payload map[string]interface{}) string {
	jsonData, _ := json.Marshal(payload)
	encoded := base64.URLEncoding.EncodeToString(jsonData)
	return strings.TrimRight(encoded, "=")
}

// decodePayload decodes a base64 URL-safe token to a payload map
func decodePayload(token string) (map[string]interface{}, error) {
	// Add padding if needed
	padLen := (4 - len(token)%4) % 4
	token += strings.Repeat("=", padLen)

	decoded, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return nil, err
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return nil, err
	}

	return payload, nil
}

// buildNZBPayload builds the form data for NZB download
func buildNZBPayload(items []easynewsItem, name string) map[string]string {
	result := map[string]string{
		"autoNZB": "1",
	}

	for i, item := range items {
		key := strconv.Itoa(i)
		if item.Sig != "" {
			key = fmt.Sprintf("%d&sig=%s", i, item.Sig)
		}
		value := buildValueToken(item)
		result[key] = value
	}

	if name != "" {
		result["nameZipQ0"] = name
	}

	return result
}

// buildValueToken builds the value token for an item
func buildValueToken(item easynewsItem) string {
	fnB64 := base64.URLEncoding.EncodeToString([]byte(item.Filename))
	extB64 := base64.URLEncoding.EncodeToString([]byte(item.Ext))
	return fmt.Sprintf("%s|%s:%s", item.Hash, fnB64, extB64)
}
