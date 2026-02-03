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
}

type StatusResponse struct {
	NZBID   string            `json:"nzb_id"`
	Summary map[string]string `json:"summary"`
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

func (c *Client) ReportAvailability(nzbID string, providerURL string, status bool) error {
	if c.APIKey == "" {
		return nil // Skip if no API key
	}

	reqBody, err := json.Marshal(ReportRequest{
		NZBID:       nzbID,
		ProviderURL: providerURL,
		Status:      status,
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
		return fmt.Errorf("unexpected status code: %d (body: %s)", resp.StatusCode, string(reqBody))
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
