package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/availnzb"
	"streamnzb/pkg/config"
	"streamnzb/pkg/indexer/newznab"
	"streamnzb/pkg/initialization"
	"streamnzb/pkg/logger"
	"streamnzb/pkg/nntp"
	"streamnzb/pkg/nzbhydra"
	"streamnzb/pkg/paths"
	"streamnzb/pkg/prowlarr"
	"streamnzb/pkg/tmdb"
	"streamnzb/pkg/triage"
	"streamnzb/pkg/tvdb"
	"streamnzb/pkg/validation"

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
	// Get authenticated device from context (set by auth middleware)
	device, ok := auth.DeviceFromContext(r)
	if !ok {
		// Try cookie fallback
		cookie, err := r.Cookie("auth_session")
		if err == nil && cookie != nil {
			device, err = s.deviceManager.AuthenticateToken(cookie.Value)
			if err != nil {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			ok = true
		}
	}

	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[WS] Upgrade error: %v", err)
		return
	}
	defer conn.Close()

	// Create Client with device
	client := &Client{
		conn:   conn,
		send:   make(chan WSMessage, 256),
		device: device,
		user:   device, // Backwards compatibility alias
	}
	s.AddClient(client)

	// Ensure cleanup
	defer func() {
		s.RemoveClient(client)
		conn.Close()
	}()

	logger.Debug("WS Client connected", "remote", r.RemoteAddr)

	ticker := time.NewTicker(5 * time.Second) // Throttled to reduce load from collectStats/SyncUsage
	defer ticker.Stop()

	// Notify current stats and config immediately
	go func() {
		logger.Trace("WS initial send: collectStats start")
		stats := s.collectStats()
		logger.Trace("WS initial send: collectStats done")
		payload, _ := json.Marshal(stats)
		trySendWS(client, WSMessage{Type: "stats", Payload: payload})
		logger.Trace("WS initial send: stats sent")

		// Send user-specific config
		s.sendConfig(client)

		// Send log history
		s.sendLogHistory(client)

		// Send auth info on connect (replaces /api/auth/check)
		var mustChangePassword bool
		if client.device != nil && client.device.Username == "admin" {
			adminCreds, err := s.deviceManager.GetAdminCredentials()
			if err == nil {
				mustChangePassword = adminCreds.MustChangePassword
			}
		}
		authInfo := map[string]interface{}{
			"authenticated":        true,
			"username":             client.device.Username,
			"must_change_password": mustChangePassword,
		}
		if s.strmServer != nil {
			authInfo["version"] = s.strmServer.Version()
		}
		authPayload, _ := json.Marshal(authInfo)
		trySendWS(client, WSMessage{Type: "auth_info", Payload: authPayload})
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
			case "save_user_configs":
				s.handleSaveUserConfigsWS(conn, client, msg.Payload)
			case "get_users":
				s.handleGetDevicesWS(client)
			case "get_user":
				s.handleGetDeviceWS(client, msg.Payload)
			case "create_user":
				s.handleCreateDeviceWS(client, msg.Payload)
			case "delete_user":
				s.handleDeleteDeviceWS(client, msg.Payload)
			case "regenerate_token":
				s.handleRegenerateTokenWS(client, msg.Payload)
			case "update_password":
				s.handleUpdatePasswordWS(client, msg.Payload)
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

// trySendWS sends msg to the client's channel without blocking. Returns true if sent.
// Use for status/response messages so handlers don't hang when the client is slow.
func trySendWS(client *Client, msg WSMessage) bool {
	select {
	case client.send <- msg:
		return true
	default:
		return false
	}
}

func (s *Server) sendStats(client *Client) {
	stats := s.collectStats()
	payload, _ := json.Marshal(stats)
	trySendWS(client, WSMessage{Type: "stats", Payload: payload})
}

// configPayload is sent to the client; includes env_overrides for admin so the UI can show warnings
type configPayload struct {
	config.Config
	EnvOverrides []string `json:"env_overrides,omitempty"`
}

func (s *Server) sendConfig(client *Client) {
	// Admin always gets global config, devices get merged config
	var cfg config.Config
	if client.device != nil && client.device.Username == "admin" {
		// Admin gets global config
		cfg = *s.config
	} else if client.device != nil {
		// Regular devices get global config merged with their custom filters/sorting (if any)
		cfg = *s.config
		// Only override if device has custom config
		// Use helper functions from auth.go
		if hasCustomFilters(client.device.Filters) {
			cfg.Filters = client.device.Filters
		}
		if hasCustomSorting(client.device.Sorting) {
			cfg.Sorting = client.device.Sorting
		}
	} else {
		cfg = *s.config
	}

	var payload []byte
	if client.device != nil && client.device.Username == "admin" {
		envKeys := config.GetEnvOverrideKeys()
		pl := configPayload{Config: cfg, EnvOverrides: envKeys}
		payload, _ = json.Marshal(pl)
	} else {
		payload, _ = json.Marshal(cfg)
	}
	trySendWS(client, WSMessage{Type: "config", Payload: payload})
}

func (s *Server) sendLogHistory(client *Client) {
	// Fetch history from global logger
	history := logger.GetHistory()
	payload, _ := json.Marshal(history)

	trySendWS(client, WSMessage{Type: "log_history", Payload: payload})
}

func (s *Server) handleSaveConfigWS(conn *websocket.Conn, client *Client, payload json.RawMessage) {
	var newCfg config.Config
	if err := json.Unmarshal(payload, &newCfg); err != nil {
		trySendWS(client, WSMessage{Type: "save_status", Payload: json.RawMessage(`{"status":"error","message":"Invalid config data"}`)})
		return
	}

	// Admin saves to global config, regular devices don't save via this endpoint
	if client.device != nil && client.device.Username == "admin" {
		// Validate settings before saving
		fieldErrors := s.validateConfig(&newCfg)
		if len(fieldErrors) > 0 {
			errorPayload, _ := json.Marshal(map[string]interface{}{
				"status":  "error",
				"message": "Validation failed",
				"errors":  fieldErrors,
			})
			trySendWS(client, WSMessage{Type: "save_status", Payload: errorPayload})
			return
		}

		// Clear legacy fields if migrated indexers were removed
		// Check if migrated Prowlarr indexer was removed
		hasProwlarrMigrated := false
		for _, idx := range newCfg.Indexers {
			if idx.Type == "prowlarr" && (idx.Name == "Prowlarr (Migrated)" || strings.Contains(idx.Name, "Prowlarr")) {
				hasProwlarrMigrated = true
				break
			}
		}
		if !hasProwlarrMigrated {
			// Migrated Prowlarr was removed, clear legacy fields
			newCfg.ProwlarrAPIKey = ""
			newCfg.ProwlarrURL = ""
		}

		// Check if migrated NZBHydra2 indexer was removed
		hasHydraMigrated := false
		for _, idx := range newCfg.Indexers {
			if idx.Type == "nzbhydra" && (idx.Name == "NZBHydra2 (Migrated)" || strings.Contains(idx.Name, "NZBHydra2")) {
				hasHydraMigrated = true
				break
			}
		}
		if !hasHydraMigrated {
			// Migrated NZBHydra2 was removed, clear legacy fields
			newCfg.NZBHydra2APIKey = ""
			newCfg.NZBHydra2URL = ""
		}

		// Set LoadedPath so Save() writes to the same path (env vars are not re-applied on save)
		s.mu.RLock()
		currentLoadedPath := s.config.LoadedPath
		s.mu.RUnlock()
		if currentLoadedPath == "" {
			currentLoadedPath = filepath.Join(paths.GetDataDir(), "config.json")
		}
		newCfg.LoadedPath = currentLoadedPath

		// Update global config
		s.mu.Lock()
		s.config = &newCfg
		s.mu.Unlock()

		if err := s.config.Save(); err != nil {
			trySendWS(client, WSMessage{Type: "save_status", Payload: json.RawMessage([]byte(fmt.Sprintf(`{"status":"error","message":"Failed to save config: %s"}`, err.Error())))})
			return
		}

		// Reload components from the saved config only (no env var reload)
		go func() {
			comp, err := initialization.BuildComponents(&newCfg)
			if err != nil {
				log.Printf("[Reload] BuildComponents failed: %v", err)
				return
			}

			validator := validation.NewChecker(comp.ProviderPools, 24*time.Hour, 10, 5)
			triageService := triage.NewService(
				&comp.Config.Filters,
				comp.Config.Sorting,
			)
			availClient := availnzb.NewClient(comp.Config.AvailNZBURL, comp.Config.AvailNZBAPIKey)
			tmdbClient := tmdb.NewClient(comp.Config.TMDBAPIKey)
			dataDir := filepath.Dir(comp.Config.LoadedPath)
			if dataDir == "" {
				dataDir, _ = os.Getwd()
			}
			tvdbClient := tvdb.NewClient(comp.Config.TVDBAPIKey, dataDir)

			s.Reload(comp.Config, comp.ProviderPools, comp.Indexer, validator, triageService, availClient, tmdbClient, tvdbClient)
			log.Printf("[Reload] Configuration reloaded successfully")
		}()

		// Push updated config back to client
		s.sendConfig(client)
		trySendWS(client, WSMessage{Type: "save_status", Payload: json.RawMessage(`{"status":"success","message":"Configuration saved and reloaded."}`)})
		return
	}

	// Regular devices cannot save via this endpoint
	trySendWS(client, WSMessage{Type: "save_status", Payload: json.RawMessage(`{"status":"error","message":"Only admin can save global configuration"}`)})
}

func (s *Server) handleSaveUserConfigsWS(conn *websocket.Conn, client *Client, payload json.RawMessage) {
	// Only admin can save device configs
	if client.device == nil || client.device.Username != "admin" {
		trySendWS(client, WSMessage{Type: "save_status", Payload: json.RawMessage(`{"status":"error","message":"Only admin can save device configurations"}`)})
		return
	}

	var deviceConfigs map[string]struct {
		Filters config.FilterConfig `json:"filters"`
		Sorting config.SortConfig   `json:"sorting"`
	}
	if err := json.Unmarshal(payload, &deviceConfigs); err != nil {
		trySendWS(client, WSMessage{Type: "save_status", Payload: json.RawMessage(`{"status":"error","message":"Invalid device config data"}`)})
		return
	}

	// Save each device's config
	var errors []string
	for username, deviceConfig := range deviceConfigs {
		if username == "admin" {
			continue // Skip admin
		}

		if err := s.deviceManager.UpdateDeviceFilters(username, deviceConfig.Filters); err != nil {
			errors = append(errors, fmt.Sprintf("Failed to update filters for %s: %v", username, err))
			continue
		}

		if err := s.deviceManager.UpdateDeviceSorting(username, deviceConfig.Sorting); err != nil {
			errors = append(errors, fmt.Sprintf("Failed to update sorting for %s: %v", username, err))
			continue
		}
	}

	if len(errors) > 0 {
		errorPayload, _ := json.Marshal(map[string]interface{}{
			"status":  "error",
			"message": "Some device configs failed to save",
			"errors":  errors,
		})
		trySendWS(client, WSMessage{Type: "save_status", Payload: errorPayload})
		return
	}

	trySendWS(client, WSMessage{Type: "save_status", Payload: json.RawMessage(`{"status":"success","message":"Device configurations saved successfully"}`)})
}

func (s *Server) handleGetDevicesWS(client *Client) {
	// Only admin can get devices list
	if client.device == nil || client.device.Username != "admin" {
		trySendWS(client, WSMessage{Type: "users_response", Payload: json.RawMessage(`{"error":"Only admin can access devices list"}`)})
		return
	}

	devices := s.deviceManager.GetAllDevices()

	// Format devices for response (exclude sensitive data)
	deviceList := make([]map[string]interface{}, 0, len(devices))
	for _, device := range devices {
		deviceList = append(deviceList, map[string]interface{}{
			"username": device.Username,
			"token":    device.Token,
			"filters":  device.Filters,
			"sorting":  device.Sorting,
		})
	}

	deviceListPayload, _ := json.Marshal(deviceList)
	trySendWS(client, WSMessage{Type: "users_response", Payload: deviceListPayload})
}

func (s *Server) handleGetDeviceWS(client *Client, payload json.RawMessage) {
	// Only admin can get user details
	if client.device == nil || client.device.Username != "admin" {
		trySendWS(client, WSMessage{Type: "user_response", Payload: json.RawMessage(`{"error":"Only admin can access user details"}`)})
		return
	}

	var req struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		trySendWS(client, WSMessage{Type: "user_response", Payload: json.RawMessage(`{"error":"Invalid request"}`)})
		return
	}

	device, err := s.deviceManager.GetDevice(req.Username)
	if err != nil {
		errorPayload, _ := json.Marshal(map[string]string{"error": err.Error()})
		trySendWS(client, WSMessage{Type: "user_response", Payload: errorPayload})
		return
	}

	response := map[string]interface{}{
		"username": device.Username,
		"token":    device.Token,
		"filters":  device.Filters,
		"sorting":  device.Sorting,
	}

	respPayload, _ := json.Marshal(response)
	trySendWS(client, WSMessage{Type: "user_response", Payload: respPayload})
}

func (s *Server) handleCreateDeviceWS(client *Client, payload json.RawMessage) {
	// Only admin can create users
	if client.device == nil || client.device.Username != "admin" {
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: json.RawMessage(`{"error":"Only admin can create users"}`)})
		return
	}

	var req struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: json.RawMessage(`{"error":"Invalid request"}`)})
		return
	}

	// Create user without password (empty string)
	device, err := s.deviceManager.CreateDevice(req.Username, "")
	if err != nil {
		errorPayload, _ := json.Marshal(map[string]string{"error": err.Error()})
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: errorPayload})
		return
	}

	response := map[string]interface{}{
		"success": true,
		"user": map[string]interface{}{
			"username": device.Username,
			"token":    device.Token,
		},
	}

	respPayload, _ := json.Marshal(response)
	trySendWS(client, WSMessage{Type: "user_action_response", Payload: respPayload})

	// Broadcast updated devices list to all admin clients
	s.broadcastUsersList()
}

func (s *Server) handleDeleteDeviceWS(client *Client, payload json.RawMessage) {
	// Only admin can delete users
	if client.device == nil || client.device.Username != "admin" {
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: json.RawMessage(`{"error":"Only admin can delete users"}`)})
		return
	}

	var req struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: json.RawMessage(`{"error":"Invalid request"}`)})
		return
	}

	if err := s.deviceManager.DeleteDevice(req.Username); err != nil {
		errorPayload, _ := json.Marshal(map[string]string{"error": err.Error()})
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: errorPayload})
		return
	}

	response := map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Device %s deleted successfully", req.Username),
	}

	respPayload, _ := json.Marshal(response)
	trySendWS(client, WSMessage{Type: "user_action_response", Payload: respPayload})

	// Broadcast updated devices list to all admin clients
	s.broadcastUsersList()
}

func (s *Server) handleRegenerateTokenWS(client *Client, payload json.RawMessage) {
	// Only admin can regenerate tokens
	if client.device == nil || client.device.Username != "admin" {
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: json.RawMessage(`{"error":"Only admin can regenerate tokens"}`)})
		return
	}

	var req struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: json.RawMessage(`{"error":"Invalid request"}`)})
		return
	}

	token, err := s.deviceManager.RegenerateToken(req.Username)
	if err != nil {
		errorPayload, _ := json.Marshal(map[string]string{"error": err.Error()})
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: errorPayload})
		return
	}

	response := map[string]interface{}{
		"success": true,
		"token":   token,
	}

	respPayload, _ := json.Marshal(response)
	trySendWS(client, WSMessage{Type: "user_action_response", Payload: respPayload})

	// Broadcast updated devices list to all admin clients
	s.broadcastUsersList()
}

func (s *Server) handleUpdatePasswordWS(client *Client, payload json.RawMessage) {
	// Only admin can update password
	if client.device == nil || client.device.Username != "admin" {
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: json.RawMessage(`{"error":"Only admin can update password"}`)})
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: json.RawMessage(`{"error":"Invalid request"}`)})
		return
	}

	if req.Username != "admin" {
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: json.RawMessage(`{"error":"Only admin user can change password"}`)})
		return
	}

	if err := s.deviceManager.UpdateUser(req.Username, req.Password); err != nil {
		errorPayload, _ := json.Marshal(map[string]string{"error": err.Error()})
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: errorPayload})
		return
	}

	response := map[string]interface{}{
		"success": true,
		"message": "Password updated successfully",
	}

	respPayload, _ := json.Marshal(response)
	trySendWS(client, WSMessage{Type: "user_action_response", Payload: respPayload})
}

func (s *Server) broadcastUsersList() {
	devices := s.deviceManager.GetAllDevices()

	// Format devices for response
	deviceList := make([]map[string]interface{}, 0, len(devices))
	for _, device := range devices {
		deviceList = append(deviceList, map[string]interface{}{
			"username": device.Username,
			"token":    device.Token,
			"filters":  device.Filters,
			"sorting":  device.Sorting,
		})
	}

	payload, _ := json.Marshal(deviceList)

	// Send to all admin clients
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	for client := range s.clients {
		if client.device != nil && client.device.Username == "admin" {
			select {
			case client.send <- WSMessage{Type: "users_response", Payload: payload}:
			default:
				// Channel full, skip
			}
		}
	}
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
			_, err := nzbhydra.NewClient(cfg.NZBHydra2URL, cfg.NZBHydra2APIKey, "Validation", nil)
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
			indexers, err := prowlarr.GetConfiguredIndexers(cfg.ProwlarrURL, cfg.ProwlarrAPIKey, nil)
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

	// 4. Validate Internal Indexers
	for i, idx := range cfg.Indexers {
		wg.Add(1)
		go func(index int, indexerCfg config.IndexerConfig) {
			defer wg.Done()
			if indexerCfg.URL == "" {
				mu.Lock()
				errors[fmt.Sprintf("indexers.%d.url", index)] = "URL is required"
				mu.Unlock()
				return
			}
			// Use our newznab client to ping
			client := newznab.NewClient(indexerCfg, nil)
			if err := client.Ping(); err != nil {
				mu.Lock()
				errors[fmt.Sprintf("indexers.%d.url", index)] = err.Error()
				mu.Unlock()
			}
		}(i, idx)
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
