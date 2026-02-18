package nzbhydra

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"streamnzb/pkg/indexer"
	"strings"
)

// ResolveDetailsLinks queries NZBHydra2's internal search API to get the actual indexer
// details_link for all search results. This is needed because NZBHydra2 doesn't expose
// the original indexer GUID in the Newznab XML response.
//
// The details_link is the full URL to the indexer's details page (e.g.,
// "https://api.althub.co.za/details/a44b52c874536bf50062b971d2c0095c")
// which AvailNZB uses as the GUID.
//
// Returns a map of searchResultId (NZBHydra2 hash) -> details_link
func (c *Client) ResolveDetailsLinks(searchRequest indexer.SearchRequest) (map[string]string, error) {
	// Build internal API URL
	apiURL := fmt.Sprintf("%s/internalapi/search", c.baseURL)

	// Create search request matching the original Newznab search
	requestBody := map[string]interface{}{
		"searchType": determineSearchType(searchRequest),
		"limit":      1000, // Match the limit from the original search
	}

	// Add search parameters
	if searchRequest.Query != "" {
		requestBody["query"] = searchRequest.Query
	}
	if searchRequest.IMDbID != "" {
		requestBody["imdbId"] = strings.TrimPrefix(searchRequest.IMDbID, "tt")
	}
	if searchRequest.TMDBID != "" {
		requestBody["tmdbId"] = searchRequest.TMDBID
	}
	if searchRequest.TVDBID != "" {
		requestBody["tvdbId"] = searchRequest.TVDBID
	}
	if searchRequest.Season != "" {
		requestBody["season"] = searchRequest.Season
	}
	if searchRequest.Episode != "" {
		requestBody["episode"] = searchRequest.Episode
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to query internal API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("internal API returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse the response
	var result struct {
		SearchResults []struct {
			SearchResultID string `json:"searchResultId"`
			DetailsLink    string `json:"details_link"`
		} `json:"searchResults"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Build the mapping
	detailsLinks := make(map[string]string)
	for _, sr := range result.SearchResults {
		if sr.DetailsLink != "" {
			detailsLinks[sr.SearchResultID] = sr.DetailsLink
		}
	}

	return detailsLinks, nil
}

// determineSearchType converts indexer.SearchRequest category to NZBHydra2 search type
func determineSearchType(req indexer.SearchRequest) string {
	if req.Cat == "2000" {
		return "MOVIE"
	} else if req.Cat == "5000" {
		return "TVSEARCH"
	}
	return "SEARCH"
}
