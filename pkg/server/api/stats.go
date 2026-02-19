package api

import (
	"fmt"
	"net"
	"sort"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/session"
)

// SystemStats represents the current state of the application
type SystemStats struct {
	Timestamp         time.Time                   `json:"timestamp"`
	TotalSpeed        float64                     `json:"total_speed_mbps"` // Mbps
	ActiveStreams     int                         `json:"active_streams"`
	TotalConnections  int                         `json:"total_connections"`
	ActiveConnections int                         `json:"active_connections"`
	TotalDownloadedMB float64                     `json:"total_downloaded_mb"` // Sum of all providers
	Providers         []ProviderStats             `json:"providers"`
	Indexers          []IndexerStats              `json:"indexers"`
	ActiveSessions    []session.ActiveSessionInfo `json:"active_sessions"`
}

// IndexerStats represents statistics and usage for an indexer
type IndexerStats struct {
	Name                   string `json:"name"`
	APIHitsLimit           int    `json:"api_hits_limit"`
	APIHitsUsed            int    `json:"api_hits_used"`
	APIHitsRemaining       int    `json:"api_hits_remaining"`
	AllTimeAPIHitsUsed     int    `json:"api_hits_used_all_time"`
	DownloadsLimit         int    `json:"downloads_limit"`
	DownloadsUsed          int    `json:"downloads_used"`
	DownloadsRemaining     int    `json:"downloads_remaining"`
	AllTimeDownloadsUsed   int    `json:"downloads_used_all_time"`
}

// ProviderStats represents statistics for a single NNTP provider
type ProviderStats struct {
	Name         string  `json:"name"`
	Host         string  `json:"host"`
	ActiveConns  int     `json:"active_conns"`
	IdleConns    int     `json:"idle_conns"`
	MaxConns     int     `json:"max_conns"`
	CurrentSpeed float64 `json:"current_speed_mbps"` // Mbps (instantaneous)
	DownloadedMB float64 `json:"downloaded_mb"`      // Lifetime MB for this provider
	UsagePercent float64 `json:"usage_percent"`      // Share of total downloaded MB
}

// collectStats gathers metrics from all sources
func (s *Server) collectStats() SystemStats {
	logger.Trace("collectStats start")
	stats := SystemStats{
		Timestamp: time.Now(),
		Providers: make([]ProviderStats, 0),
	}

	var totalActive, totalMax int
	var totalDownloadedMB float64

	s.mu.RLock()
	pools := s.providerPools
	s.mu.RUnlock()

	for name, pool := range pools {
		downloadedMB := pool.TotalMegabytes()

		pStats := ProviderStats{
			Name:         name,
			Host:         pool.Host(),
			ActiveConns:  pool.ActiveConnections(),
			IdleConns:    pool.IdleConnections(),
			MaxConns:     pool.MaxConn(),
			CurrentSpeed: pool.GetSpeed(),
			DownloadedMB: downloadedMB,
		}

		totalActive += pStats.ActiveConns
		totalMax += pStats.MaxConns
		stats.TotalSpeed += pStats.CurrentSpeed
		totalDownloadedMB += downloadedMB

		stats.Providers = append(stats.Providers, pStats)
	}

	// Compute usage share per provider
	if totalDownloadedMB > 0 {
		for i := range stats.Providers {
			stats.Providers[i].UsagePercent = (stats.Providers[i].DownloadedMB / totalDownloadedMB) * 100
		}
		stats.TotalDownloadedMB = totalDownloadedMB
	}

	// Sort providers by name
	sort.Slice(stats.Providers, func(i, j int) bool {
		return stats.Providers[i].Name < stats.Providers[j].Name
	})

	// Indexer Stats
	if s.indexer != nil {
		// If it's an aggregator, we want details for each internal indexer
		// Actually, let's just use the aggregator's Indexers if it has them
		// We'll use a type assertion or just call GetUsage.
		// But for the dashboard we want a list of individual ones.

		// In Aggregator.go we have Indexers []Indexer.
		// We can't directly access it if it's the Indexer interface.
		// Wait, s.indexer is set during initialization.

		// Let's assume s.indexer might be an Aggregator
		type indexerContainer interface {
			GetIndexers() []indexer.Indexer
		}

		var indexers []indexer.Indexer
		if container, ok := s.indexer.(indexerContainer); ok {
			indexers = container.GetIndexers()
		} else {
			indexers = []indexer.Indexer{s.indexer}
		}

		for _, idx := range indexers {
			usage := idx.GetUsage()
			stats.Indexers = append(stats.Indexers, IndexerStats{
				Name:                 idx.Name(),
				APIHitsLimit:         usage.APIHitsLimit,
				APIHitsUsed:          usage.APIHitsUsed,
				APIHitsRemaining:     usage.APIHitsRemaining,
				AllTimeAPIHitsUsed:   usage.AllTimeAPIHitsUsed,
				DownloadsLimit:       usage.DownloadsLimit,
				DownloadsUsed:        usage.DownloadsUsed,
				DownloadsRemaining:   usage.DownloadsRemaining,
				AllTimeDownloadsUsed: usage.AllTimeDownloadsUsed,
			})
		}
	}

	stats.ActiveConnections = totalActive
	stats.TotalConnections = totalMax

	// Active Sessions (Detailed)
	stats.ActiveSessions = s.sessionMgr.GetActiveSessions()

	// Append Proxy Sessions (Aggregated by IP)
	s.mu.RLock() // Lock for proxyServer access
	if s.proxyServer != nil {
		proxySessions := s.proxyServer.GetSessions()

		// Group by Client IP
		type proxyGroup struct {
			count int
			group string
			ip    string
		}
		groups := make(map[string]*proxyGroup)

		for _, ps := range proxySessions {
			// Extract IP (naive strip port if present, or use as is)
			// Assuming remote_addr is "ip:port"
			ip := ps.RemoteAddr
			if host, _, err := net.SplitHostPort(ip); err == nil {
				ip = host
			}

			if _, exists := groups[ip]; !exists {
				groups[ip] = &proxyGroup{ip: ip}
			}
			g := groups[ip]
			g.count++
			// Keep last non-empty group as representative?
			if ps.CurrentGroup != "" {
				g.group = ps.CurrentGroup
			}
		}

		// Convert groups to ActiveSessionInfo
		for ip, g := range groups {
			title := fmt.Sprintf("Proxy Client (%d conns)", g.count)
			if g.group != "" {
				title = fmt.Sprintf("Proxy: %s (%d conns)", g.group, g.count)
			}

			stats.ActiveSessions = append(stats.ActiveSessions, session.ActiveSessionInfo{
				ID:      fmt.Sprintf("proxy-%s", ip),
				Title:   title,
				Clients: []string{ip},
			})
		}
	}
	s.mu.RUnlock()

	stats.ActiveStreams = len(stats.ActiveSessions)

	logger.Trace("collectStats done", "providers", len(stats.Providers), "sessions", len(stats.ActiveSessions))
	return stats
}
