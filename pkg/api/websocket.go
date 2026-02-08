package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"streamnzb/pkg/availnzb"
	"streamnzb/pkg/config"
	"streamnzb/pkg/initialization"
	"streamnzb/pkg/logger"
	"streamnzb/pkg/nntp"
	"streamnzb/pkg/nzbhydra"
	"streamnzb/pkg/prowlarr"
	"streamnzb/pkg/tmdb"
	"streamnzb/pkg/triage"
	"streamnzb/pkg/validation"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allowing all origins for development
	},
}

type WSMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[WS] Upgrade error: %v", err)
		return
	}
	defer conn.Close()

	// Create Client
	client := &Client{conn: conn, send: make(chan WSMessage, 256)}
	s.AddClient(client)

	// Ensure cleanup
	defer func() {
		s.RemoveClient(client)
		conn.Close()
	}()

	logger.Debug("WS Client connected", "remote", r.RemoteAddr)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// Notify current stats and config immediately
	// We push to the send channel instead of writing directly to avoid concurrency issues
	// But since this is the only goroutine at this point (before read/write loops), direct write is risky if we start the read loop early?
	// No, we haven't started write loop yet.
	// Actually, let's just push to channel.
	go func() {
		stats := s.collectStats()
		payload, _ := json.Marshal(stats)
		client.send <- WSMessage{Type: "stats", Payload: payload}

		cfgPayload, _ := json.Marshal(s.config)
		client.send <- WSMessage{Type: "config", Payload: cfgPayload}

		// Send log history
		s.sendLogHistory(client)
	}()

	// Read loop (Client -> Server)
	go func() {
		defer func() {
			// When read fails, we close the connection, which triggers the write loop to exit via error or context?
			// Actually, usually we close the done channel or something.
			// Here we just let the write loop detect the closed channel (via RemoveClient)
			// But RemoveClient is called in defer of the main function.
			// We need to signal main function to exit.
			// Best way: Read loop is a separate goroutine. Main function is Write loop.
		}()

		for {
			var msg WSMessage
			if err := conn.ReadJSON(&msg); err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					log.Printf("[WS] Error: %v", err)
				}
				// Signal disconnect
				// We can close the connection here?
				// Or use a channel.
				// Since we can't easily break the main loop from here,
				// we'll rely on the write loop failing or us closing the conn.
				conn.Close()
				return
			}

			// Handle commands
			switch msg.Type {
			case "get_config":
				s.sendConfig(client)
			case "save_config":
				s.handleSaveConfigWS(conn, client, msg.Payload)
			case "close_session":
				s.handleCloseSessionWS(msg.Payload)
			case "restart":
				s.handleRestartWS(conn)
			}
		}
	}()

	// Write loop (Server -> Client)
	for {
		select {
		case <-ticker.C:
			s.sendStats(client)
		case msg, ok := <-client.send:
			if !ok {
				// Channel closed by RemoveClient
				conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := conn.WriteJSON(msg); err != nil {
				return
			}
		}
	}
}

func (s *Server) sendStats(client *Client) {
	stats := s.collectStats()
	payload, _ := json.Marshal(stats)
	select {
	case client.send <- WSMessage{Type: "stats", Payload: payload}:
	default:
	}
}

func (s *Server) sendConfig(client *Client) {
	payload, _ := json.Marshal(s.config)
	select {
	case client.send <- WSMessage{Type: "config", Payload: payload}:
	default:
	}
}

func (s *Server) sendLogHistory(client *Client) {
	// Fetch history from global logger
	history := logger.GetHistory()
	payload, _ := json.Marshal(history)

	select {
	case client.send <- WSMessage{Type: "log_history", Payload: payload}:
	default:
	}
}

func (s *Server) handleSaveConfigWS(conn *websocket.Conn, client *Client, payload json.RawMessage) {
	var newCfg config.Config
	if err := json.Unmarshal(payload, &newCfg); err != nil {
		client.send <- WSMessage{Type: "save_status", Payload: json.RawMessage(`{"status":"error","message":"Invalid config data"}`)}
		return
	}

	// Validate settings before saving
	fieldErrors := s.validateConfig(&newCfg)
	if len(fieldErrors) > 0 {
		errorPayload, _ := json.Marshal(map[string]interface{}{
			"status":  "error",
			"message": "Validation failed",
			"errors":  fieldErrors,
		})
		client.send <- WSMessage{Type: "save_status", Payload: errorPayload}
		return
	}

	loadedPath := s.config.LoadedPath
	availURL := s.config.AvailNZBURL
	availKey := s.config.AvailNZBAPIKey
	tmdbKey := s.config.TMDBAPIKey

	*s.config = newCfg

	s.config.LoadedPath = loadedPath
	s.config.AvailNZBURL = availURL
	s.config.AvailNZBAPIKey = availKey
	s.config.TMDBAPIKey = tmdbKey

	// Apply Log Level immediately
	logger.SetLevel(s.config.LogLevel)

	if err := s.config.Save(); err != nil {
		client.send <- WSMessage{Type: "save_status", Payload: json.RawMessage([]byte(fmt.Sprintf(`{"status":"error","message":"%s"}`, err.Error())))}
		return
	}

	// push updated config back to client so UI is in sync
	s.sendConfig(client)

	// Trigger Hot-Reload
	go func() {
		// Wait a bit for the save to settle and response to be sent
		time.Sleep(100 * time.Millisecond)

		comp, err := initialization.BuildComponents(s.config)
		if err != nil {
			log.Printf("[Reload] Failed to build components: %v", err)
			return
		}

		// Build dependencies for Stremio Server reload
		validator := validation.NewChecker(comp.ProviderPools, 24*time.Hour, 10, 5)
		triageService := triage.NewService(5, &comp.Config.Filters)
		availClient := availnzb.NewClient(comp.Config.AvailNZBURL, comp.Config.AvailNZBAPIKey)
		tmdbClient := tmdb.NewClient(comp.Config.TMDBAPIKey)

		s.Reload(comp.Config, comp.ProviderPools, comp.Indexer, validator, triageService, availClient, tmdbClient)
		log.Printf("[Reload] Configuration reloaded successfully")
	}()

	client.send <- WSMessage{Type: "save_status", Payload: json.RawMessage(`{"status":"success","message":"Configuration saved and reloaded."}`)}
}

// validateConfig checks connectivity for all components and returns a map of field errors
func (s *Server) validateConfig(cfg *config.Config) map[string]string {
	errors := make(map[string]string)
	var mu sync.Mutex
	var wg sync.WaitGroup

	// 1. Validate NNTP Providers
	for i, p := range cfg.Providers {
		wg.Add(1)
		go func(idx int, provider config.Provider) {
			defer wg.Done()
			// Basic format check
			if provider.Host == "" {
				mu.Lock()
				errors[fmt.Sprintf("providers.%d.host", idx)] = "Host is required"
				mu.Unlock()
				return
			}
			pool := nntp.NewClientPool(provider.Host, provider.Port, provider.UseSSL, provider.Username, provider.Password, 1)
			if err := pool.Validate(); err != nil {
				mu.Lock()
				errors[fmt.Sprintf("providers.%d.host", idx)] = err.Error()
				mu.Unlock()
			}
		}(i, p)
	}

	// 2. Validate NZBHydra2
	if cfg.NZBHydra2URL != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := nzbhydra.NewClient(cfg.NZBHydra2URL, cfg.NZBHydra2APIKey)
			if err != nil {
				mu.Lock()
				errStr := err.Error()
				if strings.Contains(strings.ToLower(errStr), "api key") || strings.Contains(strings.ToLower(errStr), "hydra error") {
					errors["nzbhydra_api_key"] = errStr
				} else {
					errors["nzbhydra_url"] = errStr
				}
				mu.Unlock()
			}
		}()
	}

	// 3. Validate Prowlarr
	if cfg.ProwlarrURL != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Fetch indexers to verify connectivity AND API Key
			indexers, err := prowlarr.GetConfiguredIndexers(cfg.ProwlarrURL, cfg.ProwlarrAPIKey)
			if err != nil {
				mu.Lock()
				errStr := err.Error()
				if strings.Contains(errStr, "401") || strings.Contains(errStr, "403") {
					errors["prowlarr_api_key"] = "Invalid Prowlarr API key"
				} else {
					errors["prowlarr_url"] = errStr
				}
				mu.Unlock()
			} else if cfg.ProwlarrAPIKey != "" && len(indexers) == 0 {
				mu.Lock()
				errors["prowlarr_api_key"] = "Success, but found no Usenet indexers in Prowlarr"
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	return errors
}

func (s *Server) handleCloseSessionWS(payload json.RawMessage) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}
	log.Printf("[WS] Closing session: %s", req.ID)
	s.sessionMgr.DeleteSession(req.ID)
}

func (s *Server) handleRestartWS(conn *websocket.Conn) {
	go func() {
		time.Sleep(500 * time.Millisecond)
		exe, _ := os.Executable()
		cmd := exec.Command(exe)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.Start()
		os.Exit(0)
	}()
}
