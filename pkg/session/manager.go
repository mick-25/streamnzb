package session

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/indexer"
	"streamnzb/pkg/loader"
	"streamnzb/pkg/logger"
	"streamnzb/pkg/nntp"
	"streamnzb/pkg/nzb"
)

// Session represents an active streaming session
type Session struct {
	ID    string
	NZB   *nzb.NZB // Parsed NZB (may be nil if deferred)
	Files []*loader.File // All files related to the content (e.g. RAR volumes)
	File  *loader.File   // Helper for single-file content, or first file of archive
	// Cache for archive structure
	Blueprint   interface{} // type *unpack.ArchiveBlueprint (interface to avoid strict cycle, though safe)
	CreatedAt   time.Time
	LastAccess  time.Time
	ActivePlays int32
	Clients     map[string]time.Time // IP -> Connected time
	mu          sync.Mutex

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc

	// Deferred download fields
	NZBURL      string
	IndexerName string
	ItemTitle   string          // Used for logging
	Indexer     indexer.Indexer // Interface to download
	GUID        string          // External ID for reporting
}

// Manager manages active streaming sessions
type Manager struct {
	sessions  map[string]*Session
	pools     []*nntp.ClientPool
	estimator *loader.SegmentSizeEstimator
	ttl       time.Duration
	mu        sync.RWMutex
}

// SetBlueprint caches the archive blueprint
func (s *Session) SetBlueprint(bp interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Blueprint = bp
}

// Manager manages active streaming sessions
func NewManager(pools []*nntp.ClientPool, ttl time.Duration) *Manager {
	m := &Manager{
		sessions:  make(map[string]*Session),
		pools:     pools,
		estimator: loader.NewSegmentSizeEstimator(),
		ttl:       ttl,
	}

	// Start cleanup goroutine
	go m.cleanupLoop()

	return m
}

// CreateSession creates a new session for the given NZB
func (m *Manager) CreateSession(sessionID string, nzbData *nzb.NZB, guid string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if session already exists
	if existing, ok := m.sessions[sessionID]; ok {
		existing.mu.Lock()
		existing.LastAccess = time.Now()
		existing.mu.Unlock()
		return existing, nil
	}

	// Get content files from NZB
	contentFiles := nzbData.GetContentFiles()
	if len(contentFiles) == 0 {
		return nil, fmt.Errorf("no content files found in NZB")
	}

	// Lifecycle context
	ctx, cancel := context.WithCancel(context.Background())

	// Create loader.File for each content file
	var loaderFiles []*loader.File
	for _, info := range contentFiles {
		lf := loader.NewFile(ctx, info.File, m.pools, m.estimator)
		loaderFiles = append(loaderFiles, lf)
	}

	// Helper: File is the first one (often sufficient for simple check or single file)
	firstFile := loaderFiles[0]

	session := &Session{
		ID:         sessionID,
		NZB:        nzbData,
		Files:      loaderFiles,
		File:       firstFile, // Keep for backward compat within session pkg
		CreatedAt:  time.Now(),
		LastAccess: time.Now(),
		Clients:    make(map[string]time.Time),
		GUID:       guid,
		ctx:        ctx,
		cancel:     cancel,
	}

	m.sessions[sessionID] = session
	return session, nil
}

// CreateDeferredSession creates a session placeholder without downloading the NZB yet
func (m *Manager) CreateDeferredSession(sessionID, nzbURL, indexerName, itemTitle string, idx indexer.Indexer, guid string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if session already exists
	if existing, ok := m.sessions[sessionID]; ok {
		existing.mu.Lock()
		existing.LastAccess = time.Now()
		existing.mu.Unlock()
		return existing, nil
	}

	ctx, cancel := context.WithCancel(context.Background())

	session := &Session{
		ID:          sessionID,
		NZB:         nil, // Deferred
		CreatedAt:   time.Now(),
		LastAccess:  time.Now(),
		Clients:     make(map[string]time.Time),
		// Deferred fields
		NZBURL:      nzbURL,
		IndexerName: indexerName,
		ItemTitle:   itemTitle,
		Indexer:     idx,
		GUID:        guid,
		ctx:         ctx,
		cancel:      cancel,
	}

	m.sessions[sessionID] = session
	return session, nil
}

// GetOrDownloadNZB returns the NZB, downloading it if necessary
func (s *Session) GetOrDownloadNZB(manager *Manager) (*nzb.NZB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.NZB != nil {
		return s.NZB, nil
	}

	// Double-check if we have deferred info
	if s.NZBURL == "" || s.Indexer == nil {
		return nil, fmt.Errorf("session has no NZB and no deferred download info")
	}

	// Download it now!
	// We use the stored Indexer interface
	logger.Info("Lazy Downloading NZB...", "title", s.ItemTitle, "indexer", s.IndexerName)
	
	data, err := s.Indexer.DownloadNZB(s.NZBURL)
	if err != nil {
		return nil, fmt.Errorf("failed to lazy download NZB: %w", err)
	}

	// Parse it
	parsedNZB, err := nzb.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to parse lazy downloaded NZB: %w", err)
	}

	// Initialize files (same logic as CreateSession)
	contentFiles := parsedNZB.GetContentFiles()
	if len(contentFiles) == 0 {
		return nil, fmt.Errorf("no content files found in lazy NZB")
	}

	// We need access to manager's pools/estimator to create loader files
	// We passed manager as arg to avoid storing it in Session (circular ref or just clean design)
	var loaderFiles []*loader.File
	for _, info := range contentFiles {
		lf := loader.NewFile(s.ctx, info.File, manager.pools, manager.estimator)
		loaderFiles = append(loaderFiles, lf)
	}

	s.NZB = parsedNZB
	s.Files = loaderFiles
	s.File = loaderFiles[0]
	
	return s.NZB, nil
}

// GetSession retrieves an existing session
func (m *Manager) GetSession(sessionID string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	session, ok := m.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	// Update last access time
	session.mu.Lock()
	session.LastAccess = time.Now()
	session.mu.Unlock()

	return session, nil
}

// DeleteSession removes a session
func (m *Manager) DeleteSession(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if sess, ok := m.sessions[sessionID]; ok {
		sess.Close()
		delete(m.sessions, sessionID)
	}
}

// Close explicitly stops all active streams and allows the session to be cleaned up
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}

// cleanupLoop periodically removes expired sessions
func (m *Manager) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		m.cleanup()
	}
}

// cleanup removes sessions that haven't been accessed within TTL
func (m *Manager) cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for id, session := range m.sessions {
		session.mu.Lock()
		// Don't clean up sessions with active playback
		// This prevents cancelling streams that are currently being served
		hasActivePlayback := session.ActivePlays > 0 || len(session.Clients) > 0
		if !hasActivePlayback && now.Sub(session.LastAccess) > m.ttl {
			session.Close()
			delete(m.sessions, id)
		}
		session.mu.Unlock()
	}
}

// Stats returns session statistics
func (m *Manager) Stats() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return map[string]interface{}{
		"active_sessions": len(m.sessions),
		"ttl_minutes":     m.ttl.Minutes(),
	}
}

// Count returns the number of sessions
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// CountActive returns the number of sessions currently being played
func (m *Manager) CountActive() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := 0
	for _, s := range m.sessions {
		// We can read ActivePlays without session lock if we accept slight race,
		// but better to allow atomic access or just lock.
		// Since we use atomic for updates (or lock), let's just lock for correctness.
		s.mu.Lock()
		if s.ActivePlays > 0 {
			count++
		}
		s.mu.Unlock()
	}
	return count
}

// StartPlayback increments the active play count for a session and tracks IP
func (m *Manager) StartPlayback(id, ip string) {
	s, err := m.GetSession(id)
	if err == nil {
		s.mu.Lock()
		s.ActivePlays++
		s.Clients[ip] = time.Now()
		s.mu.Unlock()
	}
}

// EndPlayback decrements the active play count for a session and removes IP
func (m *Manager) EndPlayback(id, ip string) {
	s, err := m.GetSession(id)
	if err == nil {
		s.mu.Lock()
		if s.ActivePlays > 0 {
			s.ActivePlays--
		}
		// Update last seen for the IP to ensure it stays for grace period
		s.Clients[ip] = time.Now()
		s.mu.Unlock()
	}
}

// KeepAlive updates the last access time for a session and client
func (m *Manager) KeepAlive(id, ip string) {
	s, err := m.GetSession(id)
	if err == nil {
		s.mu.Lock()
		s.LastAccess = time.Now()
		s.Clients[ip] = time.Now()
		s.mu.Unlock()
	}
}

// ActiveSessionInfo provides details about a currently playing session
type ActiveSessionInfo struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Clients   []string `json:"clients"`
	StartTime string   `json:"start_time"`
}

// GetActiveSessions returns a list of sessions that are currently playing
func (m *Manager) GetActiveSessions() []ActiveSessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []ActiveSessionInfo
	for _, s := range m.sessions {
		s.mu.Lock()

		// 1. Purge IPs that haven't been seen for 60 seconds
		for ip, lastSeen := range s.Clients {
			if time.Since(lastSeen) > 60*time.Second {
				delete(s.Clients, ip)
			}
		}

		// 2. A session is active if it has clients.
		// Search availability checks create sessions but don't register clients,
		// so they won't appear as active streams.
		isActive := len(s.Clients) > 0

		if isActive {
			clients := make([]string, 0, len(s.Clients))
			for ip := range s.Clients {
				clients = append(clients, ip)
			}

			title := "Unknown"
			if s.NZB != nil && len(s.NZB.Files) > 0 {
				parts := strings.Split(nzb.ExtractFilename(s.NZB.Files[0].Subject), ".")
				if len(parts) > 1 {
					title = strings.Join(parts[:len(parts)-1], ".")
				} else {
					title = parts[0]
				}
			}

			result = append(result, ActiveSessionInfo{
				ID:        s.ID,
				Title:     title,
				Clients:   clients,
				StartTime: s.CreatedAt.Format(time.Kitchen),
			})
		}
		s.mu.Unlock()
	}
	return result
}

// UpdatePools swaps the provider pools at runtime
func (m *Manager) UpdatePools(pools []*nntp.ClientPool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pools = pools
}
