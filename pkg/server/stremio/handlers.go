package stremio

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/media/loader"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/media/unpack"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search"
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvdb"
	"streamnzb/pkg/session"
	"streamnzb/pkg/usenet/validation"
)

// Server represents the Stremio addon HTTP server
type Server struct {
	mu                   sync.RWMutex
	manifest             *Manifest
	version              string // raw version for API/frontend (e.g. dev-9a3e479)
	baseURL              string
	config               *config.Config
	indexer              indexer.Indexer
	validator            *validation.Checker
	sessionManager       *session.Manager
	triageService        *triage.Service
	availClient          *availnzb.Client
	availReporter        *availnzb.Reporter
	availNZBIndexerHosts []string // Underlying indexer hostnames for AvailNZB GetReleases (e.g. nzbgeek.info from NZBHydra)
	tmdbClient           *tmdb.Client
	tvdbClient           *tvdb.Client
	deviceManager        *auth.DeviceManager
	webHandler           http.Handler
	apiHandler           http.Handler
}

// NewServer creates a new Stremio addon server.
// availNZBIndexerHosts is used to filter AvailNZB GetReleases by indexer; pass nil to get all releases.
func NewServer(cfg *config.Config, baseURL string, port int, indexer indexer.Indexer, validator *validation.Checker,
	sessionMgr *session.Manager, triageService *triage.Service, availClient *availnzb.Client,
	availNZBIndexerHosts []string,
	tmdbClient *tmdb.Client, tvdbClient *tvdb.Client, deviceManager *auth.DeviceManager, version string) (*Server, error) {

	if version == "" {
		version = "dev"
	}
	var availReporter *availnzb.Reporter
	if availClient != nil {
		availReporter = availnzb.NewReporter(availClient, validator)
	}
	s := &Server{
		manifest:             NewManifest(version),
		version:              version,
		baseURL:              baseURL,
		config:               cfg,
		indexer:              indexer,
		validator:            validator,
		sessionManager:       sessionMgr,
		triageService:        triageService,
		availClient:          availClient,
		availReporter:        availReporter,
		availNZBIndexerHosts: availNZBIndexerHosts,
		tmdbClient:           tmdbClient,
		tvdbClient:           tvdbClient,
		deviceManager:        deviceManager,
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
				device, err := deviceManager.AuthenticateToken(token, s.config.GetAdminUsername(), s.config.AdminToken)
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
	isAdmin := device != nil && device.Username == s.config.GetAdminUsername()

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

	// Allow time for indexer search (e.g. NZBHydra/Prowlarr) plus NNTP validation across providers.
	// 5s was too short: slow indexers + validation often exceeded it and returned 0 streams.
	const streamRequestTimeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(r.Context(), streamRequestTimeout)
	defer cancel()

	logger.Trace("stream request start", "type", contentType, "id", id)
	streams, err := s.searchAndValidate(ctx, contentType, id, device)
	logger.Trace("stream request searchAndValidate returned", "count", len(streams), "err", err)
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

// addAPIKeyToDownloadURL appends the matching indexer's API key to the download URL (by host). Returns original if no match.
// For Newznab t=get URLs, the API expects parameter "id" (see https://inhies.github.io/Newznab-API/functions/#get);
// if the URL has "guid" but no "id", we set id=guid so indexers that require "id" work.
func addAPIKeyToDownloadURL(downloadURL string, indexers []config.IndexerConfig) string {
	if downloadURL == "" || len(indexers) == 0 {
		return downloadURL
	}
	u, err := url.Parse(downloadURL)
	if err != nil {
		return downloadURL
	}
	q := u.Query()
	if q.Get("t") == "get" && q.Get("id") == "" && q.Get("guid") != "" {
		q.Set("id", q.Get("guid"))
		u.RawQuery = q.Encode()
	}
	downloadHost := strings.ToLower(u.Hostname())
	for _, idx := range indexers {
		idxU, err := url.Parse(idx.URL)
		if err != nil || idx.APIKey == "" {
			continue
		}
		idxHost := strings.ToLower(idxU.Hostname())
		if idxHost == downloadHost ||
			strings.TrimPrefix(idxHost, "api.") == downloadHost ||
			strings.TrimPrefix(downloadHost, "api.") == idxHost {
			q := u.Query()
			q.Set("apikey", idx.APIKey)
			u.RawQuery = q.Encode()
			return u.String()
		}
	}
	return downloadURL
}

// triageCandidates returns filtered+sorted candidates. Devices use their own filters and sorting;
// admin and unauthenticated requests use global config.
func (s *Server) triageCandidates(device *auth.Device, releases []*release.Release) []triage.Candidate {
	if device != nil && device.Username != s.config.GetAdminUsername() {
		ts := triage.NewService(&device.Filters, device.Sorting)
		return ts.Filter(releases)
	}
	return s.triageService.Filter(releases)
}

func (s *Server) searchAndValidate(ctx context.Context, contentType, id string, device *auth.Device) ([]Stream, error) {
	maxStreams := s.config.MaxStreams
	if maxStreams <= 0 {
		maxStreams = 6
	}

	// 1. Build search request and content IDs
	req := indexer.SearchRequest{
		Limit: 1000,
	}

	searchID := id
	if contentType == "series" && strings.Contains(id, ":") {
		parts := strings.Split(id, ":")
		if parts[0] == "tmdb" && len(parts) >= 4 {
			searchID = parts[1]
			req.Season, req.Episode = parts[2], parts[3]
		} else if len(parts) >= 3 {
			searchID = parts[0]
			req.Season, req.Episode = parts[1], parts[2]
		} else if len(parts) > 0 {
			searchID = parts[0]
		}
	} else if strings.HasPrefix(id, "tmdb:") {
		searchID = strings.TrimPrefix(id, "tmdb:")
	}
	if strings.HasPrefix(searchID, "tt") {
		req.IMDbID = searchID
	} else {
		req.TMDBID = searchID
	}
	imdbForText := req.IMDbID
	tmdbForText := req.TMDBID
	if contentType == "series" && strings.Contains(id, ":") {
		parts := strings.Split(id, ":")
		if parts[0] == "tmdb" && len(parts) >= 2 {
			tmdbForText = parts[1]
		}
	}
	if contentType == "movie" {
		req.Cat = "2000"
	} else {
		req.Cat = "5000"
		if req.IMDbID != "" && req.TVDBID == "" {
			if s.tvdbClient != nil {
				if tvdbID, err := s.tvdbClient.ResolveTVDBID(req.IMDbID); err == nil && tvdbID != "" {
					req.TVDBID, req.IMDbID = tvdbID, ""
				}
			}
			if req.TVDBID == "" && s.tmdbClient != nil {
				if tvdbID, err := s.tmdbClient.ResolveTVDBID(req.IMDbID); err == nil && tvdbID != "" {
					req.TVDBID, req.IMDbID = tvdbID, ""
				}
			}
		}
	}
	seasonNum, _ := strconv.Atoi(req.Season)
	episodeNum, _ := strconv.Atoi(req.Episode)
	contentIDs := &session.AvailReportMeta{ImdbID: req.IMDbID, TvdbID: req.TVDBID, Season: seasonNum, Episode: episodeNum}
	if contentType == "movie" && contentIDs.ImdbID == "" && req.TMDBID != "" && s.tmdbClient != nil {
		if tmdbIDNum, err := strconv.Atoi(req.TMDBID); err == nil {
			if extIDs, err := s.tmdbClient.GetExternalIDs(tmdbIDNum, "movie"); err == nil && extIDs.IMDbID != "" {
				contentIDs.ImdbID = extIDs.IMDbID
			}
		}
	}
	// AvailNZB indexer filter: use underlying hostnames (e.g. nzbgeek.info from NZBHydra) so GetReleases returns matches
	availIndexers := s.availNZBIndexerHosts
	logger.Debug("searchAndValidate", "imdb", req.IMDbID, "tvdb", req.TVDBID, "season", req.Season, "ep", req.Episode, "maxStreams", maxStreams)

	var streams []Stream
	seenReleaseTitles := make(map[string]bool)

	// addStream adds a stream if not already present (by normalized release title).
	addStream := func(stream Stream) {
		if stream.Release == nil || stream.Release.Title == "" {
			return
		}
		norm := release.NormalizeTitle(stream.Release.Title)
		if seenReleaseTitles[norm] {
			return
		}
		seenReleaseTitles[norm] = true
		streams = append(streams, stream)
	}

	// Helper function to check if we have enough streams
	// - If per-resolution limiting is disabled (0): only check if we have maxStreams total
	// - If per-resolution limiting is enabled (>0): check if we have enough variety across resolutions
	// Note: streams should be sorted by quality before calling this function
	hasEnoughStreams := func(currentStreams []Stream) bool {
		if len(currentStreams) < maxStreams {
			return false
		}
		// Only check for resolution variety if per-resolution limiting is enabled
		if s.config.MaxStreamsPerResolution > 0 {
			// Make a copy and sort by triage score (limitStreamsPerResolution expects sorted input)
			sorted := make([]Stream, len(currentStreams))
			copy(sorted, currentStreams)
			sort.Slice(sorted, func(i, j int) bool {
				return streamScore(sorted[i]) > streamScore(sorted[j])
			})
			limited := limitStreamsPerResolution(sorted, s.config.MaxStreamsPerResolution, maxStreams)
			// If limiting reduces the count below maxStreams, we need more streams for variety
			if len(limited) < maxStreams {
				return false
			}
		}
		// When disabled: we have enough if we have maxStreams total (no resolution variety check)
		return true
	}

	// AvailNZB: one call for all releases, split streamable vs RAR client-side
	var availResult *availnzb.ReleasesResult
	rarTitles := make(map[string]bool)
	if s.availClient != nil && s.availClient.BaseURL != "" && (contentIDs.ImdbID != "" || contentIDs.TvdbID != "") {
		availResult, _ = s.availClient.GetReleases(contentIDs.ImdbID, contentIDs.TvdbID, contentIDs.Season, contentIDs.Episode, availIndexers, "")
		if availResult != nil {
			for _, rws := range availResult.Releases {
				if rws != nil && strings.ToLower(rws.CompressionType) == "rar" && rws.Release != nil && rws.Release.Title != "" {
					rarTitles[release.NormalizeTitle(rws.Release.Title)] = true
				}
			}
			logger.Debug("AvailNZB releases", "total", len(availResult.Releases), "rar", len(rarTitles))
		}
	}

	// 2. AvailNZB first: streamable only (direct, 7z), filter per configuration
	if availResult != nil && len(availResult.Releases) > 0 {
		var availReleases []*release.Release
		for _, rws := range availResult.Releases {
			if rws == nil || rws.Release == nil || !rws.Available || rws.Release.Link == "" {
				continue
			}
			ct := strings.ToLower(rws.CompressionType)
			if ct == "rar" || (ct != "" && ct != "direct" && ct != "7z") {
				continue
			}
			availReleases = append(availReleases, rws.Release)
		}
		if len(availReleases) > 0 {
			availCandidates := s.triageCandidates(device, availReleases)
			logger.Debug("AvailNZB phase", "releases", len(availReleases), "after_triage", len(availCandidates))

			for _, cand := range availCandidates {
				if cand.Release == nil {
					continue
				}
				rel := cand.Release
				downloadURL := addAPIKeyToDownloadURL(rel.Link, s.config.Indexers)
				sessionID := fmt.Sprintf("%x", md5.Sum([]byte(rel.DetailsURL)))
				_, err := s.sessionManager.CreateDeferredSession(
					sessionID,
					downloadURL,
					rel,
					s.indexer,
					contentIDs,
				)
				if err != nil {
					logger.Debug("AvailNZB deferred session failed", "title", rel.Title, "err", err)
					continue
				}
				var streamURL string
				if device != nil {
					streamURL = fmt.Sprintf("%s/%s/play/%s", s.baseURL, device.Token, sessionID)
				}
				sizeGB := float64(rel.Size) / (1024 * 1024 * 1024)
				displayTitle := rel.Title + "\n[AvailNZB]"
				stream := buildStreamMetadata(streamURL, displayTitle, cand, sizeGB, rel.Size, rel)
				addStream(stream)
			}
			logger.Debug("AvailNZB phase done", "streams", len(streams))

			sort.Slice(streams, func(i, j int) bool {
				return streamScore(streams[i]) > streamScore(streams[j])
			})

			if hasEnoughStreams(streams) {
				knownURLs := make(map[string]bool)
				for _, rel := range availReleases {
					if rel != nil && rel.DetailsURL != "" {
						knownURLs[rel.DetailsURL] = true
					}
				}
				go s.warmAvailNZBCache(context.Background(), req, contentIDs, knownURLs, rarTitles)
			}
		}
	}

	// 3. Indexers: search, triage, validate until we have enough streams
	var indexerCandidatesCount, indexerAttempted int
	if !hasEnoughStreams(streams) {
		indexerReleases, err := search.RunIndexerSearches(s.indexer, s.tmdbClient, req, contentType, contentIDs, imdbForText, tmdbForText)
		if err != nil {
			return nil, err
		}
		candidates := s.triageCandidates(device, indexerReleases)
		indexerCandidatesCount = len(candidates)
		logger.Debug("Indexer candidates after triage", "count", indexerCandidatesCount)

		if availResult != nil && len(availResult.Releases) > 0 {
			ourProviders := make(map[string]bool)
			for _, h := range s.validator.GetProviderHosts() {
				ourProviders[strings.ToLower(h)] = true
			}
			cachedAvailable := make(map[string]bool)
			cachedUnhealthyForUs := make(map[string]bool)
			for _, rws := range availResult.Releases {
				if rws == nil || rws.Release == nil {
					continue
				}
				detailsURL := rws.Release.DetailsURL
				if rws.Available {
					cachedAvailable[detailsURL] = true
				} else if len(ourProviders) > 0 && len(rws.Summary) > 0 {
					ourReported, ourHealthy := 0, 0
					for host, status := range rws.Summary {
						if ourProviders[strings.ToLower(host)] {
							ourReported++
							if status.Healthy {
								ourHealthy++
							}
						}
					}
					if ourReported > 0 && ourHealthy == 0 {
						cachedUnhealthyForUs[detailsURL] = true
					}
				}
			}
			// Filter out known RAR releases (skip validation)
			if len(rarTitles) > 0 {
				before := len(candidates)
				filtered := candidates[:0]
				for _, c := range candidates {
					if c.Release == nil || !rarTitles[release.NormalizeTitle(c.Release.Title)] {
						filtered = append(filtered, c)
					}
				}
				candidates = filtered
				logger.Debug("Filtered RAR releases from indexer candidates", "removed", before-len(candidates), "remaining", len(candidates))
			}
			// Filter out indexer candidates that AvailNZB marks as unhealthy for our providers
			if len(cachedUnhealthyForUs) > 0 {
				before := len(candidates)
				filtered := candidates[:0]
				for _, c := range candidates {
					if c.Release == nil || !cachedUnhealthyForUs[c.Release.DetailsURL] {
						filtered = append(filtered, c)
					}
				}
				candidates = filtered
				logger.Debug("Filtered candidates by AvailNZB unhealthy (our providers)", "removed", before-len(candidates), "remaining", len(candidates))
			}
			if len(cachedAvailable) > 0 {
				sort.SliceStable(candidates, func(i, j int) bool {
					ci := candidates[i].Release != nil && cachedAvailable[candidates[i].Release.DetailsURL]
					cj := candidates[j].Release != nil && cachedAvailable[candidates[j].Release.DetailsURL]
					return ci && !cj
				})
			}
		}

		// Validate candidates in parallel until we have enough streams
		// Limit validation attempts to maxStreams * 2 to avoid excessive downloads
		maxAttempts := maxStreams * 2
		if maxAttempts < 6 {
			maxAttempts = 6
		}
		if maxAttempts > len(candidates) {
			maxAttempts = len(candidates)
		}

		sem := make(chan struct{}, 6)
		resultChan := make(chan Stream, maxAttempts)
		var mu sync.Mutex
		attempted := 0
		validationCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		var wg sync.WaitGroup

		for _, candidate := range candidates {
			mu.Lock()
			if attempted >= maxAttempts {
				mu.Unlock()
				break
			}
			attempted++
			mu.Unlock()

			wg.Add(1)
			go func(cand triage.Candidate) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-validationCtx.Done():
					return
				}

				stream, err := s.validateCandidate(validationCtx, cand, device, contentIDs)
				if err != nil {
					logger.Trace("validateCandidate failed", "title", cand.Release.Title, "err", err)
					return
				}
				resultChan <- stream
			}(candidate)
		}

		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(resultChan)
			close(done)
		}()

		timeout := time.After(60 * time.Second)
		for {
			select {
			case stream, ok := <-resultChan:
				if !ok {
					goto doneCollect
				}
				addStream(stream)
				// Sort streams by triage score before checking (limitStreamsPerResolution expects sorted input)
				sort.Slice(streams, func(i, j int) bool {
					return streamScore(streams[i]) > streamScore(streams[j])
				})
				// Check if we have enough streams after each addition
				if hasEnoughStreams(streams) {
					cancel() // Stop validating more, but collect what's already validated
					// Drain remaining streams
					for {
						select {
						case stream, ok := <-resultChan:
							if !ok {
								goto doneCollect
							}
							streams = append(streams, stream)
						default:
							goto doneCollect
						}
					}
				}
			case <-timeout:
				cancel()
				// Drain remaining streams
				for {
					select {
					case stream, ok := <-resultChan:
						if !ok {
							goto doneCollect
						}
						streams = append(streams, stream)
					default:
						goto doneCollect
					}
				}
			case <-validationCtx.Done():
				// Drain remaining streams
				for {
					select {
					case stream, ok := <-resultChan:
						if !ok {
							goto doneCollect
						}
						streams = append(streams, stream)
					default:
						goto doneCollect
					}
				}
			}
		}
	doneCollect:

		select {
		case <-done:
		case <-time.After(1 * time.Second):
			logger.Warn("Some validation goroutines may still be running")
		}

		indexerAttempted = attempted
	}

	// Final sort all streams by triage score (respects user's priority config)
	sort.Slice(streams, func(i, j int) bool {
		return streamScore(streams[i]) > streamScore(streams[j])
	})

	// Apply limiting once at the end: per-resolution limiting if enabled, otherwise just cap at maxStreams
	logger.Debug("Before limiting", "total_streams", len(streams), "maxStreamsPerResolution", s.config.MaxStreamsPerResolution, "maxStreams", maxStreams)
	// Only apply per-resolution limiting if explicitly enabled (value > 0)
	// When 0 (disabled), just take the best maxStreams streams regardless of resolution
	if s.config.MaxStreamsPerResolution > 0 {
		streams = limitStreamsPerResolution(streams, s.config.MaxStreamsPerResolution, maxStreams)
		logger.Debug("After per-resolution limiting", "count", len(streams))
	} else {
		// Feature disabled: just cap at maxStreams, no resolution variety requirement
		if len(streams) > maxStreams {
			streams = streams[:maxStreams]
		}
		logger.Debug("After maxStreams capping (per-resolution disabled)", "count", len(streams))
	}

	// Placeholder when we have 0 streams but validated only a subset of candidates (e.g. 12/193)
	if len(streams) == 0 && indexerAttempted > 0 && indexerCandidatesCount > indexerAttempted {
		errorVideoURL := strings.TrimSuffix(s.baseURL, "/") + "/error/failure.mp4"
		placeholder := Stream{
			URL:   errorVideoURL,
			Name:  "StreamNZB⚡",
			Title: fmt.Sprintf("0/%d OK\n%d total\nNo streams from first batch.\nRetry to validate more.", indexerAttempted, indexerCandidatesCount),
		}
		streams = []Stream{placeholder}
		logger.Info("Adding placeholder stream", "attempted", indexerAttempted, "candidates", indexerCandidatesCount)
	}

	logger.Info("Returning validated streams", "count", len(streams))
	return streams, nil
}

// warmAvailNZBCache validates one indexer candidate that isn't in AvailNZB with each provider
// and reports all results to AvailNZB (good or bad). Skips releases known to be RAR.
func (s *Server) warmAvailNZBCache(ctx context.Context, req indexer.SearchRequest, contentIDs *session.AvailReportMeta, knownURLs map[string]bool, rarTitles map[string]bool) {
	if s.availClient == nil || s.availClient.BaseURL == "" || (contentIDs.ImdbID == "" && contentIDs.TvdbID == "") {
		return
	}
	providerHosts := s.validator.GetProviderHosts()
	if len(providerHosts) == 0 {
		return
	}
	searchResp, err := s.indexer.Search(req)
	if err != nil {
		logger.Debug("AvailNZB cache warm: search failed", "err", err)
		return
	}
	indexer.NormalizeSearchResponse(searchResp)
	candidates := s.triageCandidates(nil, searchResp.Releases)
	for _, cand := range candidates {
		if cand.Release == nil {
			continue
		}
		detailsURL := cand.Release.DetailsURL
		if detailsURL == "" || knownURLs[detailsURL] || release.IsPrivateReleaseURL(detailsURL) {
			continue
		}
		if rarTitles != nil && rarTitles[release.NormalizeTitle(cand.Release.Title)] {
			continue
		}
		rel := cand.Release
		// 30s for validation/cache warming (NZBHydra proxies can be slow)
		dlCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		var nzbData []byte
		if rel.SourceIndexer != nil {
			if idx, ok := rel.SourceIndexer.(indexer.Indexer); ok {
				nzbData, err = idx.DownloadNZB(dlCtx, rel.Link)
			} else {
				nzbData, err = s.indexer.DownloadNZB(dlCtx, rel.Link)
			}
		} else {
			nzbData, err = s.indexer.DownloadNZB(dlCtx, rel.Link)
		}
		cancel()
		if err != nil {
			continue
		}
		nzbParsed, err := nzb.Parse(bytes.NewReader(nzbData))
		if err != nil {
			continue
		}
		if len(nzbParsed.GetContentFiles()) == 0 {
			streamSize := nzbParsed.TotalSize()
			meta := availnzb.ReportMeta{ReleaseName: rel.Title, Size: streamSize, CompressionType: nzbParsed.CompressionType()}
			meta.ImdbID = contentIDs.ImdbID
			meta.TvdbID = contentIDs.TvdbID
			meta.Season = contentIDs.Season
			meta.Episode = contentIDs.Episode
			for _, providerHost := range providerHosts {
				_ = s.availClient.ReportAvailability(detailsURL, providerHost, false, meta)
			}
			logger.Debug("AvailNZB cache warm: no content files, reported unavailable", "title", rel.Title)
			continue
		}
		streamSize := nzbParsed.TotalSize()
		meta := availnzb.ReportMeta{ReleaseName: rel.Title, Size: streamSize, CompressionType: nzbParsed.CompressionType()}
		meta.ImdbID = contentIDs.ImdbID
		meta.TvdbID = contentIDs.TvdbID
		meta.Season = contentIDs.Season
		meta.Episode = contentIDs.Episode
		// Check each provider and report all results to AvailNZB
		for _, providerHost := range providerHosts {
			result := s.validator.ValidateNZBSingleProvider(ctx, nzbParsed, providerHost)
			available := result.Error == nil && result.Available
			if err := s.availClient.ReportAvailability(detailsURL, result.Host, available, meta); err != nil {
				logger.Debug("AvailNZB cache warm: report failed", "title", rel.Title, "provider", providerHost, "err", err)
				continue
			}
			logger.Debug("AvailNZB cache warm: reported", "title", rel.Title, "provider", providerHost, "available", available)
		}
		return
	}
	logger.Debug("AvailNZB cache warm: no new candidate to validate")
}

// validateCandidate validates a single candidate and returns a stream
func (s *Server) validateCandidate(ctx context.Context, cand triage.Candidate, device *auth.Device, contentIDs *session.AvailReportMeta) (Stream, error) {
	rel := cand.Release
	if rel == nil {
		return Stream{}, fmt.Errorf("candidate has no release")
	}
	logger.Trace("validateCandidate start", "title", rel.Title)

	// Get indexer name for logging / reporting
	indexerName := rel.Indexer
	if indexerName == "" && rel.SourceIndexer != nil {
		if idx, ok := rel.SourceIndexer.(indexer.Indexer); ok {
			indexerName = idx.Name()
			if strings.HasPrefix(indexerName, "Prowlarr:") {
				indexerName = strings.TrimSpace(strings.TrimPrefix(indexerName, "Prowlarr:"))
			}
		}
	}

	// Check AvailNZB for pre-download validation (GET /api/v1/status?url=...) using details URL
	skipValidation := false
	providerHosts := s.validator.GetProviderHosts()
	releaseDetailsURL := rel.DetailsURL
	if releaseDetailsURL != "" && len(providerHosts) > 0 && s.availClient != nil && s.availClient.BaseURL != "" {
		logger.Trace("validateCandidate: CheckPreDownload start", "title", rel.Title)
		isHealthy, lastUpdated, _, err := s.availClient.CheckPreDownload(releaseDetailsURL, providerHosts)
		logger.Trace("validateCandidate: CheckPreDownload done", "title", rel.Title, "skipValidation", err == nil && isHealthy, "err", err)
		if err == nil {
			if isHealthy {
				skipValidation = true
			} else {
				if time.Since(lastUpdated) <= 24*time.Hour {
					return Stream{}, fmt.Errorf("recently reported unhealthy")
				}
			}
		}
	}

	var sessionID string
	var streamSize int64

	if skipValidation {
		sessionID = fmt.Sprintf("%x", md5.Sum([]byte(rel.GUID)))
		streamSize = rel.Size

		if streamSize == 0 {
			logger.Warn("Indexer did not provide file size", "title", rel.Title, "indexer", indexerName)
		}

		logger.Info("Deferring NZB download (Lazy)", "title", rel.Title, "session_id", sessionID)
		logger.Trace("validateCandidate: CreateDeferredSession start", "title", rel.Title)

		idx := s.indexer
		if rel.SourceIndexer != nil {
			if i, ok := rel.SourceIndexer.(indexer.Indexer); ok {
				idx = i
			}
		}
		_, err := s.sessionManager.CreateDeferredSession(
			sessionID,
			rel.Link, // NZB download URL (indexer results typically have apikey)
			rel,
			idx,
			contentIDs,
		)
		logger.Trace("validateCandidate: CreateDeferredSession done", "title", rel.Title, "err", err)
		if err != nil {
			return Stream{}, fmt.Errorf("failed to create deferred session: %w", err)
		}
	} else {
		// IMMEDIATE - Download and validate (30s; NZBHydra often proxies to indexer)
		logger.Debug("Downloading NZB for validation", "title", rel.Title)

		dlCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		var nzbData []byte
		var err error
		if rel.SourceIndexer != nil {
			if idx, ok := rel.SourceIndexer.(indexer.Indexer); ok {
				nzbData, err = idx.DownloadNZB(dlCtx, rel.Link)
			} else {
				nzbData, err = s.indexer.DownloadNZB(dlCtx, rel.Link)
			}
		} else {
			nzbData, err = s.indexer.DownloadNZB(dlCtx, rel.Link)
		}
		cancel()

		if err != nil {
			return Stream{}, fmt.Errorf("failed to download NZB: %w", err)
		}

		// Parse NZB
		nzbParsed, err := nzb.Parse(bytes.NewReader(nzbData))
		if err != nil {
			return Stream{}, fmt.Errorf("failed to parse NZB: %w", err)
		}

		if len(nzbParsed.GetContentFiles()) == 0 {
			compressionType := nzbParsed.CompressionType()
			reportMeta := availnzb.ReportMeta{ReleaseName: rel.Title, Size: nzbParsed.TotalSize(), CompressionType: compressionType}
			if contentIDs != nil {
				reportMeta.ImdbID = contentIDs.ImdbID
				reportMeta.TvdbID = contentIDs.TvdbID
				reportMeta.Season = contentIDs.Season
				reportMeta.Episode = contentIDs.Episode
			}
			if (reportMeta.ImdbID != "" || reportMeta.TvdbID != "") && !release.IsPrivateReleaseURL(rel.DetailsURL) && s.availClient != nil {
				for _, providerHost := range s.validator.GetProviderHosts() {
					host := providerHost
					go func() {
						_ = s.availClient.ReportAvailability(rel.DetailsURL, host, false, reportMeta)
					}()
				}
			}
			return Stream{}, fmt.Errorf("no content files found in NZB")
		}

		streamSize = nzbParsed.TotalSize()
		sessionID = nzbParsed.Hash()

		// Validate availability
		logger.Trace("validateCandidate: ValidateNZB start", "title", rel.Title)
		validationResults := s.validator.ValidateNZB(ctx, nzbParsed)
		logger.Trace("validateCandidate: ValidateNZB done", "title", rel.Title, "results", len(validationResults))

		compressionType := nzbParsed.CompressionType()
		reportMeta := availnzb.ReportMeta{ReleaseName: rel.Title, Size: streamSize, CompressionType: compressionType}
		if contentIDs != nil {
			reportMeta.ImdbID = contentIDs.ImdbID
			reportMeta.TvdbID = contentIDs.TvdbID
			reportMeta.Season = contentIDs.Season
			reportMeta.Episode = contentIDs.Episode
		}
		shouldReport := (reportMeta.ImdbID != "" || reportMeta.TvdbID != "") && !release.IsPrivateReleaseURL(rel.DetailsURL)

		if len(validationResults) == 0 {
			if shouldReport && s.availClient != nil {
				for _, providerHost := range s.validator.GetProviderHosts() {
					host := providerHost
					go func() {
						_ = s.availClient.ReportAvailability(rel.DetailsURL, host, false, reportMeta)
					}()
				}
			}
			return Stream{}, fmt.Errorf("no valid providers")
		}

		// Report each provider's result to AvailNZB (available=true when that provider has content, false otherwise)
		if shouldReport && s.availClient != nil {
			go func() {
				for _, result := range validationResults {
					available := result.Error == nil && result.Available
					_ = s.availClient.ReportAvailability(rel.DetailsURL, result.Host, available, reportMeta)
				}
			}()
		}

		bestResult := validation.GetBestProvider(validationResults)
		if bestResult == nil {
			return Stream{}, fmt.Errorf("no best provider")
		}

		if nzbParsed.IsRARRelease() {
			return Stream{}, fmt.Errorf("RAR playback not supported (seeking issues)")
		}

		// Store NZB in session manager
		logger.Trace("validateCandidate: CreateSession start", "title", rel.Title)
		_, err = s.sessionManager.CreateSession(sessionID, nzbParsed, rel, contentIDs)
		logger.Trace("validateCandidate: CreateSession done", "title", rel.Title, "err", err)
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
	stream := buildStreamMetadata(streamURL, rel.Title, cand, sizeGB, streamSize, rel)

	logger.Debug("Created stream", "name", stream.Name, "url", stream.URL)
	return stream, nil
}

// handlePlay serves video content for a session.
// Each request creates its own stream from the cached blueprint.
// No stream sharing, no mutexes, no caching -- the shared segment
// cache in loader.File handles deduplication automatically.
func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request, device *auth.Device) {
	sessionID := strings.TrimPrefix(r.URL.Path, "/play/")
	logger.Info("Play request", "session", sessionID)

	sess, err := s.sessionManager.GetSession(sessionID)
	if err != nil {
		http.Error(w, "Session expired or not found", http.StatusNotFound)
		return
	}

	if _, err = sess.GetOrDownloadNZB(s.sessionManager); err != nil {
		logger.Error("Failed to lazy load NZB", "id", sessionID, "err", err)
		forceDisconnect(w, s.baseURL)
		return
	}

	if sess.NZB != nil && sess.NZB.IsRARRelease() {
		logger.Info("RAR playback not supported (seeking issues)", "id", sessionID)
		if s.availReporter != nil {
			s.availReporter.ReportRAR(sess)
		}
		forceDisconnect(w, s.baseURL)
		return
	}

	files := sess.Files
	if len(files) == 0 {
		if sess.File != nil {
			files = []*loader.File{sess.File}
		} else {
			logger.Error("No files in session", "id", sessionID)
			if sess.NZB != nil {
				s.validator.InvalidateCache(sess.NZB.Hash())
			}
			forceDisconnect(w, s.baseURL)
			return
		}
	}

	// Each request gets its own stream, scoped to the HTTP request context.
	// When the client disconnects, r.Context() is cancelled, which propagates
	// down through VirtualStream -> SegmentReader -> DownloadSegment.
	stream, name, size, bp, err := unpack.GetMediaStream(r.Context(), files, sess.Blueprint)
	if err != nil {
		logger.Error("Failed to open media stream", "id", sessionID, "err", err)
		s.reportBadRelease(sess, err)
		if sess.NZB != nil {
			s.validator.InvalidateCache(sess.NZB.Hash())
		}
		forceDisconnect(w, s.baseURL)
		return
	}
	defer stream.Close()

	if bp != nil && sess.Blueprint == nil {
		sess.SetBlueprint(bp)
	}

	// Report successful fetch/stream to AvailNZB (lazy sessions weren't reported at catalog time)
	if s.availReporter != nil {
		s.availReporter.ReportGood(sess)
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

	logger.Info("Serving media", "name", name, "size", size, "session", sessionID)

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w = newWriteTimeoutResponseWriter(w, 10*time.Minute)

	http.ServeContent(w, r, name, time.Time{}, monitoredStream)
	logger.Debug("Finished serving media", "session", sessionID)
}

// reportBadRelease reports unstreamable releases to AvailNZB in the background.
func (s *Server) reportBadRelease(sess *session.Session, streamErr error) {
	errMsg := streamErr.Error()
	if !strings.Contains(errMsg, "compressed") && !strings.Contains(errMsg, "encrypted") &&
		!strings.Contains(errMsg, "EOF") && !errors.Is(streamErr, loader.ErrTooManyZeroFills) {
		return
	}
	if s.availReporter != nil {
		s.availReporter.ReportBad(sess, errMsg)
	}
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
		// URL - try indexer download first (60s for debug play)
		dlCtx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		nzbData, err = s.indexer.DownloadNZB(dlCtx, nzbPath)
		cancel()
		if err != nil {
			// Fallback to HTTP GET with timeout to avoid hanging on slow/broken URLs
			httpClient := &http.Client{Timeout: 60 * time.Second}
			resp, httpErr := httpClient.Get(nzbPath)
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

	if nzbParsed.IsRARRelease() {
		logger.Info("RAR playback not supported (seeking issues)", "nzb", nzbPath)
		forceDisconnect(w, s.baseURL)
		return
	}

	// Create Session
	// Use hash of path as ID to allow repeating same path
	sessionID := fmt.Sprintf("debug-%x", nzbPath)
	// Or use NZB hash
	// sessionID := nzbParsed.Hash()

	// Create/Get Session (no release metadata for debug path - no AvailNZB reporting)
	sess, err := s.sessionManager.CreateSession(sessionID, nzbParsed, nil, nil)
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

	stream, name, size, bp, err := unpack.GetMediaStream(r.Context(), files, sess.Blueprint)
	if err != nil {
		logger.Error("Failed to open media stream", "err", err)
		http.Error(w, "Failed to open media stream: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer stream.Close()

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

	logger.Info("Serving debug media", "name", name, "size", size)
	logger.Debug("HTTP Request", "method", r.Method, "range", r.Header.Get("Range"), "user_agent", r.Header.Get("User-Agent"))

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Accept-Ranges", "bytes")
	w = newWriteTimeoutResponseWriter(w, 10*time.Minute)
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

// streamScore returns the triage score for sorting (higher = better). Uses the score from
// triage which respects the user's priority configuration (resolution, codec, etc.).
func streamScore(s Stream) int {
	return s.Score
}

// limitStreamsPerResolution limits streams per resolution group if MaxStreamsPerResolution is enabled
// Returns limited streams, still sorted by quality score, filling up to maxTotal if possible
func limitStreamsPerResolution(streams []Stream, maxPerResolution int, maxTotal int) []Stream {
	if maxPerResolution <= 0 {
		// Feature disabled - just cap at maxTotal
		if len(streams) > maxTotal {
			return streams[:maxTotal]
		}
		return streams
	}

	// Group streams by resolution (from triage-parsed metadata, already sorted by quality)
	resolutionGroups := make(map[string][]Stream)
	for _, stream := range streams {
		group := "sd"
		if stream.ParsedMetadata != nil {
			group = stream.ParsedMetadata.ResolutionGroup()
		}
		resolutionGroups[group] = append(resolutionGroups[group], stream)
		logger.Debug("Grouping stream by resolution", "name", stream.Name, "resolution", group)
	}
	logger.Debug("Resolution groups", "4k", len(resolutionGroups["4k"]), "1080p", len(resolutionGroups["1080p"]), "720p", len(resolutionGroups["720p"]), "sd", len(resolutionGroups["sd"]))

	// First pass: take up to maxPerResolution from each resolution (skip empty string = SD/unknown)
	result := make([]Stream, 0, maxTotal)
	takenPerResolution := make(map[string]int)

	// Process resolutions in a deterministic order to ensure consistent behavior
	resolutionOrder := []string{"4k", "1080p", "720p"}
	for _, resolution := range resolutionOrder {
		groupStreams := resolutionGroups[resolution]
		if len(groupStreams) == 0 {
			continue
		}
		limit := maxPerResolution
		if limit > len(groupStreams) {
			limit = len(groupStreams)
		}
		takenPerResolution[resolution] = limit
		result = append(result, groupStreams[:limit]...)
		logger.Debug("Limited streams per resolution (first pass)", "resolution", resolution, "total", len(groupStreams), "kept", limit, "result_count", len(result))
	}

	// Second pass: if we haven't reached maxTotal, fill remaining slots by rotating through resolutions
	// This ensures we get variety across resolutions while still respecting per-resolution limits
	if len(result) < maxTotal {
		remaining := maxTotal - len(result)
		resolutionOrder := []string{"4k", "1080p", "720p"}

		// Round-robin through resolutions to fill remaining slots
		for remaining > 0 {
			added := false
			for _, resolution := range resolutionOrder {
				if remaining <= 0 {
					break
				}
				groupStreams := resolutionGroups[resolution]
				taken := takenPerResolution[resolution]
				// Check if we can take more from this resolution (respecting maxPerResolution limit)
				// taken must be strictly less than maxPerResolution to take another one
				if taken < len(groupStreams) && taken < maxPerResolution {
					result = append(result, groupStreams[taken])
					takenPerResolution[resolution]++
					remaining--
					added = true
				}
			}
			if !added {
				// No more streams available from any resolution (all at their limits or exhausted)
				break
			}
		}

		// If still haven't reached maxTotal, add unlimited resolutions (SD/unknown) if available
		if len(result) < maxTotal && len(resolutionGroups["sd"]) > 0 {
			unlimitedStreams := resolutionGroups["sd"]
			remaining := maxTotal - len(result)
			if remaining > len(unlimitedStreams) {
				remaining = len(unlimitedStreams)
			}
			result = append(result, unlimitedStreams[:remaining]...)
		}
	}

	// Final sort by triage score to maintain user's priority order
	sort.Slice(result, func(i, j int) bool {
		return streamScore(result[i]) > streamScore(result[j])
	})

	logger.Debug("Limited streams per resolution (final)", "total", len(result), "maxPerResolution", maxPerResolution, "maxTotal", maxTotal)
	return result
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
func (s *Server) Reload(cfg *config.Config, baseURL string, indexer indexer.Indexer, validator *validation.Checker,
	triage *triage.Service, avail *availnzb.Client, availNZBIndexerHosts []string,
	tmdbClient *tmdb.Client, tvdbClient *tvdb.Client, deviceManager *auth.DeviceManager) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.config = cfg // Update config so MaxStreamsPerResolution and other settings are hot-reloaded
	s.baseURL = baseURL
	s.indexer = indexer
	s.validator = validator
	s.triageService = triage
	s.availClient = avail
	if avail != nil {
		s.availReporter = availnzb.NewReporter(avail, validator)
	} else {
		s.availReporter = nil
	}
	s.availNZBIndexerHosts = availNZBIndexerHosts
	s.tmdbClient = tmdbClient
	s.tvdbClient = tvdbClient
	s.deviceManager = deviceManager
}

type writeTimeoutResponseWriter struct {
	http.ResponseWriter
	timeout time.Duration
	rc      *http.ResponseController
}

func newWriteTimeoutResponseWriter(w http.ResponseWriter, timeout time.Duration) *writeTimeoutResponseWriter {
	return &writeTimeoutResponseWriter{
		ResponseWriter: w,
		timeout:        timeout,
		rc:             http.NewResponseController(w),
	}
}

func (w *writeTimeoutResponseWriter) Write(p []byte) (n int, err error) {
	if setErr := w.rc.SetWriteDeadline(time.Now().Add(w.timeout)); setErr != nil {
		return 0, setErr
	}
	return w.ResponseWriter.Write(p)
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
