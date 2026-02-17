package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type contextKey string

const userContextKey contextKey = "user"

// DeviceFromContext extracts the authenticated device from context
func DeviceFromContext(r *http.Request) (*Device, bool) {
	device, ok := r.Context().Value(userContextKey).(*Device)
	return device, ok
}

// ContextWithDevice adds a device to the request context
func ContextWithDevice(ctx context.Context, device *Device) context.Context {
	return context.WithValue(ctx, userContextKey, device)
}

// AuthMiddleware handles authentication for API routes.
// getAdminUsername and getAdminToken return the configured admin username and single admin token (from config).
func AuthMiddleware(deviceManager *DeviceManager, getAdminUsername, getAdminToken func() string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			adminUsername := ""
			if getAdminUsername != nil {
				adminUsername = getAdminUsername()
			}
			adminToken := ""
			if getAdminToken != nil {
				adminToken = getAdminToken()
			}
			var device *Device
			var err error

			// Try session cookie first
			cookie, err := r.Cookie("auth_session")
			if err == nil && cookie != nil {
				device, err = deviceManager.AuthenticateToken(cookie.Value, adminUsername, adminToken)
				if err == nil {
					ctx := context.WithValue(r.Context(), userContextKey, device)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}

			// Try Authorization header (Bearer token)
			authHeader := r.Header.Get("Authorization")
			if authHeader != "" {
				parts := strings.SplitN(authHeader, " ", 2)
				if len(parts) == 2 && parts[0] == "Bearer" {
					token := parts[1]
					device, err = deviceManager.AuthenticateToken(token, adminUsername, adminToken)
					if err == nil {
						ctx := context.WithValue(r.Context(), userContextKey, device)
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
				}
			}

			// Try query parameter token (for backwards compatibility)
			token := r.URL.Query().Get("token")
			if token != "" {
				device, err = deviceManager.AuthenticateToken(token, adminUsername, adminToken)
				if err == nil {
					ctx := context.WithValue(r.Context(), userContextKey, device)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}

			// No valid authentication found
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "Unauthorized",
			})
		})
	}
}
