package session

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/media/loader"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/release"
	"streamnzb/pkg/usenet/nntp"
)

// Session represents an active streaming session
type Session struct {
	ID    string
	NZB   *nzb.NZB       // Parsed NZB (may be nil if deferred)
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

	// Release metadata (from release.Release) - used for AvailNZB reporting and deferred download
	Release *release.Release

	// ContentIDs for AvailNZB reporting (movie/TV context from catalog request)
	ContentIDs *AvailReportMeta

	// Deferred download: URL to fetch NZB (may have apikey added by caller); indexer for DownloadNZB
	downloadURL string
	indexer     indexer.Indexer
}

// ReleaseURL returns the indexer details URL for AvailNZB reporting
func (s *Session) ReleaseURL() string {
	if s.Release != nil && s.Release.DetailsURL != "" {
		return s.Release.DetailsURL
	}
	return s.downloadURL
}

// ReportSize returns size in bytes for AvailNZB (from NZB if loaded, else Release)
func (s *Session) ReportSize() int64 {
	if s.NZB != nil {
		return s.NZB.TotalSize()
	}
	if s.Release != nil {
		return s.Release.Size
	}
	return 0
}

// ReportReleaseName returns the release title for AvailNZB
func (s *Session) ReportReleaseName() string {
	if s.Release != nil {
		return s.Release.Title
	}
	return ""
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

// catFromReportMeta derives Newznab category from report meta: "5000" for TV, "2000" for movies.
func catFromReportMeta(m *AvailReportMeta) string {
	if m == nil {
		return ""
	}
	if m.Season > 0 || m.Episode > 0 || m.TvdbID != "" {
		return "5000"
	}
	if m.ImdbID != "" {
		return "2000"
	}
	return ""
}

// CreateSession creates a new session for the given NZB.
// rel provides release metadata for AvailNZB; contentIDs holds catalog context (ImdbID, TvdbID, etc.).
// Heavy work (GetContentFiles, NewFile) is done outside the manager lock.
func (m *Manager) CreateSession(sessionID string, nzbData *nzb.NZB, rel *release.Release, contentIDs *AvailReportMeta) (*Session, error) {
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

	session := &Session{
		ID:         sessionID,
		NZB:        nzbData,
		Files:      loaderFiles,
		File:       loaderFiles[0],
		Release:    rel,
		ContentIDs: contentIDs,
		CreatedAt:  time.Now(),
		LastAccess: time.Now(),
		Clients:    make(map[string]time.Time),
		ctx:        ctx,
		cancel:     cancel,
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
// downloadURL is the NZB fetch URL (caller adds apikey if needed). rel provides metadata; idx is used for DownloadNZB.
func (m *Manager) CreateDeferredSession(sessionID, downloadURL string, rel *release.Release, idx indexer.Indexer, contentIDs *AvailReportMeta) (*Session, error) {
	logger.Trace("session CreateDeferredSession start", "id", sessionID)
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.sessions[sessionID]; ok {
		existing.mu.Lock()
		existing.LastAccess = time.Now()
		existing.mu.Unlock()
		logger.Trace("session CreateDeferredSession existing", "id", sessionID)
		return existing, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	session := &Session{
		ID:          sessionID,
		NZB:         nil,
		Release:     rel,
		ContentIDs:  contentIDs,
		downloadURL: downloadURL,
		indexer:     idx,
		CreatedAt:   time.Now(),
		LastAccess:  time.Now(),
		Clients:     make(map[string]time.Time),
		ctx:         ctx,
		cancel:      cancel,
	}
	m.sessions[sessionID] = session
	logger.Trace("session CreateDeferredSession done", "id", sessionID)
	return session, nil
}

// GetOrDownloadNZB returns the NZB, downloading it if necessary.
// I/O is done outside the session lock so GetActiveSessions is not blocked.
func (s *Session) GetOrDownloadNZB(manager *Manager) (*nzb.NZB, error) {
	s.mu.Lock()
	if s.NZB != nil {
		nzb := s.NZB
		s.mu.Unlock()
		return nzb, nil
	}
	if s.downloadURL == "" || s.indexer == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("session has no NZB and no deferred download info")
	}
	nzbURL := s.downloadURL
	idx := s.indexer
	itemTitle := ""
	indexerName := ""
	reportSize := int64(0)
	reportCat := ""
	if s.Release != nil {
		itemTitle = s.Release.Title
		indexerName = s.Release.Indexer
		reportSize = s.Release.Size
		reportCat = catFromReportMeta(s.ContentIDs)
	}
	ctx := s.ctx
	s.mu.Unlock()

	var data []byte
	var err error
	downloadCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	hasAPIKey := urlHasAPIKey(nzbURL)
	if hasAPIKey {
		logger.Trace("Lazy Downloading NZB (direct)...", "title", itemTitle, "indexer", indexerName)
		data, err = idx.DownloadNZB(downloadCtx, nzbURL)
	}
	if !hasAPIKey || err != nil {
		if res, ok := idx.(indexer.IndexerWithResolve); ok {
			resolved, resolveErr := res.ResolveDownloadURL(ctx, nzbURL, itemTitle, reportSize, reportCat)
			if resolveErr != nil {
				logger.Debug("Resolve failed for direct indexer URL", "url", nzbURL, "title", itemTitle, "err", resolveErr)
				return nil, fmt.Errorf("no API key in URL and could not resolve: %w", resolveErr)
			}
			if resolved == "" {
				return nil, fmt.Errorf("no API key in URL and resolver returned empty proxy URL")
			}
			logger.Debug("Resolved to proxy URL via search", "title", itemTitle)
			data, err = idx.DownloadNZB(downloadCtx, resolved)
		} else if !hasAPIKey {
			return nil, fmt.Errorf("URL has no API key (indexer: %s); add indexer with API key", indexerName)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to lazy download NZB: %w", err)
	}
	if len(data) == 0 {
		logger.Debug("NZB download returned empty body", "indexer", indexerName, "title", itemTitle, "url", nzbURL)
		return nil, fmt.Errorf("NZB download returned empty body (indexer: %s)", indexerName)
	}
	parsedNZB, err := nzb.Parse(bytes.NewReader(data))
	if err != nil {
		snippet := string(data)
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		logger.Debug("Failed to parse NZB", "indexer", indexerName, "title", itemTitle, "url", nzbURL, "len", len(data), "snippet", snippet, "err", err)
		return nil, fmt.Errorf("failed to parse lazy downloaded NZB: %w", err)
	}
	contentFiles := parsedNZB.GetContentFiles()
	if len(contentFiles) == 0 {
		logger.Error("Lazy load: no content files in NZB",
			"title", itemTitle,
			"indexer", indexerName,
			"nzb_files", len(parsedNZB.Files),
			"details", "see DEBUG log GetContentFiles returned empty for file list")
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
	if s.NZB != nil {
		return s.NZB, nil
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
			if s.Release != nil && s.Release.Title != "" {
				title = s.Release.Title
			} else if s.NZB != nil && len(s.NZB.Files) > 0 {
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

func urlHasAPIKey(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	q := u.Query()
	return q.Get("apikey") != "" || q.Get("api_key") != "" || q.Get("r") != ""
}
