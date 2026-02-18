package api

import (
	"encoding/json"
	"net/http"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
)

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Success            bool   `json:"success"`
	Token              string `json:"token,omitempty"`
	User               string `json:"user,omitempty"`
	MustChangePassword bool   `json:"must_change_password,omitempty"`
	Error              string `json:"error,omitempty"`
}

// handleLogin handles user login
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	adminUsername := s.config.GetAdminUsername()
	if req.Username != adminUsername {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(LoginResponse{
			Success: false,
			Error:   "Invalid credentials",
		})
		return
	}

	device, err := s.deviceManager.Authenticate(req.Username, req.Password, adminUsername, s.config.AdminPasswordHash, s.config.AdminToken)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(LoginResponse{
			Success: false,
			Error:   "Invalid credentials",
		})
		return
	}

	// Set session cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "auth_session",
		Value:    device.Token,
		Path:     "/",
		HttpOnly: true,
		Secure:   false, // Set to true in production with HTTPS
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400 * 7, // 7 days
	})

	var mustChangePassword bool
	if device.Username == s.config.GetAdminUsername() {
		mustChangePassword = s.config.AdminMustChangePassword
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(LoginResponse{
		Success:            true,
		Token:              device.Token, // Empty for admin
		User:               device.Username,
		MustChangePassword: mustChangePassword,
	})
}

// handleInfo returns app info (version) - public, no auth
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	version := "dev"
	if s.strmServer != nil {
		version = s.strmServer.Version()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"version": version})
}

// handleAuthCheck checks if user is authenticated
func (s *Server) handleAuthCheck(w http.ResponseWriter, r *http.Request) {
	device, ok := auth.DeviceFromContext(r)
	if !ok {
		// Try cookie
		cookie, err := r.Cookie("auth_session")
		if err == nil && cookie != nil {
			device, err = s.deviceManager.AuthenticateToken(cookie.Value, s.config.GetAdminUsername(), s.config.AdminToken)
			if err == nil {
				ok = true
			}
		}
	}

	if ok {
		var mustChangePassword bool
		if device.Username == s.config.GetAdminUsername() {
			mustChangePassword = s.config.AdminMustChangePassword
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"authenticated":       true,
			"username":             device.Username,
			"must_change_password": mustChangePassword,
		})
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"authenticated": false,
		})
	}
}

// REST endpoints removed - all device management and config operations now use WebSocket

// hasCustomFilters checks if user has custom filter configuration
// Returns true only if user has explicitly set non-default values
// Note: Empty FilterConfig structs have zero values (false, 0, "", nil slices)
// We only consider it custom if at least one field is explicitly set to a non-zero value
func hasCustomFilters(filters config.FilterConfig) bool {
	// Check if any filter field is explicitly set (non-empty/non-zero)
	return len(filters.AllowedQualities) > 0 ||
		len(filters.BlockedQualities) > 0 ||
		filters.MinResolution != "" ||
		filters.MaxResolution != "" ||
		len(filters.AllowedCodecs) > 0 ||
		len(filters.BlockedCodecs) > 0 ||
		len(filters.RequiredAudio) > 0 ||
		len(filters.AllowedAudio) > 0 ||
		filters.MinChannels != "" ||
		filters.RequireHDR ||
		len(filters.AllowedHDR) > 0 ||
		len(filters.BlockedHDR) > 0 ||
		filters.BlockSDR ||
		len(filters.RequiredLanguages) > 0 ||
		len(filters.AllowedLanguages) > 0 ||
		filters.BlockDubbed ||
		filters.BlockCam ||
		filters.RequireProper ||
		// AllowRepack: We can't distinguish between "not set" (false) and "explicitly set to false"
		// So we'll only mark as custom if AllowRepack is false AND at least one other field is set
		(!filters.AllowRepack && hasAnyOtherFilterSet(filters)) ||
		filters.BlockHardcoded ||
		filters.MinBitDepth != "" ||
		filters.MinSizeGB > 0 ||
		filters.MaxSizeGB > 0 ||
		len(filters.BlockedGroups) > 0
}

// hasAnyOtherFilterSet checks if any filter field (except AllowRepack) is set
func hasAnyOtherFilterSet(filters config.FilterConfig) bool {
	return len(filters.AllowedQualities) > 0 ||
		len(filters.BlockedQualities) > 0 ||
		filters.MinResolution != "" ||
		filters.MaxResolution != "" ||
		len(filters.AllowedCodecs) > 0 ||
		len(filters.BlockedCodecs) > 0 ||
		len(filters.RequiredAudio) > 0 ||
		len(filters.AllowedAudio) > 0 ||
		filters.MinChannels != "" ||
		filters.RequireHDR ||
		len(filters.AllowedHDR) > 0 ||
		len(filters.BlockedHDR) > 0 ||
		filters.BlockSDR ||
		len(filters.RequiredLanguages) > 0 ||
		len(filters.AllowedLanguages) > 0 ||
		filters.BlockDubbed ||
		filters.BlockCam ||
		filters.RequireProper ||
		filters.BlockHardcoded ||
		filters.MinBitDepth != "" ||
		filters.MinSizeGB > 0 ||
		filters.MaxSizeGB > 0 ||
		len(filters.BlockedGroups) > 0
}

// hasCustomSorting checks if user has custom sorting configuration
func hasCustomSorting(sorting config.SortConfig) bool {
	// Check if any sorting field is set (non-empty/non-zero)
	return len(sorting.ResolutionWeights) > 0 ||
		len(sorting.CodecWeights) > 0 ||
		len(sorting.AudioWeights) > 0 ||
		len(sorting.QualityWeights) > 0 ||
		len(sorting.VisualTagWeights) > 0 ||
		sorting.GrabWeight != 0 ||
		sorting.AgeWeight != 0 ||
		len(sorting.PreferredGroups) > 0 ||
		len(sorting.PreferredLanguages) > 0
}

// REST endpoint removed - config saving now uses WebSocket
