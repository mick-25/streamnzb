package api

import (
	"sort"
	"time"

	"streamnzb/pkg/session"
)

// SystemStats represents the current state of the application
type SystemStats struct {
	Timestamp         time.Time                   `json:"timestamp"`
	TotalSpeed        float64                     `json:"total_speed_mbps"` // Mbps
	ActiveStreams     int                         `json:"active_streams"`
	TotalConnections  int                         `json:"total_connections"`
	ActiveConnections int                         `json:"active_connections"`
	Providers         []ProviderStats             `json:"providers"`
	ActiveSessions    []session.ActiveSessionInfo `json:"active_sessions"`
}

// ProviderStats represents statistics for a single NNTP provider
type ProviderStats struct {
	Name         string  `json:"name"`
	Host         string  `json:"host"`
	ActiveConns  int     `json:"active_conns"`
	IdleConns    int     `json:"idle_conns"`
	MaxConns     int     `json:"max_conns"`
	CurrentSpeed float64 `json:"current_speed_mbps"` // Mbps
}

// collectStats gathers metrics from all sources
func (s *Server) collectStats() SystemStats {
	stats := SystemStats{
		Timestamp: time.Now(),
		Providers: make([]ProviderStats, 0, len(s.providerPools)),
	}

	var totalActive, totalMax int

	for name, pool := range s.providerPools {
		pStats := ProviderStats{
			Name:         name,
			Host:         pool.Host(),
			ActiveConns:  pool.ActiveConnections(),
			IdleConns:    pool.IdleConnections(),
			MaxConns:     pool.MaxConn(),
			CurrentSpeed: pool.GetSpeed(),
		}

		totalActive += pStats.ActiveConns
		totalMax += pStats.MaxConns
		stats.TotalSpeed += pStats.CurrentSpeed

		stats.Providers = append(stats.Providers, pStats)
	}

	// Sort providers by name
	sort.Slice(stats.Providers, func(i, j int) bool {
		return stats.Providers[i].Name < stats.Providers[j].Name
	})

	stats.ActiveConnections = totalActive
	stats.TotalConnections = totalMax

	// Active Sessions (Detailed)
	stats.ActiveSessions = s.sessionMgr.GetActiveSessions()
	stats.ActiveStreams = len(stats.ActiveSessions)

	return stats
}
