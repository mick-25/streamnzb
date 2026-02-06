package api

import (
	"fmt"
	"net"
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

	return stats
}
