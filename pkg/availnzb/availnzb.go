package availnzb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

type ReportRequest struct {
	NZBID       string `json:"nzb_id"`
	ProviderURL string `json:"provider_url"`
	Status      bool   `json:"status"`
	Indexer     string `json:"indexer,omitempty"`
	ExternalID  string `json:"external_id,omitempty"`
}

type ProviderStatus struct {
	Text        string    `json:"text"`
	LastUpdated time.Time `json:"last_updated"`
	Healthy     bool      `json:"healthy"`
}

type StatusResponse struct {
	NZBID       string                    `json:"nzb_id"`
	Available   bool                      `json:"available"`
	Summary     map[string]ProviderStatus `json:"summary"`
	LastUpdated time.Time                 `json:"last_updated"` // Optional root level
}

func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: baseURL,
		APIKey:  apiKey,
		HTTP: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *Client) ReportAvailability(nzbID string, providerURL string, status bool, indexerName, externalID string) error {
	if c.APIKey == "" {
		return nil // Skip if no unconfigured
	}

	reqBody, err := json.Marshal(ReportRequest{
		NZBID:       nzbID,
		ProviderURL: providerURL,
		Status:      status,
		Indexer:     indexerName,
		ExternalID:  externalID,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", c.BaseURL+"/report", bytes.NewBuffer(reqBody))
	if err != nil {
		return err
	}

	req.Header.Set("X-API-Key", c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

func (c *Client) GetStatus(nzbID string) (*StatusResponse, error) {
	resp, err := c.HTTP.Get(fmt.Sprintf("%s/status/%s", c.BaseURL, nzbID))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // No reports yet
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var status StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, err
	}

	return &status, nil
}

// CheckPreDownload checks if the NZB is already known by its Indexer ID
// Returns the NZB ID (hash), availability status, last updated time, and the capable provider if known
func (c *Client) CheckPreDownload(indexerName, externalID string, validProviders []string) (string, bool, time.Time, string, error) {
	if c.APIKey == "" {
		return "", false, time.Time{}, "", nil
	}

	url := fmt.Sprintf("%s/status/placeholder?indexer=%s&external_id=%s", c.BaseURL, indexerName, externalID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", false, time.Time{}, "", err
	}
	
	req.Header.Set("X-API-Key", c.APIKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", false, time.Time{}, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", false, time.Time{}, "", nil // Unknown mapping
	}

	if resp.StatusCode != http.StatusOK {
		return "", false, time.Time{}, "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var status StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return "", false, time.Time{}, "", err
	}
	
	// Check if *any* of OUR providers has a good report in the summary
	isHealthy := false
	capableProvider := ""
	
	// Create a map for faster lookup of our providers
	ourProviders := make(map[string]bool)
	for _, p := range validProviders {
		ourProviders[p] = true
	}
	
	// Check summary for a match
	if status.Summary != nil {
		for providerHost, report := range status.Summary {
			if ourProviders[providerHost] {
				// Check if this provider report is healthy
				if report.Healthy {
					isHealthy = true
					capableProvider = providerHost
					
					// Update root last updated if this specific report is newer
					if report.LastUpdated.After(status.LastUpdated) {
						status.LastUpdated = report.LastUpdated
					}
					break
				}
			}
		}
	}

	return status.NZBID, status.Available || isHealthy, status.LastUpdated, capableProvider, nil
}
