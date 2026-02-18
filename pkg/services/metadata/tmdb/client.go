package tmdb

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"streamnzb/pkg/core/logger"
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
	Name          string `json:"name"`           // TV
	Title         string `json:"title"`          // Movie
	OriginalName  string `json:"original_name"`  // TV
	OriginalTitle string `json:"original_title"` // Movie
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

// MovieDetails is the response from GET /movie/{id}
type MovieDetails struct {
	ID           int    `json:"id"`
	Title        string `json:"title"`
	ReleaseDate  string `json:"release_date"`
	OriginalTitle string `json:"original_title"`
}

// TVDetails is the response from GET /tv/{id}
type TVDetails struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// GetMovieTitle returns the movie title for text-based search.
// Supports IMDb ID (tt123) or TMDB ID.
func (c *Client) GetMovieTitle(imdbID string, tmdbID string) (string, error) {
	if tmdbID != "" {
		if id, err := strconv.Atoi(tmdbID); err == nil {
			d, err := c.GetMovieDetails(id)
			if err != nil {
				return "", err
			}
			return d.Title, nil
		}
	}
	if imdbID != "" {
		find, err := c.Find(imdbID, "imdb_id")
		if err != nil {
			return "", err
		}
		if len(find.MovieResults) > 0 {
			return find.MovieResults[0].Title, nil
		}
	}
	return "", fmt.Errorf("could not resolve movie title")
}

// GetTVShowName returns the TV show name for text-based search.
// Supports TMDB ID or IMDb ID (tt123).
func (c *Client) GetTVShowName(tmdbID string, imdbID string) (string, error) {
	if tmdbID != "" {
		if id, err := strconv.Atoi(tmdbID); err == nil {
			d, err := c.GetTVDetails(id)
			if err != nil {
				return "", err
			}
			return d.Name, nil
		}
	}
	if imdbID != "" {
		find, err := c.Find(imdbID, "imdb_id")
		if err != nil {
			return "", err
		}
		if len(find.TVResults) > 0 {
			return find.TVResults[0].Name, nil
		}
	}
	return "", fmt.Errorf("could not resolve TV show name")
}

// GetMovieDetails fetches movie title for text-based search.
func (c *Client) GetMovieDetails(tmdbID int) (*MovieDetails, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("TMDB API key not configured")
	}
	endpoint := fmt.Sprintf("https://api.themoviedb.org/3/movie/%d", tmdbID)
	resp, err := c.doRequest(endpoint, url.Values{})
	if err != nil {
		return nil, fmt.Errorf("TMDB movie details: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TMDB returned status: %d", resp.StatusCode)
	}
	var d MovieDetails
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("TMDB movie decode: %w", err)
	}
	return &d, nil
}

// GetTVDetails fetches TV show name for text-based search.
func (c *Client) GetTVDetails(tmdbID int) (*TVDetails, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("TMDB API key not configured")
	}
	endpoint := fmt.Sprintf("https://api.themoviedb.org/3/tv/%d", tmdbID)
	resp, err := c.doRequest(endpoint, url.Values{})
	if err != nil {
		return nil, fmt.Errorf("TMDB TV details: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TMDB returned status: %d", resp.StatusCode)
	}
	var d TVDetails
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("TMDB TV decode: %w", err)
	}
	return &d, nil
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
