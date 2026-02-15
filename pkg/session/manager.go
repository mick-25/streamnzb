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

	// ReleaseURL is the indexer release URL (e.g. item.Link) for AvailNZB reporting
	ReleaseURL string

	// AvailNZB report meta (optional); set when creating session so bad reports can include content IDs
	ReportReleaseName  string
	ReportDownloadLink string // NZB download URL for report (apikey stripped by client)
	ReportSize         int64  // File size in bytes for report (required by API)
	ReportImdbID       string
	ReportTvdbID       string
	ReportSeason       int
	ReportEpisode      int
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

// AvailReportMeta holds optional content IDs for availability reporting (movie or TV).
type AvailReportMeta struct {
	ImdbID  string
	TvdbID  string
	Season  int
	Episode int
}

// CreateSession creates a new session for the given NZB.
// releaseURL is the indexer details URL for availability reporting. releaseName, downloadLink, and reportSize are for AvailNZB reports.
// reportMeta is optional; when set, bad-playback reports can include content IDs.
// Heavy work (GetContentFiles, NewFile) is done outside the manager lock so
// GetActiveSessions (e.g. from WebSocket collectStats) is not blocked.
func (m *Manager) CreateSession(sessionID string, nzbData *nzb.NZB, guid string, releaseURL string, reportMeta *AvailReportMeta, releaseName string, downloadLink string, reportSize int64) (*Session, error) {
	logger.Trace("session CreateSession start", "id", sessionID)
	m.mu.Lock()
	if existing, ok := m.sessions[sessionID]; ok {
		existing.mu.Lock()
		existing.LastAccess = time.Now()
		existing.mu.Unlock()
		m.mu.Unlock()
		logger.Trace("session CreateSession existing", "id", sessionID)
		return existing, nil
	}
	m.mu.Unlock()

	logger.Trace("session CreateSession heavy work", "id", sessionID)
	// Heavy work outside lock so we don't block GetActiveSessions / WebSocket stats
	contentFiles := nzbData.GetContentFiles()
	if len(contentFiles) == 0 {
		return nil, fmt.Errorf("no content files found in NZB")
	}
	m.mu.RLock()
	pools := m.pools
	estimator := m.estimator
	m.mu.RUnlock()

	ctx, cancel := context.WithCancel(context.Background())
	var loaderFiles []*loader.File
	for _, info := range contentFiles {
		lf := loader.NewFile(ctx, info.File, pools, estimator)
		loaderFiles = append(loaderFiles, lf)
	}

	firstFile := loaderFiles[0]
	session := &Session{
		ID:         sessionID,
		NZB:        nzbData,
		Files:      loaderFiles,
		File:       firstFile,
		CreatedAt:  time.Now(),
		LastAccess: time.Now(),
		Clients:    make(map[string]time.Time),
		GUID:       guid,
		ReleaseURL: releaseURL,
		ctx:        ctx,
		cancel:     cancel,
	}
	session.ReportReleaseName = releaseName
	session.ReportDownloadLink = downloadLink
	session.ReportSize = reportSize
	if reportMeta != nil {
		session.ReportImdbID = reportMeta.ImdbID
		session.ReportTvdbID = reportMeta.TvdbID
		session.ReportSeason = reportMeta.Season
		session.ReportEpisode = reportMeta.Episode
	}

	logger.Trace("session CreateSession insert", "id", sessionID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.sessions[sessionID]; ok {
		existing.mu.Lock()
		existing.LastAccess = time.Now()
		existing.mu.Unlock()
		return existing, nil
	}
	m.sessions[sessionID] = session
	logger.Trace("session CreateSession done", "id", sessionID)
	return session, nil
}

// CreateDeferredSession creates a session placeholder without downloading the NZB yet.
// nzbURL is the download URL for lazy fetch (include apikey if required); releaseDetailsURL is the indexer details URL for AvailNZB (if empty, nzbURL is used).
// reportSize is the known size in bytes for reporting (e.g. from AvailNZB); 0 if unknown.
// reportMeta is optional; when set, bad-playback reports can include content IDs.
func (m *Manager) CreateDeferredSession(sessionID, nzbURL, releaseDetailsURL, indexerName, itemTitle string, idx indexer.Indexer, guid string, reportMeta *AvailReportMeta, reportSize int64) (*Session, error) {
	logger.Trace("session CreateDeferredSession start", "id", sessionID)
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if session already exists
	if existing, ok := m.sessions[sessionID]; ok {
		existing.mu.Lock()
		existing.LastAccess = time.Now()
		existing.mu.Unlock()
		logger.Trace("session CreateDeferredSession existing", "id", sessionID)
		return existing, nil
	}

	if releaseDetailsURL == "" {
		releaseDetailsURL = nzbURL
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
		ReleaseURL:  releaseDetailsURL,
		IndexerName: indexerName,
		ItemTitle:   itemTitle,
		Indexer:     idx,
		GUID:        guid,
		ctx:         ctx,
		cancel:      cancel,
	}
	session.ReportReleaseName = itemTitle
	session.ReportDownloadLink = nzbURL
	session.ReportSize = reportSize
	if reportMeta != nil {
		session.ReportImdbID = reportMeta.ImdbID
		session.ReportTvdbID = reportMeta.TvdbID
		session.ReportSeason = reportMeta.Season
		session.ReportEpisode = reportMeta.Episode
	}

	m.sessions[sessionID] = session
	logger.Trace("session CreateDeferredSession done", "id", sessionID)
	return session, nil
}

// GetOrDownloadNZB returns the NZB, downloading it if necessary.
// I/O (DownloadNZB, parse, NewFile) is done outside the session lock so GetActiveSessions
// and other callers are not blocked for the duration of the download (avoids app appearing
// hung and requiring restart when the indexer is slow or the play request holds the lock).
func (s *Session) GetOrDownloadNZB(manager *Manager) (*nzb.NZB, error) {
	s.mu.Lock()
	if s.NZB != nil {
		nzb := s.NZB
		s.mu.Unlock()
		return nzb, nil
	}
	if s.NZBURL == "" || s.Indexer == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("session has no NZB and no deferred download info")
	}
	nzbURL := s.NZBURL
	indexer := s.Indexer
	itemTitle := s.ItemTitle
	indexerName := s.IndexerName
	ctx := s.ctx
	s.mu.Unlock()

	// Download and parse outside lock so session is not held for I/O duration
	logger.Info("Lazy Downloading NZB...", "title", itemTitle, "indexer", indexerName)
	data, err := indexer.DownloadNZB(nzbURL)
	if err != nil {
		return nil, fmt.Errorf("failed to lazy download NZB: %w", err)
	}
	parsedNZB, err := nzb.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to parse lazy downloaded NZB: %w", err)
	}
	contentFiles := parsedNZB.GetContentFiles()
	if len(contentFiles) == 0 {
		return nil, fmt.Errorf("no content files found in lazy NZB")
	}

	manager.mu.RLock()
	pools := manager.pools
	estimator := manager.estimator
	manager.mu.RUnlock()

	var loaderFiles []*loader.File
	for _, info := range contentFiles {
		lf := loader.NewFile(ctx, info.File, pools, estimator)
		loaderFiles = append(loaderFiles, lf)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-check in case another goroutine filled it
	if s.NZB != nil {
		return s.NZB, nil
	}
	s.NZB = parsedNZB
	s.Files = loaderFiles
	s.File = loaderFiles[0]
	if s.ReportSize == 0 {
		s.ReportSize = parsedNZB.TotalSize()
	}
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

// cleanup removes sessions that haven't been accessed within TTL.
// We must not call session.Close() while holding session.mu: Close() locks the same
// mutex, causing deadlock (sync.Mutex is not reentrant). So we remove from map
// under lock, then unlock, then Close().
func (m *Manager) cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	var toClose []*Session
	for id, session := range m.sessions {
		session.mu.Lock()
		hasActivePlayback := session.ActivePlays > 0 || len(session.Clients) > 0
		if !hasActivePlayback && now.Sub(session.LastAccess) > m.ttl {
			delete(m.sessions, id)
			toClose = append(toClose, session)
		}
		session.mu.Unlock()
	}
	for _, s := range toClose {
		s.Close()
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

// GetActiveSessions returns a list of sessions that are currently playing.
// We snapshot session refs under RLock then release, so CreateSession/CreateDeferredSession
// are not blocked while we lock each session (avoids blocking stream validation and WebSocket).
func (m *Manager) GetActiveSessions() []ActiveSessionInfo {
	logger.Trace("session GetActiveSessions start")
	m.mu.RLock()
	snapshot := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		snapshot = append(snapshot, s)
	}
	m.mu.RUnlock()

	var result []ActiveSessionInfo
	for _, s := range snapshot {
		// Use TryLock so we never block: if a session is busy (e.g. KeepAlive during stream Read),
		// skip it this round rather than hanging the dashboard/WebSocket stats.
		if !s.mu.TryLock() {
			continue
		}
		// Purge IPs that haven't been seen for 60 seconds
		for ip, lastSeen := range s.Clients {
			if time.Since(lastSeen) > 60*time.Second {
				delete(s.Clients, ip)
			}
		}
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
	logger.Trace("session GetActiveSessions done", "sessions", len(snapshot), "active", len(result))
	return result
}

// UpdatePools swaps the provider pools at runtime
func (m *Manager) UpdatePools(pools []*nntp.ClientPool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pools = pools
}
