package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"sync"
)

// Device represents a device account
type Device struct {
	Username string              `json:"username"`
	Token    string              `json:"token"` // SHA256 token for API access
	Filters  config.FilterConfig `json:"filters"`
	Sorting  config.SortConfig   `json:"sorting"`
	// PasswordHash and MustChangePassword are not stored for regular devices
	// They are only used for admin (stored separately in AdminCredentials)
}

// DeviceManager handles device storage and authentication.
// Admin credentials and single admin token are stored in config, not state.
type DeviceManager struct {
	mu      sync.RWMutex
	devices map[string]*Device // username -> Device (excludes admin)
	manager *persistence.StateManager
}

var globalDeviceManager *DeviceManager
var deviceManagerMu sync.Mutex

// GetDeviceManager returns the global device manager
func GetDeviceManager(dataDir string) (*DeviceManager, error) {
	deviceManagerMu.Lock()
	defer deviceManagerMu.Unlock()

	if globalDeviceManager != nil {
		return globalDeviceManager, nil
	}

	manager, err := persistence.GetManager(dataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get persistence manager: %w", err)
	}

	dm := &DeviceManager{
		devices: make(map[string]*Device),
		manager: manager,
	}

	if err := dm.load(); err != nil {
		return nil, fmt.Errorf("failed to load devices: %w", err)
	}

	globalDeviceManager = dm
	return dm, nil
}

// load loads devices from persistent storage
func (dm *DeviceManager) load() error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	// Try loading from "devices" first, fallback to "users" for migration
	var devices map[string]*Device
	found, err := dm.manager.Get("devices", &devices)
	if err != nil {
		return err
	}

	if !found {
		// Try migration from "users" to "devices"
		var users map[string]*Device
		if found, err := dm.manager.Get("users", &users); found && err == nil {
			devices = users
			// Save as "devices" for future use
			dm.manager.Set("devices", devices)
			logger.Info("Migrated users to devices in state.json")
		}
	}

	if devices != nil {
		dm.devices = devices
		// Remove legacy "admin" from devices map if present (admin is now in config)
		if _, exists := dm.devices["admin"]; exists {
			delete(dm.devices, "admin")
			dm.saveLocked()
			logger.Info("Removed legacy admin from devices (admin is in config)")
		}
	} else {
		dm.devices = make(map[string]*Device)
	}

	return nil
}

// saveLocked saves devices to persistent storage (caller must hold write lock, excludes admin)
func (dm *DeviceManager) saveLocked() error {
	return dm.manager.Set("devices", dm.devices)
}

// HashPassword creates a SHA256 hash of a password (exported for API password updates).
func HashPassword(password string) string {
	hash := sha256.Sum256([]byte(password))
	return hex.EncodeToString(hash[:])
}

// GenerateToken generates a random SHA256 token (exported for migration/config bootstrap).
func GenerateToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	hash := sha256.Sum256(bytes)
	return hex.EncodeToString(hash[:]), nil
}

// Authenticate validates username and password, returns device if valid.
// adminUsername, adminPasswordHash, adminToken come from config (admin uses a single persistent token).
// When loginUsername == adminUsername, validates password against adminPasswordHash and returns Device with Token set to adminToken.
func (dm *DeviceManager) Authenticate(loginUsername, password, adminUsername, adminPasswordHash, adminToken string) (*Device, error) {
	if adminUsername == "" {
		adminUsername = "admin"
	}

	if loginUsername == adminUsername {
		if adminPasswordHash == "" || adminToken == "" {
			return nil, fmt.Errorf("invalid credentials")
		}
		passwordHash := HashPassword(password)
		if passwordHash != adminPasswordHash {
			return nil, fmt.Errorf("invalid credentials")
		}
		return &Device{
			Username: adminUsername,
			Token:    adminToken,
			Filters:  config.FilterConfig{},
			Sorting:  config.SortConfig{},
		}, nil
	}

	return nil, fmt.Errorf("invalid credentials")
}

// AuthenticateToken validates a token and returns the device.
// adminUsername and adminToken come from config; if token == adminToken, returns admin device.
func (dm *DeviceManager) AuthenticateToken(token string, adminUsername, adminToken string) (*Device, error) {
	if adminUsername == "" {
		adminUsername = "admin"
	}

	if adminToken != "" && token == adminToken {
		return &Device{
			Username: adminUsername,
			Token:    adminToken,
			Filters:  config.FilterConfig{},
			Sorting:  config.SortConfig{},
		}, nil
	}

	dm.mu.RLock()
	defer dm.mu.RUnlock()
	for _, device := range dm.devices {
		if device.Token == token {
			return device, nil
		}
	}

	return nil, fmt.Errorf("invalid token")
}

// GetDevice retrieves a device by username. Admin (dashboard login) is not a regular device; pass adminUsername to reject it.
func (dm *DeviceManager) GetDevice(username string, adminUsername string) (*Device, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	if adminUsername == "" {
		adminUsername = "admin"
	}
	if username == adminUsername {
		return nil, fmt.Errorf("admin is not a regular device")
	}

	device, exists := dm.devices[username]
	if !exists {
		return nil, fmt.Errorf("device not found")
	}

	return device, nil
}

// GetUser is an alias for GetDevice for backwards compatibility
func (dm *DeviceManager) GetUser(username string, adminUsername string) (*Device, error) {
	return dm.GetDevice(username, adminUsername)
}

// GetAllDevices returns all devices (without password hashes for security, excludes admin)
func (dm *DeviceManager) GetAllDevices() []Device {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	devices := make([]Device, 0, len(dm.devices))
	for _, device := range dm.devices {
		// Skip dashboard admin if it somehow got in there (admin is stored separately)
		if device.Username == "admin" {
			continue
		}
		// Return copy (Device struct no longer has PasswordHash or MustChangePassword)
		devices = append(devices, Device{
			Username: device.Username,
			Token:    device.Token,
			Filters:  device.Filters,
			Sorting:  device.Sorting,
		})
	}

	return devices
}

// GetAllUsers is an alias for GetAllDevices for backwards compatibility
func (dm *DeviceManager) GetAllUsers() []Device {
	return dm.GetAllDevices()
}

// CreateDevice creates a new device (password is optional). adminUsername is the dashboard admin name; cannot create a device with that name.
func (dm *DeviceManager) CreateDevice(username, password string, adminUsername string) (*Device, error) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	if adminUsername == "" {
		adminUsername = "admin"
	}
	if username == adminUsername {
		return nil, fmt.Errorf("cannot create admin device via this method")
	}

	if _, exists := dm.devices[username]; exists {
		return nil, fmt.Errorf("device already exists")
	}

	token, err := GenerateToken()
	if err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	device := &Device{
		Username: username,
		Token:    token,
		Filters:  config.DefaultFilterConfig(),
		Sorting:  config.DefaultSortConfig(),
	}

	dm.devices[username] = device

	if err := dm.saveLocked(); err != nil {
		delete(dm.devices, username)
		return nil, fmt.Errorf("failed to save device: %w", err)
	}

	logger.Info("Created device", "username", username)
	return device, nil
}

// CreateUser is an alias for CreateDevice for backwards compatibility
func (dm *DeviceManager) CreateUser(username, password string, adminUsername string) (*Device, error) {
	return dm.CreateDevice(username, password, adminUsername)
}

// UpdateUser updates dashboard admin password. adminUsername is the configured admin name.
// Admin password is stored in config; callers must update config and save instead of using this for admin.
func (dm *DeviceManager) UpdateUser(username, newPassword string, adminUsername string) error {
	if adminUsername == "" {
		adminUsername = "admin"
	}
	if username == adminUsername {
		return fmt.Errorf("admin password is managed via config; use dashboard to change")
	}
	return fmt.Errorf("only admin password can be updated")
}

// RegenerateToken generates a new token for a device
func (dm *DeviceManager) RegenerateToken(username string) (string, error) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	device, exists := dm.devices[username]
	if !exists {
		return "", fmt.Errorf("device not found")
	}

	token, err := GenerateToken()
	if err != nil {
		return "", fmt.Errorf("failed to generate token: %w", err)
	}

	device.Token = token

	if err := dm.saveLocked(); err != nil {
		return "", fmt.Errorf("failed to save device: %w", err)
	}

	logger.Info("Regenerated token for device", "username", username)
	return token, nil
}

// DeleteDevice removes a device
func (dm *DeviceManager) DeleteDevice(username string) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	if _, exists := dm.devices[username]; !exists {
		return fmt.Errorf("device not found")
	}

	delete(dm.devices, username)

	if err := dm.saveLocked(); err != nil {
		return fmt.Errorf("failed to save device: %w", err)
	}

	logger.Info("Deleted device", "username", username)
	return nil
}

// DeleteUser is an alias for DeleteDevice for backwards compatibility
func (dm *DeviceManager) DeleteUser(username string) error {
	return dm.DeleteDevice(username)
}

// UpdateDeviceFilters updates a device's filter configuration
func (dm *DeviceManager) UpdateDeviceFilters(username string, filters config.FilterConfig) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	device, exists := dm.devices[username]
	if !exists {
		return fmt.Errorf("device not found")
	}

	device.Filters = filters

	if err := dm.saveLocked(); err != nil {
		return fmt.Errorf("failed to save device filters: %w", err)
	}

	return nil
}

// UpdateUserFilters is an alias for UpdateDeviceFilters for backwards compatibility
func (dm *DeviceManager) UpdateUserFilters(username string, filters config.FilterConfig) error {
	return dm.UpdateDeviceFilters(username, filters)
}

// UpdateDeviceSorting updates a device's sorting configuration
func (dm *DeviceManager) UpdateDeviceSorting(username string, sorting config.SortConfig) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	device, exists := dm.devices[username]
	if !exists {
		return fmt.Errorf("device not found")
	}

	device.Sorting = sorting

	if err := dm.saveLocked(); err != nil {
		return fmt.Errorf("failed to save device sorting: %w", err)
	}

	return nil
}

// UpdateUserSorting is an alias for UpdateDeviceSorting for backwards compatibility
func (dm *DeviceManager) UpdateUserSorting(username string, sorting config.SortConfig) error {
	return dm.UpdateDeviceSorting(username, sorting)
}

// GetDeviceConfig returns a device's filter and sorting config
func (dm *DeviceManager) GetDeviceConfig(username string) (config.FilterConfig, config.SortConfig, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	device, exists := dm.devices[username]
	if !exists {
		return config.FilterConfig{}, config.SortConfig{}, fmt.Errorf("device not found")
	}

	return device.Filters, device.Sorting, nil
}

// GetUserConfig is an alias for GetDeviceConfig for backwards compatibility
func (dm *DeviceManager) GetUserConfig(username string) (config.FilterConfig, config.SortConfig, error) {
	return dm.GetDeviceConfig(username)
}
