package stremio

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/availnzb"
	"streamnzb/pkg/config"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/loader"
	"streamnzb/pkg/logger"
	"streamnzb/pkg/nzb"
	"streamnzb/pkg/session"
	"streamnzb/pkg/tmdb"
	"streamnzb/pkg/triage"
	"streamnzb/pkg/tvdb"
	"streamnzb/pkg/unpack"
	"streamnzb/pkg/validation"
)

// Server represents the Stremio addon HTTP server
type Server struct {
	mu             sync.RWMutex
	manifest       *Manifest
	version        string // raw version for API/frontend (e.g. dev-9a3e479)
	baseURL        string
	config         *config.Config
	indexer        indexer.Indexer
	validator      *validation.Checker
	sessionManager *session.Manager
	triageService  *triage.Service
	availClient    *availnzb.Client
	tmdbClient     *tmdb.Client
	tvdbClient     *tvdb.Client
	deviceManager  *auth.DeviceManager
	webHandler     http.Handler
	apiHandler     http.Handler
}

// NewServer creates a new Stremio addon server
func NewServer(cfg *config.Config, baseURL string, port int, indexer indexer.Indexer, validator *validation.Checker,
	sessionMgr *session.Manager, triageService *triage.Service, availClient *availnzb.Client,
	tmdbClient *tmdb.Client, tvdbClient *tvdb.Client, deviceManager *auth.DeviceManager, version string) (*Server, error) {

	if version == "" {
		version = "dev"
	}
	s := &Server{
		manifest:       NewManifest(version),
		version:        version,
		baseURL:        baseURL,
		config:         cfg,
		indexer:        indexer,
		validator:      validator,
		sessionManager: sessionMgr,
		triageService:  triageService,
		availClient:    availClient,
		tmdbClient:     tmdbClient,
		tvdbClient:     tvdbClient,
		deviceManager:  deviceManager,
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

// Version returns the raw version for API/frontend (e.g. dev-9a3e479)
func (s *Server) Version() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.version != "" {
		return s.version
	}
	return "dev"
}

// SetupRoutes configures HTTP routes for the addon
func (s *Server) SetupRoutes(mux *http.ServeMux) {
	// Root handler for manifest and other routes
	// We use a custom handler to handle the optional token prefix
	finalHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		deviceManager := s.deviceManager
		webHandler := s.webHandler
		apiHandler := s.apiHandler
		s.mu.RUnlock()

		path := r.URL.Path
		var authenticatedDevice *auth.Device

		// Serve embedded error video directly - bypass token logic so /error/... is never treated as a device token
		if path == "/error/failure.mp4" && webHandler != nil {
			webHandler.ServeHTTP(w, r)
			return
		}

		// Determine if this is a Stremio route that requires device token
		isStremioRoute := path == "/manifest.json" || strings.HasPrefix(path, "/stream/") || strings.HasPrefix(path, "/play/") || strings.HasPrefix(path, "/debug/play")

		// Root path "/" and web UI routes are always accessible (no token required)
		// Only Stremio routes require device tokens in the path

		// Check for device token in path (only if path has a token segment)
		trimmedPath := strings.TrimPrefix(path, "/")
		parts := strings.SplitN(trimmedPath, "/", 2)

		if len(parts) >= 1 && parts[0] != "" {
			token := parts[0]

			// Try to authenticate as a device token
			if deviceManager != nil {
				device, err := deviceManager.AuthenticateToken(token)
				if err == nil && device != nil {
					authenticatedDevice = device
					// Strip token from path for internal routing
					if len(parts) > 1 {
						path = "/" + parts[1]
					} else {
						path = "/"
					}
					r.URL.Path = path
					// Store device in context for handlers to use
					r = r.WithContext(auth.ContextWithDevice(r.Context(), device))
				} else if isStremioRoute {
					// Token in path but doesn't match any device, and this is a Stremio route - unauthorized
					logger.Error("Unauthorized request - invalid device token", "path", path, "remote", r.RemoteAddr)
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
					return
				}
				// If token doesn't match but it's not a Stremio route, continue (might be web UI route like /login)
			}
		} else if isStremioRoute {
			// Stremio routes require device token in path
			logger.Error("Unauthorized request - Stremio route requires device token", "path", path, "remote", r.RemoteAddr)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		// If no token in path and not a Stremio route, allow access (for web UI routes like /, /login, and API routes which use cookies/headers)

		// Internal routing
		if path == "/manifest.json" {
			s.handleManifest(w, r)
		} else if strings.HasPrefix(path, "/stream/") {
			s.handleStream(w, r, authenticatedDevice)
		} else if strings.HasPrefix(path, "/play/") {
			s.handlePlay(w, r, authenticatedDevice)
		} else if strings.HasPrefix(path, "/debug/play") {
			s.handleDebugPlay(w, r, authenticatedDevice)
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

	// Configure button (behaviorHints.configurable) only for admin users
	device, _ := auth.DeviceFromContext(r)
	isAdmin := device != nil && device.Username == "admin"

	data, err := manifest.ToJSONForDevice(isAdmin)
	if err != nil {
		http.Error(w, "Failed to generate manifest", http.StatusInternalServerError)
		return
	}

	w.Write(data)
}

// handleStream handles stream requests
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request, device *auth.Device) {
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

	logger.Info("Stream request", "type", contentType, "id", id, "device", func() string {
		if device != nil {
			return device.Username
		}
		return "legacy"
	}())

	// Search NZBHydra2
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	streams, err := s.searchAndValidate(ctx, contentType, id, device)
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

func (s *Server) searchAndValidate(ctx context.Context, contentType, id string, device *auth.Device) ([]Stream, error) {
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

		// Attempt to resolve TVDB ID for better indexer results (TVDB first, TMDB as fallback)
		if req.IMDbID != "" && req.TVDBID == "" {
			var tvdbID string
			var err error
			if s.tvdbClient != nil {
				tvdbID, err = s.tvdbClient.ResolveTVDBID(req.IMDbID)
				if err != nil {
					logger.Debug("TVDB failed to resolve TVDB ID, trying TMDB fallback", "imdb", req.IMDbID, "err", err)
				}
			}
			if (tvdbID == "" || err != nil) && s.tmdbClient != nil {
				tvdbID, err = s.tmdbClient.ResolveTVDBID(req.IMDbID)
				if err != nil {
					logger.Debug("Failed to resolve TVDB ID", "err", err)
				}
			}
			if tvdbID != "" {
				req.TVDBID = tvdbID
				req.IMDbID = ""
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

	// Filter results using Triage Service with device-specific filters if available
	var candidates []triage.Candidate
	if device != nil && device.Username != "admin" {
		// Check if device has custom filters/sorting
		hasCustomFilters := len(device.Filters.AllowedQualities) > 0 ||
			len(device.Filters.BlockedQualities) > 0 ||
			device.Filters.MinResolution != "" ||
			device.Filters.MaxResolution != "" ||
			len(device.Filters.AllowedCodecs) > 0 ||
			len(device.Filters.BlockedCodecs) > 0 ||
			len(device.Filters.RequiredAudio) > 0 ||
			len(device.Filters.AllowedAudio) > 0 ||
			device.Filters.MinChannels != "" ||
			device.Filters.RequireHDR ||
			len(device.Filters.AllowedHDR) > 0 ||
			len(device.Filters.BlockedHDR) > 0 ||
			device.Filters.BlockSDR ||
			len(device.Filters.RequiredLanguages) > 0 ||
			len(device.Filters.AllowedLanguages) > 0 ||
			device.Filters.BlockDubbed ||
			device.Filters.BlockCam ||
			device.Filters.RequireProper ||
			!device.Filters.AllowRepack ||
			device.Filters.BlockHardcoded ||
			device.Filters.MinBitDepth != "" ||
			device.Filters.MinSizeGB > 0 ||
			device.Filters.MaxSizeGB > 0 ||
			len(device.Filters.BlockedGroups) > 0

		hasCustomSorting := len(device.Sorting.ResolutionWeights) > 0 ||
			len(device.Sorting.CodecWeights) > 0 ||
			len(device.Sorting.AudioWeights) > 0 ||
			len(device.Sorting.QualityWeights) > 0 ||
			len(device.Sorting.VisualTagWeights) > 0 ||
			device.Sorting.GrabWeight != 0 ||
			device.Sorting.AgeWeight != 0 ||
			len(device.Sorting.PreferredGroups) > 0 ||
			len(device.Sorting.PreferredLanguages) > 0

		if hasCustomFilters || hasCustomSorting {
			// Use device's custom filters/sorting
			deviceTriageService := triage.NewService(&device.Filters, device.Sorting)
			candidates = deviceTriageService.Filter(searchResp.Channel.Items)
			logger.Debug("Selected candidates after device-specific triage", "count", len(candidates), "device", device.Username)
		} else {
			// Device has no custom config, use global defaults
			candidates = s.triageService.Filter(searchResp.Channel.Items)
			logger.Debug("Selected candidates after triage (using global config)", "count", len(candidates), "device", device.Username)
		}
	} else {
		// Use default triage service (global config)
		candidates = s.triageService.Filter(searchResp.Channel.Items)
		logger.Debug("Selected candidates after triage", "count", len(candidates))
	}

	// Single semaphore with 6 concurrent slots (API-friendly)
	sem := make(chan struct{}, 6)
	resultChan := make(chan Stream, len(candidates))

	// Track validation progress
	var mu sync.Mutex
	validated := 0 // Successful validations
	attempted := 0 // Total validation attempts
	maxToValidate := s.config.MaxStreams
	if maxToValidate <= 0 {
		maxToValidate = 6 // Fallback default
	}
	maxAttempts := maxToValidate * 2 // Auto-calculate safety limit

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup

	for _, candidate := range candidates {
		// Pre-check: stop launching new goroutines if we've already hit attempt limit
		mu.Lock()
		if attempted >= maxAttempts {
			mu.Unlock()
			logger.Debug("Hit attempt limit, stopping launch", "attempted", attempted, "maxAttempts", maxAttempts)
			break
		}
		attempted++ // Count this attempt
		mu.Unlock()

		wg.Add(1)
		go func(cand triage.Candidate) {
			defer wg.Done()

			// Acquire semaphore (respect context)
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			// Check if we've already hit the validated limit (after acquiring semaphore)
			mu.Lock()
			if validated >= maxToValidate {
				mu.Unlock()
				logger.Debug("Already hit validation limit, skipping", "validated", validated, "maxToValidate", maxToValidate)
				cancel() // Cancel remaining goroutines
				return
			}
			mu.Unlock()

			// Validate candidate (pass device to include token in URL)
			stream, err := s.validateCandidate(ctx, cand, device)
			if err == nil {
				mu.Lock()
				// Double-check limit before adding (in case multiple goroutines validated simultaneously)
				if validated < maxToValidate {
					resultChan <- stream
					validated++ // Only count successful validations

					// If we just hit the limit, cancel remaining work
					if validated >= maxToValidate {
						logger.Debug("Hit validation limit, canceling remaining", "validated", validated, "maxToValidate", maxToValidate)
						cancel()
					}
				}
				mu.Unlock()
			}
		}(candidate)
	}

	// Close result channel when all goroutines finish (with timeout to prevent hanging)
	done := make(chan struct{})
	channelClosed := false
	var channelMu sync.Mutex

	go func() {
		wg.Wait()
		channelMu.Lock()
		if !channelClosed {
			close(resultChan) // Close channel when all goroutines finish
			channelClosed = true
		}
		channelMu.Unlock()
		close(done)
	}()

	// Collect all successful results with timeout
	var streams []Stream
	timeout := time.After(60 * time.Second)

	collecting := true
	for collecting {
		select {
		case stream, ok := <-resultChan:
			if !ok {
				// Channel closed, all results collected
				collecting = false
			} else {
				streams = append(streams, stream)
			}
		case <-timeout:
			// Timeout after 60 seconds to prevent hanging
			logger.Warn("Stream collection timeout, returning partial results", "count", len(streams))
			cancel() // Cancel any remaining work
			// Close channel to unblock any goroutines waiting to write
			channelMu.Lock()
			if !channelClosed {
				close(resultChan)
				channelClosed = true
			}
			channelMu.Unlock()
			collecting = false
		case <-ctx.Done():
			// Context cancelled, return what we have
			logger.Debug("Stream collection cancelled, returning partial results", "count", len(streams))
			// Close channel to unblock any goroutines waiting to write
			channelMu.Lock()
			if !channelClosed {
				close(resultChan)
				channelClosed = true
			}
			channelMu.Unlock()
			collecting = false
		}
	}

	// Wait briefly for goroutines to finish (non-blocking)
	select {
	case <-done:
		// All goroutines finished
	case <-time.After(1 * time.Second):
		// Don't wait forever, just log warning
		logger.Warn("Some validation goroutines may still be running")
	}

	// Sort by quality score for display
	sort.Slice(streams, func(i, j int) bool {
		return getQualityScore(streams[i].Name) > getQualityScore(streams[j].Name)
	})

	logger.Info("Returning validated streams", "count", len(streams))
	return streams, nil
}

// validateCandidate validates a single candidate and returns a stream
func (s *Server) validateCandidate(ctx context.Context, cand triage.Candidate, device *auth.Device) (Stream, error) {
	item := cand.Result

	// Get indexer name for AvailNZB
	var indexerName string
	if item.SourceIndexer != nil {
		if item.ActualIndexer != "" {
			indexerName = item.ActualIndexer
		} else {
			indexerName = item.SourceIndexer.Name()
			// Prowlarr indexers are often named "Prowlarr: IndexerName"
			if strings.HasPrefix(indexerName, "Prowlarr:") {
				indexerName = strings.TrimSpace(strings.TrimPrefix(indexerName, "Prowlarr:"))
			}
		}
	}

	// Check AvailNZB for pre-download validation
	skipValidation := false
	providerHosts := s.validator.GetProviderHosts()

	if indexerName != "" && len(providerHosts) > 0 {
		guidToCheck := item.GUID
		if item.ActualGUID != "" {
			guidToCheck = item.ActualGUID
		}

		nzbID, isHealthy, lastUpdated, _, err := s.availClient.CheckPreDownload(indexerName, guidToCheck, providerHosts)
		if err == nil && nzbID != "" {
			if isHealthy {
				skipValidation = true
			} else {
				// Skip if recently reported as unhealthy
				if time.Since(lastUpdated) <= 24*time.Hour {
					return Stream{}, fmt.Errorf("recently reported unhealthy")
				}
			}
		}
	}

	var sessionID string
	var streamSize int64

	if skipValidation {
		// DEFERRED (Lazy) - Trust AvailNZB
		sessionID = fmt.Sprintf("%x", md5.Sum([]byte(item.GUID)))
		streamSize = item.Size

		if streamSize == 0 {
			logger.Warn("Indexer did not provide file size", "title", item.Title, "indexer", indexerName)
		}

		logger.Info("Deferring NZB download (Lazy)", "title", item.Title, "session_id", sessionID)

		_, err := s.sessionManager.CreateDeferredSession(
			sessionID,
			item.Link,
			indexerName,
			item.Title,
			item.SourceIndexer,
			item.GUID,
		)
		if err != nil {
			return Stream{}, fmt.Errorf("failed to create deferred session: %w", err)
		}
	} else {
		// IMMEDIATE - Download and validate
		logger.Info("Downloading NZB for validation", "title", item.Title)

		// Download NZB
		var nzbData []byte
		var err error

		if item.SourceIndexer != nil {
			nzbData, err = item.SourceIndexer.DownloadNZB(item.Link)
		} else {
			nzbData, err = s.indexer.DownloadNZB(item.Link)
		}

		if err != nil {
			return Stream{}, fmt.Errorf("failed to download NZB: %w", err)
		}

		// Parse NZB
		nzbParsed, err := nzb.Parse(bytes.NewReader(nzbData))
		if err != nil {
			return Stream{}, fmt.Errorf("failed to parse NZB: %w", err)
		}

		streamSize = nzbParsed.TotalSize()
		sessionID = nzbParsed.Hash()

		// Validate availability
		validationResults := s.validator.ValidateNZB(ctx, nzbParsed)
		if len(validationResults) == 0 {
			return Stream{}, fmt.Errorf("no valid providers")
		}

		bestResult := validation.GetBestProvider(validationResults)
		if bestResult == nil {
			return Stream{}, fmt.Errorf("no best provider")
		}

		// Async report to AvailNZB
		go func() {
			nzbID := nzbParsed.CalculateID()
			if nzbID != "" {
				_ = s.availClient.ReportAvailability(nzbID, bestResult.Host, true, indexerName, item.GUID)
			}
		}()

		// Store NZB in session manager
		_, err = s.sessionManager.CreateSession(sessionID, nzbParsed, item.GUID)
		if err != nil {
			return Stream{}, fmt.Errorf("failed to create session: %w", err)
		}
	}

	// Create stream URL (always include device token if device is present)
	// Admin and all devices need token in URL for proper routing
	var streamURL string
	token := ""
	if device != nil {
		token = device.Token
		// Include device token in URL path: /{token}/play/{sessionID}
		streamURL = fmt.Sprintf("%s/%s/play/%s", s.baseURL, token, sessionID)
	}
	sizeGB := float64(streamSize) / (1024 * 1024 * 1024)

	// Build stream metadata
	stream := buildStreamMetadata(streamURL, item.Title, cand, sizeGB, streamSize)

	logger.Debug("Created stream", "name", stream.Name, "url", stream.URL)
	return stream, nil
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
func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request, device *auth.Device) {
	sessionID := strings.TrimPrefix(r.URL.Path, "/play/")

	logger.Info("Play request", "session", sessionID)

	// Get session
	// Get session
	sess, err := s.sessionManager.GetSession(sessionID)
	if err != nil {
		http.Error(w, "Session expired or not found", http.StatusNotFound)
		return
	}

	// Lazy Load NZB if needed
	_, err = sess.GetOrDownloadNZB(s.sessionManager)
	if err != nil {
		logger.Error("Failed to lazy load NZB", "id", sessionID, "err", err)
		http.Error(w, "Failed to load NZB content", http.StatusInternalServerError)
		return
	}

	// Track active playback
	s.sessionManager.StartPlayback(sessionID, r.RemoteAddr)
	defer s.sessionManager.EndPlayback(sessionID, r.RemoteAddr)

	// Get files from session
	files := sess.Files
	if len(files) == 0 {
		// Fallback to single file if Files not populated (legacy)
		if sess.File != nil {
			files = []*loader.File{sess.File}
		} else {
			logger.Error("No files in session", "id", sessionID)
			// Invalidate validation cache
			if sess.NZB != nil {
				s.validator.InvalidateCache(sess.NZB.Hash())
			}
			forceDisconnect(w, s.baseURL)
			return
		}
	}

	// Get media stream (handles RAR, 7z, and direct files)
	// Pass cached blueprint if available
	stream, name, size, bp, err := unpack.GetMediaStream(files, sess.Blueprint)
	if err != nil {
		logger.Error("Failed to open media stream", "id", sessionID, "err", err)

		// Report as Bad to AvailNZB if it's a structural issue (like compression)
		if strings.Contains(err.Error(), "compressed") || strings.Contains(err.Error(), "encrypted") || strings.Contains(err.Error(), "EOF") {
			logger.Info("Reporting bad/unstreamable release to AvailNZB", "id", sessionID, "reason", err.Error())
			go func() {
				// We need NZB ID for reporting.
				var nzbID string
				if sess.NZB != nil {
					nzbID = sess.NZB.CalculateID()
				}

				if nzbID != "" && sess.GUID != "" {
					_ = s.availClient.ReportAvailability(nzbID, "ALL", false, sess.IndexerName, sess.GUID)
				}
			}()
		}

		// Invalidate validation cache so we don't keep serving this bad release
		if sess.NZB != nil {
			s.validator.InvalidateCache(sess.NZB.Hash())
		}

		forceDisconnect(w, s.baseURL)
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

// handleDebugPlay allows playing directly from an NZB URL or local file for debugging
func (s *Server) handleDebugPlay(w http.ResponseWriter, r *http.Request, device *auth.Device) {
	nzbPath := r.URL.Query().Get("nzb")
	if nzbPath == "" {
		http.Error(w, "Missing 'nzb' query parameter (URL or file path)", http.StatusBadRequest)
		return
	}

	logger.Info("Debug Play request", "nzb", nzbPath)

	var nzbData []byte
	var err error

	// Check if it's a local file path (starts with / or drive letter on Windows)
	if strings.HasPrefix(nzbPath, "/") || (len(nzbPath) > 2 && nzbPath[1] == ':') {
		// Local file path
		logger.Debug("Reading NZB from local file", "path", nzbPath)
		nzbData, err = os.ReadFile(nzbPath)
		if err != nil {
			logger.Error("Failed to read local NZB file", "path", nzbPath, "err", err)
			http.Error(w, "Failed to read local NZB file: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		// URL - try indexer download first
		nzbData, err = s.indexer.DownloadNZB(nzbPath)
		if err != nil {
			// Fallback to simple HTTP GET
			resp, httpErr := http.Get(nzbPath)
			if httpErr != nil {
				logger.Error("Failed to download NZB", "url", nzbPath, "err", err, "httpErr", httpErr)
				http.Error(w, "Failed to download NZB: "+err.Error(), http.StatusInternalServerError)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != 200 {
				msg := fmt.Sprintf("Failed to download NZB (HTTP %d)", resp.StatusCode)
				logger.Error(msg, "url", nzbPath)
				http.Error(w, msg, http.StatusInternalServerError)
				return
			}

			nzbData, err = io.ReadAll(resp.Body)
			if err != nil {
				http.Error(w, "Failed to read NZB body", http.StatusInternalServerError)
				return
			}
		}
	}

	// Parse NZB
	nzbParsed, err := nzb.Parse(bytes.NewReader(nzbData))
	if err != nil {
		logger.Error("Failed to parse NZB", "err", err)
		http.Error(w, "Failed to parse NZB", http.StatusInternalServerError)
		return
	}

	// Create Session
	// Use hash of path as ID to allow repeating same path
	sessionID := fmt.Sprintf("debug-%x", nzbPath)
	// Or use NZB hash
	// sessionID := nzbParsed.Hash()

	// Create/Get Session
	sess, err := s.sessionManager.CreateSession(sessionID, nzbParsed, "")
	if err != nil {
		logger.Error("Failed to create session", "err", err)
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	// Get Files
	files := sess.Files
	if len(files) == 0 {
		http.Error(w, "No files in NZB", http.StatusInternalServerError)
		return
	}

	// Get Media Stream (same logic as handlePlay)
	stream, name, size, bp, err := unpack.GetMediaStream(files, sess.Blueprint)
	if err != nil {
		logger.Error("Failed to open media stream", "err", err)
		http.Error(w, "Failed to open media stream: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if bp != nil && sess.Blueprint == nil {
		sess.SetBlueprint(bp)
	}

	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if clientIP == "" {
		clientIP = r.RemoteAddr
	}

	s.sessionManager.StartPlayback(sessionID, clientIP)
	defer s.sessionManager.EndPlayback(sessionID, clientIP)

	monitoredStream := &StreamMonitor{
		ReadSeekCloser: stream,
		sessionID:      sessionID,
		clientIP:       clientIP,
		manager:        s.sessionManager,
		lastUpdate:     time.Now(),
	}
	defer monitoredStream.Close()

	logger.Info("Serving debug media", "name", name, "size", size)
	logger.Debug("HTTP Request", "method", r.Method, "range", r.Header.Get("Range"), "user_agent", r.Header.Get("User-Agent"))

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, name, time.Time{}, monitoredStream)

	logger.Debug("Finished serving debug media")
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
	Title         string         `json:"title,omitempty"`
	Description   string         `json:"description,omitempty"`
	BehaviorHints *BehaviorHints `json:"behaviorHints,omitempty"`
}

// BehaviorHints provides hints to Stremio about stream behavior
type BehaviorHints struct {
	NotWebReady      bool     `json:"notWebReady,omitempty"`
	BingeGroup       string   `json:"bingeGroup,omitempty"`
	CountryWhitelist []string `json:"countryWhitelist,omitempty"`
	VideoSize        int64    `json:"videoSize,omitempty"`
	Filename         string   `json:"filename,omitempty"`
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

	// Visual tag bonus (HDR/3D)
	if strings.Contains(nameLower, "hdr") || strings.Contains(nameLower, "dv") || strings.Contains(nameLower, "3d") {
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

// forceDisconnect redirects to the embedded failure video when streaming is unavailable.
// The video is packaged with the binary and served from /error/failure.mp4.
func forceDisconnect(w http.ResponseWriter, baseURL string) {
	errorVideoURL := strings.TrimSuffix(baseURL, "/") + "/error/failure.mp4"
	logger.Info("Redirecting to error video", "url", errorVideoURL)

	w.Header().Set("Connection", "close")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	http.Redirect(w, &http.Request{Method: "GET"}, errorVideoURL, http.StatusTemporaryRedirect)
}

// Reload updates the server components at runtime
func (s *Server) Reload(baseURL string, indexer indexer.Indexer, validator *validation.Checker,
	triage *triage.Service, avail *availnzb.Client, tmdbClient *tmdb.Client, tvdbClient *tvdb.Client, deviceManager *auth.DeviceManager) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.baseURL = baseURL
	s.indexer = indexer
	s.validator = validator
	s.triageService = triage
	s.availClient = avail
	s.tmdbClient = tmdbClient
	s.tvdbClient = tvdbClient
	s.deviceManager = deviceManager
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

func (s *StreamMonitor) Close() error {
	if s.ReadSeekCloser != nil {
		return s.ReadSeekCloser.Close()
	}
	return nil
}
