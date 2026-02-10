package indexer

import (
	"streamnzb/pkg/logger"
	"streamnzb/pkg/persistence"
	"sync"
	"time"
)

// UsageData stores the usage for a specific indexer
type UsageData struct {
	LastResetDay   string `json:"last_reset_day"`
	APIHitsUsed    int    `json:"api_hits_used"`
	DownloadsUsed  int    `json:"downloads_used"`
}

// UsageManager handles persistent storage of indexer usage via StateManager
type UsageManager struct {
	state *persistence.StateManager
	data  map[string]*UsageData
	mu    sync.RWMutex
}

var globalManager *UsageManager
var managerMu sync.Mutex

// GetUsageManager returns a usage manager using the provided StateManager
func GetUsageManager(sm *persistence.StateManager) (*UsageManager, error) {
	managerMu.Lock()
	defer managerMu.Unlock()

	if globalManager != nil {
		return globalManager, nil
	}

	m := &UsageManager{
		state: sm,
		data:  make(map[string]*UsageData),
	}

	if err := m.load(); err != nil {
		return nil, err
	}

	globalManager = m
	return m, nil
}

func (m *UsageManager) load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, err := m.state.Get("indexer_usage", &m.data)
	return err
}

func (m *UsageManager) save() error {
	return m.state.Set("indexer_usage", m.data)
}

// GetIndexerUsage returns the usage for an indexer, resetting it if it's a new day
func (m *UsageManager) GetIndexerUsage(name string) *UsageData {
	m.mu.Lock()
	defer m.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	data, ok := m.data[name]
	if !ok {
		data = &UsageData{LastResetDay: today}
		m.data[name] = data
		return data
	}

	if data.LastResetDay != today {
		logger.Debug("Resetting daily usage for indexer", "name", name, "last_reset", data.LastResetDay, "today", today)
		data.LastResetDay = today
		data.APIHitsUsed = 0
		data.DownloadsUsed = 0
	}

	return data
}

// UpdateUsage updates and saves the usage for an indexer
func (m *UsageManager) UpdateUsage(name string, apiHits, downloads int) {
	m.mu.Lock()
	// We don't unlock yet because we want to save while holding lock?
	// Actually, let's update then save.
	
	today := time.Now().Format("2006-01-02")
	data, ok := m.data[name]
	if !ok {
		data = &UsageData{LastResetDay: today}
		m.data[name] = data
	}
	
	if data.LastResetDay != today {
		data.LastResetDay = today
		data.APIHitsUsed = apiHits
		data.DownloadsUsed = downloads
	} else {
		// Newznab headers often give us the TOTAL spent or REMAINING.
		// If apiHits is provided as an absolute "used" value, we set it.
		// If it's a delta, we should add it.
		// Let's assume absolute "used" for simplicity if possible.
		data.APIHitsUsed = apiHits
		data.DownloadsUsed = downloads
	}
	m.mu.Unlock()

	if err := m.save(); err != nil {
		logger.Error("Failed to save usage data", "err", err)
	}
}

// IncrementUsed increments the hits/downloads and saves
func (m *UsageManager) IncrementUsed(name string, hits, downloads int) {
	m.mu.Lock()
	today := time.Now().Format("2006-01-02")
	data, ok := m.data[name]
	if !ok {
		data = &UsageData{LastResetDay: today}
		m.data[name] = data
	}
	
	if data.LastResetDay != today {
		data.LastResetDay = today
		data.APIHitsUsed = hits
		data.DownloadsUsed = downloads
	} else {
		data.APIHitsUsed += hits
		data.DownloadsUsed += downloads
	}
	m.mu.Unlock()

	if err := m.save(); err != nil {
		logger.Error("Failed to save usage data", "err", err)
	}
}

// SyncUsage removes usage data for indexers that are no longer active
func (m *UsageManager) SyncUsage(activeNames []string) {
	m.mu.Lock()
	
	activeMap := make(map[string]bool)
	for _, name := range activeNames {
		activeMap[name] = true
	}

	changed := false
	for name := range m.data {
		if !activeMap[name] {
			logger.Info("Removing orphaned usage data for indexer", "name", name)
			delete(m.data, name)
			changed = true
		}
	}
	m.mu.Unlock()

	if changed {
		if err := m.save(); err != nil {
			logger.Error("Failed to save usage data after sync", "err", err)
		}
	}
}
