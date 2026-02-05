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
	"streamnzb/pkg/nntp"
	"streamnzb/pkg/nzbhydra"
	"streamnzb/pkg/prowlarr"
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

	log.Printf("[WS] Client connected: %s", r.RemoteAddr)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// Notify current stats and config immediately
	s.sendStats(conn)
	s.sendConfig(conn)

	// Command output channel for the write loop
	responses := make(chan WSMessage, 10)

	// Read loop (Client -> Server)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			var msg WSMessage
			if err := conn.ReadJSON(&msg); err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					log.Printf("[WS] Error: %v", err)
				}
				return
			}

			// Handle commands
			switch msg.Type {
			case "get_config":
				s.sendConfig(conn)
			case "save_config":
				s.handleSaveConfigWS(conn, msg.Payload, responses)
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
			s.sendStats(conn)
		case resp := <-responses:
			if err := conn.WriteJSON(resp); err != nil {
				return
			}
		case <-done:
			log.Printf("[WS] Client disconnected: %s", r.RemoteAddr)
			return
		}
	}
}

func (s *Server) sendStats(conn *websocket.Conn) {
	stats := s.collectStats()
	payload, _ := json.Marshal(stats)
	conn.WriteJSON(WSMessage{Type: "stats", Payload: payload})
}

func (s *Server) sendConfig(conn *websocket.Conn) {
	payload, _ := json.Marshal(s.config)
	conn.WriteJSON(WSMessage{Type: "config", Payload: payload})
}

func (s *Server) handleSaveConfigWS(conn *websocket.Conn, payload json.RawMessage, responses chan WSMessage) {
	var newCfg config.Config
	if err := json.Unmarshal(payload, &newCfg); err != nil {
		responses <- WSMessage{Type: "save_status", Payload: json.RawMessage(`{"status":"error","message":"Invalid config data"}`)}
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
		responses <- WSMessage{Type: "save_status", Payload: errorPayload}
		return
	}

	*s.config = newCfg
	if err := s.config.SaveFile("config.json"); err != nil {
		responses <- WSMessage{Type: "save_status", Payload: json.RawMessage([]byte(fmt.Sprintf(`{"status":"error","message":"%s"}`, err.Error())))}
		return
	}

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
		triageService := triage.NewService(5)
		availClient := availnzb.NewClient(comp.Config.AvailNZBURL, comp.Config.AvailNZBAPIKey)

		s.Reload(comp.Config, comp.ProviderPools, comp.Indexer, validator, triageService, availClient)
		log.Printf("[Reload] Configuration reloaded successfully")
	}()

	responses <- WSMessage{Type: "save_status", Payload: json.RawMessage(`{"status":"success","message":"Configuration saved and reloaded."}`)}
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
