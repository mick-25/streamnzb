package availnzb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/release"
)

const apiPath = "/api/v1"

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// ReportRequest is the body for POST /api/v1/report (authenticated).
// For movies: set ImdbID. For TV: set TvdbID, Season, Episode.
// download_link is not sent; status/releases return it (Newznab API URL without apikey—add apikey when fetching).
// compression_type: "direct", "7z", or "rar" — used by AvailNZB to filter GetReleases.
type ReportRequest struct {
	URL             string `json:"url"`                        // Indexer release URL (details link)
	ReleaseName     string `json:"release_name"`               // Release name (e.g. Show.Name.S01E02.720p.WEB.x264-GROUP)
	Size            int64  `json:"size"`                       // File size in bytes (required)
	CompressionType string `json:"compression_type,omitempty"` // "direct", "7z", or "rar"
	ProviderURL     string `json:"provider_url"`               // Usenet provider hostname
	Status          bool   `json:"status"`                     // true = available, false = failed
	ImdbID          string `json:"imdb_id,omitempty"`          // Required for movies
	TvdbID          string `json:"tvdb_id,omitempty"`          // Required for TV (with season, episode)
	Season          int    `json:"season,omitempty"`           // Required for TV
	Episode         int    `json:"episode,omitempty"`          // Required for TV
}

// ProviderStatus is one provider's status in a summary.
type ProviderStatus struct {
	Text        string    `json:"text"`
	LastUpdated time.Time `json:"last_updated"`
	Healthy     bool      `json:"healthy"`
}

// StatusResponse is the response from GET /api/v1/status?url=...
type StatusResponse struct {
	URL          string                    `json:"url"`
	Available    bool                      `json:"available"`
	ReleaseName  string                    `json:"release_name,omitempty"`
	DownloadLink string                    `json:"download_link,omitempty"`
	Size         int64                     `json:"size,omitempty"`
	Summary      map[string]ProviderStatus `json:"summary"`
}

// releaseItemJSON is the JSON shape for GET /api/v1/releases (unmarshal only).
type releaseItemJSON struct {
	URL             string                    `json:"url"`
	ReleaseName     string                    `json:"release_name,omitempty"`
	DownloadLink    string                    `json:"download_link,omitempty"`
	Size            int64                     `json:"size,omitempty"`
	CompressionType string                    `json:"compression_type,omitempty"`
	Indexer         string                    `json:"indexer"`
	Available       bool                      `json:"available"`
	Summary         map[string]ProviderStatus `json:"summary"`
}

// ReleaseWithStatus wraps release.Release with AvailNZB-specific status.
// Returned by GetReleases so handlers work with a single type.
type ReleaseWithStatus struct {
	*release.Release
	Available       bool
	CompressionType string
	Summary         map[string]ProviderStatus
}

// ReleasesResult is the return value of GetReleases.
type ReleasesResult struct {
	ImdbID   string
	Count    int
	Releases []*ReleaseWithStatus
}

// ReportMeta holds data for reporting (movie or TV). ReleaseName and Size are required by the API.
// CompressionType: "direct", "7z", or "rar" — optional; when empty, API may default.
type ReportMeta struct {
	ReleaseName     string // Release name (e.g. item.Title)
	Size            int64  // File size in bytes (required)
	CompressionType string // "direct", "7z", or "rar"
	ImdbID          string // For movies
	TvdbID          string // For TV
	Season          int
	Episode         int
}

func NewClient(baseURL, apiKey string) *Client {
	baseURL = strings.TrimSuffix(baseURL, "/")
	return &Client{
		BaseURL: baseURL,
		APIKey:  apiKey,
		HTTP: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// ReportAvailability submits an availability report for a release (POST /api/v1/report).
// releaseURL is the indexer details URL. meta.ReleaseName is required; meta must have either ImdbID (movie) or TvdbID+Season+Episode (TV).
func (c *Client) ReportAvailability(releaseURL string, providerURL string, status bool, meta ReportMeta) error {
	if c.BaseURL == "" {
		logger.Debug("AvailNZB report skipped", "reason", "no base URL configured")
		return nil
	}
	if c.APIKey == "" {
		logger.Debug("AvailNZB report skipped", "reason", "no API key configured")
		return nil
	}
	if meta.ReleaseName == "" {
		logger.Debug("AvailNZB report skipped", "reason", "no release_name in meta", "url", releaseURL)
		return nil
	}

	body := ReportRequest{
		URL:             releaseURL,
		ReleaseName:     meta.ReleaseName,
		Size:            meta.Size,
		CompressionType: meta.CompressionType,
		ProviderURL:     providerURL,
		Status:          status,
	}
	if meta.ImdbID != "" {
		body.ImdbID = meta.ImdbID
	} else if meta.TvdbID != "" {
		body.TvdbID = meta.TvdbID
		body.Season = meta.Season
		body.Episode = meta.Episode
	}
	if body.ImdbID == "" && body.TvdbID == "" {
		logger.Debug("AvailNZB report skipped", "reason", "no imdb_id or tvdb_id in meta", "url", releaseURL)
		return nil
	}

	logger.Debug("AvailNZB report", "url", releaseURL, "release_name", body.ReleaseName, "provider", providerURL, "status", status, "imdb_id", body.ImdbID, "tvdb_id", body.TvdbID, "season", body.Season, "episode", body.Episode)

	reqBody, err := json.Marshal(body)
	if err != nil {
		logger.Error("AvailNZB report marshal failed", "err", err)
		return err
	}

	req, err := http.NewRequest("POST", c.BaseURL+apiPath+"/report", bytes.NewBuffer(reqBody))
	if err != nil {
		return err
	}

	req.Header.Set("X-API-Key", c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		logger.Error("AvailNZB report request failed", "err", err, "url", releaseURL)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		logger.Error("AvailNZB report unexpected status", "status", resp.StatusCode, "url", releaseURL)
		return fmt.Errorf("availnzb report: unexpected status code: %d", resp.StatusCode)
	}

	logger.Debug("AvailNZB report success", "url", releaseURL, "status_code", resp.StatusCode)
	return nil
}

// GetStatus returns availability for a release by URL (GET /api/v1/status?url=...).
// provider is optional; if non-empty, filters by that provider.
func (c *Client) GetStatus(releaseURL string, provider string) (*StatusResponse, error) {
	if c.BaseURL == "" {
		logger.Trace("AvailNZB GetStatus skipped", "reason", "no base URL")
		return nil, nil
	}

	params := url.Values{}
	params.Set("url", releaseURL)
	if provider != "" {
		params.Set("provider", provider)
	}
	reqURL := c.BaseURL + apiPath + "/status?" + params.Encode()

	logger.Debug("AvailNZB GetStatus", "url", releaseURL, "provider", provider)

	resp, err := c.HTTP.Get(reqURL)
	if err != nil {
		logger.Error("AvailNZB GetStatus request failed", "err", err, "url", releaseURL)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		logger.Debug("AvailNZB GetStatus", "result", "not_found", "url", releaseURL)
		return nil, nil // No reports yet
	}

	if resp.StatusCode != http.StatusOK {
		logger.Error("AvailNZB GetStatus unexpected status", "status", resp.StatusCode, "url", releaseURL)
		return nil, fmt.Errorf("availnzb status: unexpected status code: %d", resp.StatusCode)
	}

	var status StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		logger.Error("AvailNZB GetStatus decode failed", "err", err)
		return nil, err
	}

	logger.Debug("AvailNZB GetStatus", "url", releaseURL, "available", status.Available, "providers", len(status.Summary))
	return &status, nil
}

// releasesResponseJSON is the JSON shape for GET /api/v1/releases (unmarshal only).
type releasesResponseJSON struct {
	ImdbID   string            `json:"imdb_id,omitempty"`
	Count    int               `json:"count"`
	Releases []releaseItemJSON `json:"releases"`
}

// GetReleases returns all cached releases for content (GET /api/v1/releases).
// Pass empty compressionTypes to get all; filter/split streamable vs RAR client-side.
func (c *Client) GetReleases(imdbID string, tvdbID string, season, episode int, indexers []string, compressionTypes string) (*ReleasesResult, error) {
	if c.BaseURL == "" {
		logger.Trace("AvailNZB GetReleases skipped", "reason", "no base URL")
		return nil, nil
	}

	params := url.Values{}
	if imdbID != "" {
		params.Set("imdb_id", imdbID)
	} else if tvdbID != "" {
		params.Set("tvdb_id", tvdbID)
		params.Set("season", strconv.Itoa(season))
		params.Set("episode", strconv.Itoa(episode))
	} else {
		return nil, fmt.Errorf("availnzb releases: need imdb_id or tvdb_id+season+episode")
	}
	if len(indexers) > 0 {
		params.Set("indexers", strings.Join(indexers, ","))
	}
	if compressionTypes != "" {
		params.Set("compression_types", compressionTypes)
	}
	reqURL := c.BaseURL + apiPath + "/releases?" + params.Encode()

	logger.Debug("AvailNZB GetReleases", "imdb_id", imdbID, "tvdb_id", tvdbID, "season", season, "episode", episode, "indexer_filter", len(indexers))

	resp, err := c.HTTP.Get(reqURL)
	if err != nil {
		logger.Error("AvailNZB GetReleases request failed", "err", err, "imdb_id", imdbID, "tvdb_id", tvdbID)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Error("AvailNZB GetReleases unexpected status", "status", resp.StatusCode, "imdb_id", imdbID, "tvdb_id", tvdbID)
		return nil, fmt.Errorf("availnzb releases: unexpected status code: %d", resp.StatusCode)
	}

	var raw releasesResponseJSON
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		logger.Error("AvailNZB GetReleases decode failed", "err", err)
		return nil, err
	}

	releases := make([]*ReleaseWithStatus, 0, len(raw.Releases))
	availableCount := 0
	for i := range raw.Releases {
		r := &raw.Releases[i]
		idx := r.Indexer
		if idx == "" {
			idx = "AvailNZB"
		}
		rel := &release.Release{
			Title:      r.ReleaseName,
			Link:       r.DownloadLink,
			DetailsURL: r.URL,
			Size:       r.Size,
			Indexer:    idx,
		}
		releases = append(releases, &ReleaseWithStatus{
			Release:         rel,
			Available:       r.Available,
			CompressionType: r.CompressionType,
			Summary:         r.Summary,
		})
		if r.Available {
			availableCount++
		}
	}
	logger.Debug("AvailNZB GetReleases", "count", raw.Count, "available", availableCount, "imdb_id", imdbID, "tvdb_id", tvdbID)
	return &ReleasesResult{ImdbID: raw.ImdbID, Count: raw.Count, Releases: releases}, nil
}

// CheckPreDownload checks if the release URL is already known and healthy for one of validProviders.
// Returns: available (can skip validation), last updated time, capable provider, error.
// Use releaseURL (indexer release URL, e.g. item.Link) to query GET /api/v1/status.
func (c *Client) CheckPreDownload(releaseURL string, validProviders []string) (available bool, lastUpdated time.Time, capableProvider string, err error) {
	logger.Debug("AvailNZB CheckPreDownload", "url", releaseURL, "our_providers", len(validProviders))
	if c.BaseURL == "" || releaseURL == "" {
		logger.Trace("AvailNZB CheckPreDownload skipped", "reason", "no base URL or empty release URL")
		return false, time.Time{}, "", nil
	}

	status, err := c.GetStatus(releaseURL, "")
	if err != nil {
		logger.Debug("AvailNZB CheckPreDownload GetStatus failed", "url", releaseURL, "err", err)
		return false, time.Time{}, "", err
	}
	if status == nil {
		logger.Debug("AvailNZB CheckPreDownload", "result", "not_found", "url", releaseURL)
		return false, time.Time{}, "", nil
	}

	ourProviders := make(map[string]bool)
	for _, p := range validProviders {
		ourProviders[p] = true
	}

	for providerHost, report := range status.Summary {
		if ourProviders[providerHost] && report.Healthy {
			if report.LastUpdated.After(lastUpdated) {
				lastUpdated = report.LastUpdated
			}
			available = true
			capableProvider = providerHost
			break
		}
	}
	// If no provider match but API says available, still trust it and use latest from summary
	if status.Available && !available && len(status.Summary) > 0 {
		for _, report := range status.Summary {
			if report.LastUpdated.After(lastUpdated) {
				lastUpdated = report.LastUpdated
			}
		}
		available = status.Available
	}

	logger.Debug("AvailNZB CheckPreDownload", "result", "found", "available", available, "capable_provider", capableProvider, "url", releaseURL)
	return available, lastUpdated, capableProvider, nil
}
