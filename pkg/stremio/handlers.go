package stremio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/availnzb"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/loader"
	"streamnzb/pkg/logger"
	"streamnzb/pkg/nzb"
	"streamnzb/pkg/session"
	"streamnzb/pkg/tmdb"
	"streamnzb/pkg/triage"
	"streamnzb/pkg/unpack"
	"streamnzb/pkg/validation"
)

// Server represents the Stremio addon HTTP server
type Server struct {
	mu             sync.RWMutex
	manifest       *Manifest
	baseURL        string
	indexer        indexer.Indexer
	validator      *validation.Checker
	sessionManager *session.Manager
	triageService  *triage.Service
	availClient    *availnzb.Client
	tmdbClient     *tmdb.Client
	securityToken  string
	webHandler     http.Handler
	apiHandler     http.Handler
}

// NewServer creates a new Stremio addon server
func NewServer(baseURL string, port int, indexer indexer.Indexer, validator *validation.Checker, 
	sessionMgr *session.Manager, triageService *triage.Service, availClient *availnzb.Client, 
	tmdbClient *tmdb.Client, securityToken string) (*Server, error) {
	
	actualBaseURL := baseURL
	if securityToken != "" {
		if !strings.HasSuffix(actualBaseURL, "/") {
			actualBaseURL += "/"
		}
		actualBaseURL += securityToken
	}

	s := &Server{
		manifest:       NewManifest(),
		baseURL:        actualBaseURL,
		indexer:        indexer,
		validator:      validator,
		sessionManager: sessionMgr,
		triageService:  triageService,
		availClient:    availClient,
		tmdbClient:     tmdbClient,
		securityToken:  securityToken,
	}

	if err := s.CheckPort(port); err != nil {
		return nil, err
	}

	return s, nil
}

// CheckPort verifies if the specified port is available for the addon
func (s *Server) CheckPort(port int) error {
	address := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("addon port %d is already in use", port)
	}
	ln.Close()
	return nil
}

// SetWebHandler sets the handler for static web content (fallback)
func (s *Server) SetWebHandler(h http.Handler) {
	s.webHandler = h
}

// SetAPIHandler sets the handler for API requests
func (s *Server) SetAPIHandler(h http.Handler) {
	s.apiHandler = h
}

// SetupRoutes configures HTTP routes for the addon
func (s *Server) SetupRoutes(mux *http.ServeMux) {
	// Root handler for manifest and other routes
	// We use a custom handler to handle the optional token prefix
	finalHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		securityToken := s.securityToken
		webHandler := s.webHandler
		apiHandler := s.apiHandler
		s.mu.RUnlock()

		path := r.URL.Path
		
		if securityToken != "" {
			// Path format: /{token}/manifest.json or /{token}/stream/...
			trimmedPath := strings.TrimPrefix(path, "/")
			parts := strings.SplitN(trimmedPath, "/", 2)
			
			if len(parts) < 1 || parts[0] != securityToken {
				logger.Error("Unauthorized request", "path", path, "remote", r.RemoteAddr)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			
			// Ensure trailing slash for root token path to support relative assets
			if len(parts) == 1 && !strings.HasSuffix(path, "/") {
				http.Redirect(w, r, path+"/", http.StatusTemporaryRedirect)
				return
			}
			
			// Strip token from path for internal routing
			if len(parts) > 1 {
				path = "/" + parts[1]
			} else {
				path = "/"
			}
			// Update the request path so the internal mux works
			r.URL.Path = path
		}

		// Internal routing
		if path == "/manifest.json" {
			s.handleManifest(w, r)
		} else if strings.HasPrefix(path, "/stream/") {
			s.handleStream(w, r)
		} else if strings.HasPrefix(path, "/play/") {
			s.handlePlay(w, r)
		} else if path == "/health" {
			s.handleHealth(w, r)
		} else if strings.HasPrefix(path, "/api/") {
			if apiHandler != nil {
				// API Handler expects /api/...
				// Current path is /api/... (token stripped)
				// Need to preserve the path for the API mux
				apiHandler.ServeHTTP(w, r)
			} else {
				http.NotFound(w, r)
			}
		} else {
			if webHandler != nil {
				webHandler.ServeHTTP(w, r)
			} else {
				http.NotFound(w, r)
			}
		}
	})

	mux.Handle("/", finalHandler)
}

// handleManifest serves the addon manifest
func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	logger.Debug("Manifest request", "remote", r.RemoteAddr)
	
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	
	s.mu.RLock()
	manifest := s.manifest
	s.mu.RUnlock()

	data, err := manifest.ToJSON()
	if err != nil {
		http.Error(w, "Failed to generate manifest", http.StatusInternalServerError)
		return
	}
	
	w.Write(data)
}

// handleStream handles stream requests
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	// Parse URL: /stream/{type}/{id}.json
	path := strings.TrimPrefix(r.URL.Path, "/stream/")
	path = strings.TrimSuffix(path, ".json")
	
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		http.Error(w, "Invalid stream URL", http.StatusBadRequest)
		return
	}
	
	contentType := parts[0] // "movie" or "series"
	id := parts[1]          // IMDb ID (tt1234567) or TMDB ID
	
	logger.Info("Stream request", "type", contentType, "id", id)
	
	// Search NZBHydra2
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	
	streams, err := s.searchAndValidate(ctx, contentType, id)
	if err != nil {
		logger.Error("Error searching for streams", "err", err)
		streams = []Stream{} // Return empty list on error
	}
	
	response := StreamResponse{
		Streams: streams,
	}
	
	// Debug: Log the response
	responseJSON, _ := json.MarshalIndent(response, "", "  ")
	logger.Debug("Sending stream response", "json", string(responseJSON))
	
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	
	json.NewEncoder(w).Encode(response)
}

func (s *Server) searchAndValidate(ctx context.Context, contentType, id string) ([]Stream, error) {
	// Determine search parameters based on ID type
	req := indexer.SearchRequest{
		Limit: 1000,
	}
	
	// For series, extract IMDb/TMDB ID and season/episode
	searchID := id
	if contentType == "series" && strings.Contains(id, ":") {
		parts := strings.Split(id, ":")
		// Handle "tmdb:12345:1:1" format
		if parts[0] == "tmdb" && len(parts) >= 4 {
			searchID = parts[1]
			req.Season = parts[2]
			req.Episode = parts[3]
		} else if len(parts) >= 3 {
			// Standard "tt12345678:1:1" format
			searchID = parts[0]
			req.Season = parts[1]
			req.Episode = parts[2]
		} else if len(parts) > 0 {
			searchID = parts[0]
		}
	} else if strings.HasPrefix(id, "tmdb:") {
		// Handle movie "tmdb:12345"
		searchID = strings.TrimPrefix(id, "tmdb:")
	}
	
	if strings.HasPrefix(searchID, "tt") {
		req.IMDbID = searchID
	} else {
		req.TMDBID = searchID
	}
	
	// Set category based on content type
	if contentType == "movie" {
		req.Cat = "2000" // Movies category
	} else {
		req.Cat = "5000" // TV category
		
		// Attempt to resolve TVDB ID using TMDB (if available) for better indexer results
		// Only relevant if we have an IMDb ID and no specific TVDB ID yet
		if req.IMDbID != "" && req.TVDBID == "" && s.tmdbClient != nil {
			tvdbID, err := s.tmdbClient.ResolveTVDBID(req.IMDbID)
			if err == nil && tvdbID != "" {
				req.TVDBID = tvdbID
				req.IMDbID = ""
				logger.Debug("Enriched search with TVDB ID", "imdb", req.IMDbID, "tvdb", tvdbID)
			} else if err != nil {
				logger.Debug("Failed to resolve TVDB ID", "err", err)
			}
		}
	}
	
	// Debug: Log search parameters
	logger.Debug("Indexer search", "imdb", req.IMDbID, "tvdb", req.TVDBID, "cat", req.Cat, "season", req.Season, "ep", req.Episode)
	
	// Search Indexer
	searchResp, err := s.indexer.Search(req)
	if err != nil {
		return nil, fmt.Errorf("indexer search failed: %w", err)
	}
	
	logger.Debug("Found NZB results", "count", len(searchResp.Channel.Items))
	
	// Filter results using Triage Service
	candidates := s.triageService.Filter(searchResp.Channel.Items)
	logger.Debug("Selected candidates after triage", "count", len(candidates))

	// Process candidates in parallel
	type nzbResult struct {
		stream Stream
		err    error
		group  string
	}
	
	resultChan := make(chan nzbResult, len(candidates))
	var wg sync.WaitGroup

	// Limit concurrent validations (Concurrent Worker Pool)
	// Reduced from 20 to 5 to verify candidates sequentially/lazily
	const maxWorkers = 5
	sem := make(chan struct{}, maxWorkers)

	// Quota Tracker (Thread-Safe)
	// Allows workers to skip candidates if we already have enough for that group
	// Quota Tracker (Thread-Safe)
	var quotaMu sync.Mutex
	quotaCounts := make(map[string]int)      // Successful validations
	processingCounts := make(map[string]int) // Currently in-progress validations
	const quotaPerGroup = 2
	
	// Helper to check if we still need more of this group
	// Returns true if (success + processing) < quota
	needsMore := func(group string) bool {
		quotaMu.Lock()
		defer quotaMu.Unlock()
		return (quotaCounts[group] + processingCounts[group]) < quotaPerGroup
	}

	// Helper to mark start of processing
	startProcessing := func(group string) {
		quotaMu.Lock()
		defer quotaMu.Unlock()
		processingCounts[group]++
	}

	// Helper to mark end of processing (always called)
	endProcessing := func(group string) {
		quotaMu.Lock()
		defer quotaMu.Unlock()
		processingCounts[group]--
	}

	// Helper to record success
	addSuccess := func(group string) {
		quotaMu.Lock()
		defer quotaMu.Unlock()
		quotaCounts[group]++
	}

	// Create cancellable context to stop pending downloads once we have enough results
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for _, candidate := range candidates {
		// Early check: if we already have enough for this group, don't even spawn/queue
		// Note: This is an optimization; the worker also checks.
		if !needsMore(candidate.Group) {
			continue
		}

		wg.Add(1)
		go func(cand triage.Candidate) {
			defer wg.Done()
			
			// Acquire semaphore (respect context)
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return // Abort if cancelled while waiting
			}
			
			// Double-check: Do we still need this group?
			// (Another worker might have finished while we were waiting)
			if !needsMore(cand.Group) {
				return
			}
			
			// Mark as processing to block other workers from picking up this group unnecessarily
			startProcessing(cand.Group)
			defer endProcessing(cand.Group) // Ensure we decrement even on panic/return
			
			// Use candidate's result item
			item := cand.Result
			
			// Download NZB
			var nzbData []byte
			var err error

			if item.SourceIndexer != nil {
				// Use the specific indexer that found this item (Load Balancing)
				nzbData, err = item.SourceIndexer.DownloadNZB(item.Link)
			} else {
				// Fallback to default indexer/aggregator
				nzbData, err = s.indexer.DownloadNZB(item.Link)
			}

			if err != nil {
				logger.Error("Failed to download NZB", "title", item.Title, "err", err)
				// Don't report error to channel to keep it clean, just return
				return
			}
			
			// Parse NZB
			nzbParsed, err := nzb.Parse(bytes.NewReader(nzbData))
			if err != nil {
				logger.Error("Failed to parse NZB", "title", item.Title, "err", err)
				return
			}
			
			// Validate availability via HEAD check
			// Pass cancelable context
			validationResults := s.validator.ValidateNZB(ctx, nzbParsed)
			if len(validationResults) == 0 {
				return
			}
			
			// Find best provider
			bestResult := validation.GetBestProvider(validationResults)
			if bestResult == nil {
				return
			}

			// Async report to AvailNZB
			go func() {
				nzbID := nzbParsed.CalculateID()
				if nzbID != "" {
					_ = s.availClient.ReportAvailability(nzbID, bestResult.Host, true)
				}
			}()

			// Create session ID from NZB hash
			sessionID := nzbParsed.Hash()
			
			// Store NZB in session manager
			_, err = s.sessionManager.CreateSession(sessionID, nzbParsed)
			if err != nil {
				return
			}
			
			// Create stream URL
			streamURL := fmt.Sprintf("%s/play/%s", s.baseURL, sessionID)
			
			// Determine size
			sizeGB := float64(nzbParsed.TotalSize()) / (1024 * 1024 * 1024)

			// Build rich stream metadata from PTT
			stream := buildStreamMetadata(streamURL, item.Title, cand, sizeGB, nzbParsed.TotalSize())

			// Record success
			addSuccess(cand.Group)

			logger.Debug("Created stream", "name", stream.Name, "url", stream.URL)
			resultChan <- nzbResult{stream: stream, group: cand.Group}
		}(candidate)
	}
	
	// Close result channel when all goroutines finish
	go func() {
		wg.Wait()
		close(resultChan)
	}()
	
	// Collect results with Early Exit
	var streams []Stream
	
	// Monitor loop to stop completely if ALL quotas are met
	// We check current quotas from `quotaCounts`?
	// The resultChan receive is still useful to build the final list.
	
	for result := range resultChan {
		if result.err == nil {
			streams = append(streams, result.stream)
			
			// Check global completion
			// We can check the shared state (it's updated by workers)
			quotaMu.Lock()
			has4k := quotaCounts["4k"] >= quotaPerGroup
			has1080p := quotaCounts["1080p"] >= quotaPerGroup
			has720p := quotaCounts["720p"] >= quotaPerGroup
			quotaMu.Unlock()
			
			if has4k && has1080p && has720p {
				logger.Debug("Fast-Path: All quotas met. Cancelling checks.")
				cancel() // Stop others!
				break // Return immediately
			}
		}
	}
	
	// Group valid streams by resolution bucket
	buckets := make(map[string][]Stream)
	for _, s := range streams {
		// Determine bucket based on name/description (re-using logic or simple string check)
		bucket := "sd"
		nameLower := strings.ToLower(s.Name)
		descLower := strings.ToLower(s.Description)
		
		if strings.Contains(nameLower, "4k") || strings.Contains(nameLower, "2160") || strings.Contains(descLower, "2160") {
			bucket = "4k"
		} else if strings.Contains(nameLower, "1080") || strings.Contains(descLower, "1080") {
			bucket = "1080p"
		} else if strings.Contains(nameLower, "720") || strings.Contains(descLower, "720") {
			bucket = "720p"
		}
		
		buckets[bucket] = append(buckets[bucket], s)
	}

	// Select top N from each bucket (redundant if quota worked, but safe)
	var finalStreams []Stream
	priorities := []string{"4k", "1080p", "720p", "sd"}
	
	for _, bucketName := range priorities {
		bucketStreams := buckets[bucketName]
		
		// Sort by quality score within bucket to get best ones
		sort.Slice(bucketStreams, func(i, j int) bool {
			return getQualityScore(bucketStreams[i].Name) > getQualityScore(bucketStreams[j].Name)
		})
		
		finalStreams = append(finalStreams, bucketStreams...)
	}

	// Final sort by quality for display
	sort.Slice(finalStreams, func(i, j int) bool {
		return getQualityScore(finalStreams[i].Name) > getQualityScore(finalStreams[j].Name)
	})
	
	logger.Info("Returning validated streams", "count", len(finalStreams))
	return finalStreams, nil
}

// extractFilenameFromSubject extracts filename from NZB subject line
func extractFilenameFromSubject(subject string) string {
	// Try to find quoted filename
	if start := strings.Index(subject, "\""); start != -1 {
		if end := strings.Index(subject[start+1:], "\""); end != -1 {
			return subject[start+1 : start+1+end]
		}
	}
	
	// Fallback: extract before yEnc or (1/50) pattern
	subject = strings.TrimSpace(subject)
	if idx := strings.Index(subject, " yEnc"); idx != -1 {
		subject = subject[:idx]
	}
	if idx := strings.Index(subject, " ("); idx != -1 {
		subject = subject[:idx]
	}
	
	return strings.Trim(subject, "\"' ")
}

// handlePlay handles playback requests - serves actual video content
func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimPrefix(r.URL.Path, "/play/")
	
	logger.Info("Play request", "session", sessionID)
	
	// Get session
	sess, err := s.sessionManager.GetSession(sessionID)
	if err != nil {
		logger.Error("Session not found", "err", err)
		forceDisconnect(w)
		return
	}
	
	// Get files from session
	files := sess.Files
	if len(files) == 0 {
		// Fallback to single file if Files not populated (legacy)
		if sess.File != nil {
			files = []*loader.File{sess.File}
		} else {
			logger.Error("No files in session", "id", sessionID)
			forceDisconnect(w)
			return
		}
	}
	
	// Get media stream (handles RAR, 7z, and direct files)
	// Pass cached blueprint if available
	stream, name, size, bp, err := unpack.GetMediaStream(files, sess.Blueprint)
	if err != nil {
		logger.Error("Failed to open media stream", "id", sessionID, "err", err)
		forceDisconnect(w)
		return
	}
	
	// Cache the blueprint if new one returned
	if bp != nil && sess.Blueprint == nil {
		sess.SetBlueprint(bp)
	}
	
	// Track active playback
	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if clientIP == "" {
		clientIP = r.RemoteAddr
	}
	s.sessionManager.StartPlayback(sessionID, clientIP)
	defer s.sessionManager.EndPlayback(sessionID, clientIP)

	// Wrap stream with monitor to keep session alive during playback
	monitoredStream := &StreamMonitor{
		ReadSeekCloser: stream,
		sessionID:      sessionID,
		clientIP:       clientIP,
		manager:        s.sessionManager,
		lastUpdate:     time.Now(),
	}
	defer monitoredStream.Close()

	logger.Info("Serving media", "name", name, "size", size, "session", sessionID)
	
	// Set headers
	w.Header().Set("Content-Type", "video/mp4") // Stremio often prefers this or generic buffer
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	
	// Handle streaming using standard library ServeContent
	// This automatically handles Range requests, HEAD requests, and efficient copying.
	http.ServeContent(w, r, name, time.Time{}, monitoredStream)
	
	// Log completion (ServeContent blocks until done)
	logger.Debug("Finished serving media", "session", sessionID)
}

// handleHealth serves health check endpoint
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
		"addon":  "streamnzb",
	})
}

// StreamResponse represents the response to a stream request
type StreamResponse struct {
	Streams []Stream `json:"streams"`
}

// Stream represents a single stream option
type Stream struct {
	// URL for direct streaming (HTTP video file)
	URL string `json:"url,omitempty"`
	
	// ExternalUrl for external player (alternative to URL)
	ExternalUrl string `json:"externalUrl,omitempty"`
	
	// Display name in Stremio
	Name string `json:"name,omitempty"`
	
	// Optional metadata (shown in Stremio UI)
	Title       string            `json:"title,omitempty"`
	Description string            `json:"description,omitempty"`
	BehaviorHints *BehaviorHints  `json:"behaviorHints,omitempty"`
}

// BehaviorHints provides hints to Stremio about stream behavior
type BehaviorHints struct {
	NotWebReady      bool   `json:"notWebReady,omitempty"`
	BingeGroup       string `json:"bingeGroup,omitempty"`
	CountryWhitelist []string `json:"countryWhitelist,omitempty"`
	VideoSize        int64  `json:"videoSize,omitempty"`
	Filename         string `json:"filename,omitempty"`
}

// getQualityScore assigns a score for sorting (higher = better quality)
func getQualityScore(name string) int {
	nameLower := strings.ToLower(name)
	
	// Resolution scoring (primary)
	score := 0
	if strings.Contains(nameLower, "4k") || strings.Contains(nameLower, "2160p") {
		score += 4000
	} else if strings.Contains(nameLower, "1080p") {
		score += 3000
	} else if strings.Contains(nameLower, "720p") {
		score += 2000
	} else {
		score += 1000 // SD
	}
	
	// Source quality bonus
	if strings.Contains(nameLower, "remux") {
		score += 500
	} else if strings.Contains(nameLower, "bluray") {
		score += 400
	} else if strings.Contains(nameLower, "web-dl") || strings.Contains(nameLower, "web") {
		score += 300
	} else if strings.Contains(nameLower, "webrip") {
		score += 200
	}
	
	// HDR bonus
	if strings.Contains(nameLower, "hdr") || strings.Contains(nameLower, "dv") {
		score += 100
	}
	
	// Atmos/TrueHD bonus
	if strings.Contains(nameLower, "atmos") {
		score += 50
	}
	if strings.Contains(nameLower, "truehd") {
		score += 40
	}
	
	return score
}

// forceDisconnect redirects to a verified valid public video file.
// If Stremio plays this video, it finishes naturally and closes the player.
// We use Big Buck Bunny (10s) because it is a proven reliable URL that Stremio accepts.
// TODO: Replace with a smaller, hosted "Stream Unavailable" video if possible.
var errorVideoURL = "https://test-videos.co.uk/vids/bigbuckbunny/mp4/h264/360/Big_Buck_Bunny_360_10s_1MB.mp4"

func forceDisconnect(w http.ResponseWriter) {
	logger.Info("Redirecting to error video", "url", errorVideoURL)

	w.Header().Set("Connection", "close")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	http.Redirect(w, &http.Request{Method: "GET"}, errorVideoURL, http.StatusTemporaryRedirect)
}

// Reload updates the server components at runtime
func (s *Server) Reload(baseURL string, indexer indexer.Indexer, validator *validation.Checker, 
	triage *triage.Service, avail *availnzb.Client, tmdbClient *tmdb.Client, securityToken string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	actualBaseURL := baseURL
	if securityToken != "" {
		if !strings.HasSuffix(actualBaseURL, "/") {
			actualBaseURL += "/"
		}
		actualBaseURL += securityToken
	}

	s.baseURL = actualBaseURL
	s.indexer = indexer
	s.validator = validator
	s.triageService = triage
	s.availClient = avail
	s.tmdbClient = tmdbClient
	s.securityToken = securityToken
	// Note: sessionManager pools are updated separately via sessionManager.UpdatePools
}

// StreamMonitor wraps an io.ReadSeekCloser to provide keep-alive updates
type StreamMonitor struct {
	io.ReadSeekCloser
	sessionID  string
	clientIP   string
	manager    *session.Manager
	lastUpdate time.Time
	mu         sync.Mutex // Protect lastUpdate to be safe, though Read is usually serial
}

func (s *StreamMonitor) Read(p []byte) (n int, err error) {
	n, err = s.ReadSeekCloser.Read(p)
	
	// Non-blocking update check
	// We don't want to lock on every read, so just check time occasionally
	if time.Since(s.lastUpdate) > 10*time.Second {
		s.mu.Lock()
		if time.Since(s.lastUpdate) > 10*time.Second {
			s.manager.KeepAlive(s.sessionID, s.clientIP)
			s.lastUpdate = time.Now()
		}
		s.mu.Unlock()
	}
	
	return n, err
}

