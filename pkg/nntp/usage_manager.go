package nntp

import (
	"streamnzb/pkg/logger"
	"streamnzb/pkg/persistence"
	"sync"
)

// ProviderUsageData stores cumulative usage for a specific NNTP provider
type ProviderUsageData struct {
	TotalBytes int64 `json:"total_bytes"`
}

// ProviderUsageManager handles persistent storage of provider usage via StateManager
type ProviderUsageManager struct {
	state *persistence.StateManager
	data  map[string]*ProviderUsageData
	mu    sync.RWMutex
}

var providerManager *ProviderUsageManager
var providerManagerMu sync.Mutex

// GetProviderUsageManager returns a provider usage manager using the provided StateManager
func GetProviderUsageManager(sm *persistence.StateManager) (*ProviderUsageManager, error) {
	providerManagerMu.Lock()
	defer providerManagerMu.Unlock()

	if providerManager != nil {
		return providerManager, nil
	}

	m := &ProviderUsageManager{
		state: sm,
		data:  make(map[string]*ProviderUsageData),
	}

	if err := m.load(); err != nil {
		return nil, err
	}

	providerManager = m
	return m, nil
}

func (m *ProviderUsageManager) load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, err := m.state.Get("provider_usage", &m.data)
	return err
}

func (m *ProviderUsageManager) save() error {
	return m.state.Set("provider_usage", m.data)
}

// GetUsage returns the usage for a provider, creating an entry if needed
func (m *ProviderUsageManager) GetUsage(name string) *ProviderUsageData {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, ok := m.data[name]
	if !ok {
		data = &ProviderUsageData{}
		m.data[name] = data
	}
	return data
}

// IncrementBytes increments the total bytes for a provider and persists
func (m *ProviderUsageManager) IncrementBytes(name string, delta int64) {
	m.mu.Lock()
	data, ok := m.data[name]
	if !ok {
		data = &ProviderUsageData{}
		m.data[name] = data
	}
	data.TotalBytes += delta
	m.mu.Unlock()

	if err := m.save(); err != nil {
		logger.Error("Failed to save provider usage data", "err", err)
	}
}

// SyncUsage removes usage data for providers that are no longer active
func (m *ProviderUsageManager) SyncUsage(activeNames []string) {
	m.mu.Lock()

	activeMap := make(map[string]bool)
	for _, name := range activeNames {
		activeMap[name] = true
	}

	changed := false
	for name := range m.data {
		if !activeMap[name] {
			logger.Info("Removing orphaned usage data for provider", "name", name)
			delete(m.data, name)
			changed = true
		}
	}
	m.mu.Unlock()

	if changed {
		if err := m.save(); err != nil {
			logger.Error("Failed to save provider usage data after sync", "err", err)
		}
	}
}

