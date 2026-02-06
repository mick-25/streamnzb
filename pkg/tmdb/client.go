package tmdb

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"streamnzb/pkg/logger"
	"time"
)

// Client for TheMovieDB API
type Client struct {
	apiKey string
	client *http.Client
}

// NewClient creates a new TMDB client
func NewClient(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// FindResponse represents the response from /find/{id}
type FindResponse struct {
	MovieResults     []Result `json:"movie_results"`
	PersonResults    []Result `json:"person_results"`
	TVResults        []Result `json:"tv_results"`
	TVEpisodeResults []Result `json:"tv_episode_results"`
	TVSeasonResults  []Result `json:"tv_season_results"`
}

// Result represents a search result item
type Result struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`            // TV
	Title         string `json:"title"`           // Movie
	OriginalName  string `json:"original_name"`   // TV
	OriginalTitle string `json:"original_title"`  // Movie
	MediaType     string `json:"media_type"`
	Overview      string `json:"overview"`
}

// ExternalIDsResponse represents the response from /{type}/{id}/external_ids
type ExternalIDsResponse struct {
	ID          int    `json:"id"`
	IMDbID      string `json:"imdb_id"`
	TVDBID      int    `json:"tvdb_id"`
	FreebaseID  string `json:"freebase_id"`
	WikidataID  string `json:"wikidata_id"`
	FacebookID  string `json:"facebook_id"`
	InstagramID string `json:"instagram_id"`
	TwitterID   string `json:"twitter_id"`
}

func (c *Client) doRequest(endpoint string, params url.Values) (*http.Response, error) {
	reqURL := fmt.Sprintf("%s?%s", endpoint, params.Encode())
	
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("accept", "application/json")

	return c.client.Do(req)
}

// Find searches for objects by external ID (IMDb ID)
// source: 'imdb_id', 'tvdb_id', etc.
func (c *Client) Find(externalID, source string) (*FindResponse, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("TMDB API key not configured")
	}

	endpoint := fmt.Sprintf("https://api.themoviedb.org/3/find/%s", externalID)
	params := url.Values{}
	params.Set("external_source", source)

	resp, err := c.doRequest(endpoint, params)
	if err != nil {
		return nil, fmt.Errorf("TMDB find request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TMDB returned status: %d", resp.StatusCode)
	}

	var result FindResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode TMDB response: %w", err)
	}

	return &result, nil
}

// GetExternalIDs retrieves external IDs for a specific TMDB object
// mediaType: 'movie' or 'tv'
func (c *Client) GetExternalIDs(tmdbID int, mediaType string) (*ExternalIDsResponse, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("TMDB API key not configured")
	}

	endpoint := fmt.Sprintf("https://api.themoviedb.org/3/%s/%d/external_ids", mediaType, tmdbID)
	params := url.Values{}

	resp, err := c.doRequest(endpoint, params)
	if err != nil {
		return nil, fmt.Errorf("TMDB external_ids request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TMDB returned status: %d", resp.StatusCode)
	}

	var result ExternalIDsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode TMDB response: %w", err)
	}

	return &result, nil
}

// ResolveTVDBID tries to find the TVDB ID for a given IMDb string (e.g. tt123456)
func (c *Client) ResolveTVDBID(imdbID string) (string, error) {
	// 1. Find the TMDB ID from IMDb ID
	findResp, err := c.Find(imdbID, "imdb_id")
	if err != nil {
		return "", err
	}

	// Check if we found a TV show
	if len(findResp.TVResults) == 0 {
		return "", fmt.Errorf("no TV show found for IMDb ID: %s", imdbID)
	}

	tmdbID := findResp.TVResults[0].ID
	logger.Debug("Resolved TMDB ID from IMDb", "imdb", imdbID, "tmdb", tmdbID)

	// 2. Get External IDs using TMDB ID
	extIDs, err := c.GetExternalIDs(tmdbID, "tv")
	if err != nil {
		return "", err
	}

	if extIDs.TVDBID == 0 {
		return "", fmt.Errorf("no TVDB ID found for TMDB ID: %d", tmdbID)
	}

	logger.Debug("Resolved TVDB ID", "tvdb", extIDs.TVDBID)
	return strconv.Itoa(extIDs.TVDBID), nil
}
