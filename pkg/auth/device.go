package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"streamnzb/pkg/config"
	"streamnzb/pkg/logger"
	"streamnzb/pkg/persistence"
	"sync"
)

// Device represents a device account
type Device struct {
	Username string                `json:"username"`
	Token    string                `json:"token"` // SHA256 token for API access
	Filters  config.FilterConfig   `json:"filters"`
	Sorting  config.SortConfig      `json:"sorting"`
	// PasswordHash and MustChangePassword are not stored for regular devices
	// They are only used for admin (stored separately in AdminCredentials)
}

// AdminCredentials stores admin login credentials separately
type AdminCredentials struct {
	PasswordHash      string `json:"password_hash"`
	MustChangePassword bool   `json:"must_change_password"`
}

// DeviceManager handles device storage and authentication
type DeviceManager struct {
	mu            sync.RWMutex
	devices       map[string]*Device // username -> Device (excludes admin)
	admin         *AdminCredentials // Admin credentials stored separately
	adminSessions map[string]bool   // Active admin session tokens
	manager       *persistence.StateManager
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
		devices:       make(map[string]*Device),
		adminSessions: make(map[string]bool),
		manager:       manager,
	}

	if err := dm.load(); err != nil {
		return nil, fmt.Errorf("failed to load devices: %w", err)
	}

	globalDeviceManager = dm
	return dm, nil
}

// GetUserManager is an alias for GetDeviceManager for backwards compatibility
func GetUserManager(dataDir string) (*DeviceManager, error) {
	return GetDeviceManager(dataDir)
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
		// Remove admin from devices map if it exists (migration)
		if _, exists := dm.devices["admin"]; exists {
			// Migrate admin to separate storage
			// Note: Old Device struct had PasswordHash and MustChangePassword, but we removed them
			// For migration, we'll create default admin credentials (password reset required)
			admin := AdminCredentials{
				PasswordHash:      hashPassword("admin"), // Reset to default
				MustChangePassword: true,                 // Force password change
			}
			dm.manager.Set("admin", admin)
			delete(dm.devices, "admin")
			dm.saveLocked() // Save without admin
			logger.Info("Migrated admin from devices to separate storage (password reset required)")
		}
	} else {
		dm.devices = make(map[string]*Device)
	}

	// Load admin credentials separately
	var admin AdminCredentials
	adminFound, err := dm.manager.Get("admin", &admin)
	if err != nil {
		return err
	}

	if !adminFound {
		// Create default admin credentials
		admin = AdminCredentials{
			PasswordHash:      hashPassword("admin"),
			MustChangePassword: true,
		}
		if err := dm.manager.Set("admin", admin); err != nil {
			return fmt.Errorf("failed to save default admin credentials: %w", err)
		}
		logger.Info("Created default admin credentials", "password", "admin")
	}

	dm.admin = &admin

	// Load admin sessions
	var adminSessions map[string]bool
	if found, _ := dm.manager.Get("admin_sessions", &adminSessions); found {
		dm.adminSessions = adminSessions
	} else {
		dm.adminSessions = make(map[string]bool)
	}

	return nil
}

// save saves devices to persistent storage (excludes admin)
func (dm *DeviceManager) save() error {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	return dm.manager.Set("devices", dm.devices)
}

// saveLocked saves devices to persistent storage (caller must hold write lock, excludes admin)
func (dm *DeviceManager) saveLocked() error {
	return dm.manager.Set("devices", dm.devices)
}

// saveAdmin saves admin credentials separately
func (dm *DeviceManager) saveAdmin() error {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	if dm.admin == nil {
		return fmt.Errorf("admin credentials not initialized")
	}
	return dm.manager.Set("admin", dm.admin)
}

// saveAdminLocked saves admin credentials to persistent storage (caller must hold write lock)
func (dm *DeviceManager) saveAdminLocked() error {
	if dm.admin == nil {
		return fmt.Errorf("admin credentials not initialized")
	}
	return dm.manager.Set("admin", dm.admin)
}

// hashPassword creates a SHA256 hash of a password
func hashPassword(password string) string {
	hash := sha256.Sum256([]byte(password))
	return hex.EncodeToString(hash[:])
}

// generateToken generates a random SHA256 token
func generateToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	hash := sha256.Sum256(bytes)
	return hex.EncodeToString(hash[:]), nil
}

// Authenticate validates username and password, returns device if valid
// Admin is handled separately and gets a session token
func (dm *DeviceManager) Authenticate(username, password string) (*Device, error) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	// Handle admin separately
	if username == "admin" {
		if dm.admin == nil {
			return nil, fmt.Errorf("invalid credentials")
		}

		passwordHash := hashPassword(password)
		if dm.admin.PasswordHash != passwordHash {
			return nil, fmt.Errorf("invalid credentials")
		}

		// Generate a session token for admin
		token, err := generateToken()
		if err != nil {
			return nil, fmt.Errorf("failed to generate session token: %w", err)
		}

		// Store admin session token
		if dm.adminSessions == nil {
			dm.adminSessions = make(map[string]bool)
		}
		dm.adminSessions[token] = true
		if err := dm.manager.Set("admin_sessions", dm.adminSessions); err != nil {
			return nil, fmt.Errorf("failed to save admin session: %w", err)
		}

		// Return a temporary Device object for admin (not stored in devices map)
		return &Device{
			Username: "admin",
			Token:    token,
			Filters:  config.FilterConfig{},
			Sorting:  config.SortConfig{},
		}, nil
	}

	// Regular devices don't have passwords
	return nil, fmt.Errorf("invalid credentials")
}

// AuthenticateToken validates a token and returns the device
// Admin tokens are stored separately in admin_sessions
func (dm *DeviceManager) AuthenticateToken(token string) (*Device, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	// Check admin sessions first
	var adminSessions map[string]bool
	if found, _ := dm.manager.Get("admin_sessions", &adminSessions); found {
		if adminSessions[token] {
			// Valid admin session token
			if dm.admin == nil {
				return nil, fmt.Errorf("admin credentials not initialized")
			}
			return &Device{
				Username: "admin",
				Token:    token,
				Filters:  config.FilterConfig{},
				Sorting:  config.SortConfig{},
			}, nil
		}
	}

	// Check regular devices
	for _, device := range dm.devices {
		if device.Token == token {
			return device, nil
		}
	}

	return nil, fmt.Errorf("invalid token")
}

// GetDevice retrieves a device by username (admin returns error, use GetAdminCredentials instead)
func (dm *DeviceManager) GetDevice(username string) (*Device, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	if username == "admin" {
		return nil, fmt.Errorf("admin is not a regular device")
	}

	device, exists := dm.devices[username]
	if !exists {
		return nil, fmt.Errorf("device not found")
	}

	return device, nil
}

// GetUser is an alias for GetDevice for backwards compatibility
func (dm *DeviceManager) GetUser(username string) (*Device, error) {
	return dm.GetDevice(username)
}

// GetAdminCredentials returns admin credentials
func (dm *DeviceManager) GetAdminCredentials() (*AdminCredentials, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	if dm.admin == nil {
		return nil, fmt.Errorf("admin credentials not initialized")
	}

	return dm.admin, nil
}

// GetAllDevices returns all devices (without password hashes for security, excludes admin)
func (dm *DeviceManager) GetAllDevices() []Device {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	devices := make([]Device, 0, len(dm.devices))
	for _, device := range dm.devices {
		// Skip admin if it somehow got in there
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

// CreateDevice creates a new device (password is optional, admin cannot be created this way)
func (dm *DeviceManager) CreateDevice(username, password string) (*Device, error) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	if username == "admin" {
		return nil, fmt.Errorf("cannot create admin device via this method")
	}

	if _, exists := dm.devices[username]; exists {
		return nil, fmt.Errorf("device already exists")
	}

	token, err := generateToken()
	if err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	device := &Device{
		Username: username,
		Token:    token,
		Filters:  config.FilterConfig{}, // Default empty filters (device inherits global defaults)
		Sorting:  config.SortConfig{},   // Default empty sorting (device inherits global defaults)
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
func (dm *DeviceManager) CreateUser(username, password string) (*Device, error) {
	return dm.CreateDevice(username, password)
}

// UpdateUser updates user password (only for admin)
func (dm *DeviceManager) UpdateUser(username, newPassword string) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	if username != "admin" {
		return fmt.Errorf("only admin password can be updated")
	}

	if dm.admin == nil {
		return fmt.Errorf("admin credentials not initialized")
	}

	if newPassword != "" {
		dm.admin.PasswordHash = hashPassword(newPassword)
		dm.admin.MustChangePassword = false // Clear the flag when password is changed
	}

	if err := dm.saveAdminLocked(); err != nil {
		return fmt.Errorf("failed to save admin credentials: %w", err)
	}

	logger.Info("Updated admin password")
	return nil
}

// RegenerateToken generates a new token for a device
func (dm *DeviceManager) RegenerateToken(username string) (string, error) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	device, exists := dm.devices[username]
	if !exists {
		return "", fmt.Errorf("device not found")
	}

	token, err := generateToken()
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
