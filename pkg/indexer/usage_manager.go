package indexer

import (
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"sync"
	"time"
)

// UsageData stores the usage for a specific indexer
type UsageData struct {
	LastResetDay         string `json:"last_reset_day"`
	APIHitsUsed          int    `json:"api_hits_used"`
	DownloadsUsed        int    `json:"downloads_used"`
	AllTimeAPIHitsUsed   int    `json:"all_time_api_hits_used"`
	AllTimeDownloadsUsed int    `json:"all_time_downloads_used"`
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
	if err != nil {
		return err
	}

	var needSave bool
	for _, data := range m.data {
		if data == nil {
			continue
		}
		if data.AllTimeAPIHitsUsed != 0 || data.AllTimeDownloadsUsed != 0 {
			continue
		}
		if data.APIHitsUsed > 0 || data.DownloadsUsed > 0 {
			data.AllTimeAPIHitsUsed = data.APIHitsUsed
			data.AllTimeDownloadsUsed = data.DownloadsUsed
			needSave = true
		}
	}
	if needSave {
		_ = m.state.Set("indexer_usage", m.data)
	}
	return nil
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
		// Add previous day's final counts to all-time before resetting
		data.AllTimeAPIHitsUsed += data.APIHitsUsed
		data.AllTimeDownloadsUsed += data.DownloadsUsed
		data.APIHitsUsed = apiHits
		data.DownloadsUsed = downloads
		// Add today's usage from indexer headers (may be >0 if searches already done today)
		data.AllTimeAPIHitsUsed += apiHits
		data.AllTimeDownloadsUsed += downloads
	} else {
		// Newznab headers often give us the TOTAL spent or REMAINING.
		// If apiHits is provided as an absolute "used" value, we set it.
		// If it's a delta, we should add it.
		// Let's assume absolute "used" for simplicity if possible.
		deltaHits := apiHits - data.APIHitsUsed
		deltaDls := downloads - data.DownloadsUsed
		data.APIHitsUsed = apiHits
		data.DownloadsUsed = downloads
		if deltaHits > 0 {
			data.AllTimeAPIHitsUsed += deltaHits
		}
		if deltaDls > 0 {
			data.AllTimeDownloadsUsed += deltaDls
		}
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
	data.AllTimeAPIHitsUsed += hits
	data.AllTimeDownloadsUsed += downloads
	m.mu.Unlock()

	if err := m.save(); err != nil {
		logger.Error("Failed to save usage data", "err", err)
	}
}

// GetUsageByPrefix returns usage data for all indexers whose name has the given prefix.
// Used by meta-indexers (e.g. NZBHydra) to report per-indexer stats.
func (m *UsageManager) GetUsageByPrefix(prefix string) map[string]*UsageData {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*UsageData)
	for name, data := range m.data {
		if len(name) >= len(prefix) && name[:len(prefix)] == prefix {
			// Return a copy to avoid concurrent modification
			cp := *data
			result[name] = &cp
		}
	}
	return result
}

// SyncUsage removes usage data for indexers that are no longer active.
// Keeps sub-indexers (e.g. "NZBHydra2: NZBgeek") when their parent (e.g. "NZBHydra2") is active.
func (m *UsageManager) SyncUsage(activeNames []string) {
	m.mu.Lock()

	activeMap := make(map[string]bool)
	for _, name := range activeNames {
		activeMap[name] = true
	}

	// Check if name is active (directly or as sub-indexer of an active parent)
	isActive := func(name string) bool {
		if activeMap[name] {
			return true
		}
		// Check if this is a sub-indexer (e.g. "NZBHydra2: NZBgeek")
		for active := range activeMap {
			prefix := active + ": "
			if len(name) > len(prefix) && name[:len(prefix)] == prefix {
				return true
			}
		}
		return false
	}

	changed := false
	for name := range m.data {
		if !isActive(name) {
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
