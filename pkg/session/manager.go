package session

import (
	"fmt"
	"sync"
	"time"

	"streamnzb/pkg/loader"
	"streamnzb/pkg/nntp"
	"streamnzb/pkg/nzb"
)

// Session represents an active streaming session
type Session struct {
	ID          string
	NZB         *nzb.NZB
	Files       []*loader.File // All files related to the content (e.g. RAR volumes)
	File        *loader.File   // Helper for single-file content, or first file of archive
	// Cache for archive structure
	Blueprint interface{} // type *unpack.ArchiveBlueprint (interface to avoid strict cycle, though safe)
	CreatedAt   time.Time
	LastAccess  time.Time
	mu          sync.Mutex
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
func (m *Manager) CreateSession(sessionID string, nzbData *nzb.NZB) (*Session, error) {
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
	
	// Create loader.File for each content file
	var loaderFiles []*loader.File
	for _, info := range contentFiles {
		lf := loader.NewFile(info.File, m.pools, m.estimator)
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
	}
	
	m.sessions[sessionID] = session
	return session, nil
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
	
	delete(m.sessions, sessionID)
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
		if now.Sub(session.LastAccess) > m.ttl {
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
