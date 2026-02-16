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
	"net/url"
	"os"
	"sort"
	"strconv"
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
	indexer             indexer.Indexer
	validator           *validation.Checker
	sessionManager      *session.Manager
	triageService       *triage.Service
	availClient         *availnzb.Client
	availNZBIndexerHosts []string // Underlying indexer hostnames for AvailNZB GetReleases (e.g. nzbgeek.info from NZBHydra)
	tmdbClient          *tmdb.Client
	tvdbClient          *tvdb.Client
	deviceManager       *auth.DeviceManager
	webHandler          http.Handler
	apiHandler          http.Handler
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
	s := &Server{
		manifest:            NewManifest(version),
		version:             version,
		baseURL:             baseURL,
		config:              cfg,
		indexer:             indexer,
		validator:           validator,
		sessionManager:      sessionMgr,
		triageService:       triageService,
		availClient:         availClient,
		availNZBIndexerHosts: availNZBIndexerHosts,
		tmdbClient:          tmdbClient,
		tvdbClient:          tvdbClient,
		deviceManager:       deviceManager,
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
	const streamRequestTimeout = 15 * time.Second
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

// triageCandidates returns filtered+sorted candidates (device or global triage).
func (s *Server) triageCandidates(device *auth.Device, items []indexer.Item) []triage.Candidate {
	if device != nil && device.Username != s.config.GetAdminUsername() &&
		(len(device.Filters.AllowedQualities) > 0 || device.Filters.MinResolution != "" || len(device.Filters.AllowedCodecs) > 0 ||
			len(device.Sorting.ResolutionWeights) > 0 || device.Sorting.GrabWeight != 0) {
		ts := triage.NewService(&device.Filters, device.Sorting)
		return ts.Filter(items)
	}
	return s.triageService.Filter(items)
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

	// 2. AvailNZB first: get cached releases, triage, validate up to max_streams
	if s.availClient != nil && s.availClient.BaseURL != "" && (contentIDs.ImdbID != "" || contentIDs.TvdbID != "") {
		releases, err := s.availClient.GetReleases(contentIDs.ImdbID, contentIDs.TvdbID, contentIDs.Season, contentIDs.Episode, availIndexers)
		if err == nil && releases != nil && len(releases.Releases) > 0 {
			var availItems []indexer.Item
			detailsURLToRelease := make(map[string]*availnzb.ReleaseItem)
			for i := range releases.Releases {
				r := &releases.Releases[i]
				if !r.Available || r.DownloadLink == "" {
					continue
				}
				item := indexer.Item{Title: r.ReleaseName, Link: r.DownloadLink, GUID: r.URL, Size: r.Size}
				if r.URL != "" && strings.Contains(r.URL, "://") {
					item.ActualGUID = r.URL
				}
				availItems = append(availItems, item)
				detailsURLToRelease[item.ReleaseDetailsURL()] = r
			}
			if len(availItems) > 0 {
				availCandidates := s.triageCandidates(device, availItems)
				logger.Debug("AvailNZB phase", "releases", len(availItems), "after_triage", len(availCandidates), "max_streams", maxStreams)
				for _, cand := range availCandidates {
					if len(streams) >= maxStreams {
						break
					}
					detailsURL := cand.Result.ReleaseDetailsURL()
					r := detailsURLToRelease[detailsURL]
					if r == nil {
						continue
					}
					// AvailNZB release: we have triage info and know it's reported good; defer NZB download until play.
					downloadURL := addAPIKeyToDownloadURL(r.DownloadLink, s.config.Indexers)
					sessionID := fmt.Sprintf("%x", md5.Sum([]byte(r.URL)))
					indexerName := r.Indexer
					if indexerName == "" {
						indexerName = "AvailNZB"
					}
					_, err := s.sessionManager.CreateDeferredSession(
						sessionID,
						downloadURL,
						r.URL,
						indexerName,
						r.ReleaseName,
						s.indexer, // aggregator does HTTP GET; URL already has apikey
						r.URL,
						contentIDs,
						r.Size,
					)
					if err != nil {
						logger.Debug("AvailNZB deferred session failed", "title", r.ReleaseName, "err", err)
						continue
					}
					var streamURL string
					if device != nil {
						streamURL = fmt.Sprintf("%s/%s/play/%s", s.baseURL, device.Token, sessionID)
					}
					sizeGB := float64(r.Size) / (1024 * 1024 * 1024)
					title := r.ReleaseName + "\n[AvailNZB]"
					stream := buildStreamMetadata(streamURL, title, cand, sizeGB, r.Size)
					streams = append(streams, stream)
					logger.Debug("Stream from AvailNZB (deferred)", "title", r.ReleaseName, "count", len(streams))
				}
				logger.Debug("AvailNZB phase done", "streams", len(streams), "need_more", maxStreams-len(streams))
				// Grow cache: validate one indexer candidate not already in AvailNZB and report it (background)
				if len(streams) >= maxStreams {
					knownURLs := make(map[string]bool)
					for u := range detailsURLToRelease {
						knownURLs[u] = true
					}
					go s.warmAvailNZBCache(context.Background(), req, contentIDs, knownURLs)
				}
			}
		}
	}

	// 3. Indexers: search, triage, validate until we have max_streams
	if len(streams) >= maxStreams {
		sort.Slice(streams, func(i, j int) bool { return getQualityScore(streams[i].Name) > getQualityScore(streams[j].Name) })
		logger.Info("Returning validated streams", "count", len(streams))
		return streams, nil
	}

	searchResp, err := s.indexer.Search(req)
	if err != nil {
		return nil, fmt.Errorf("indexer search failed: %w", err)
	}
	candidates := s.triageCandidates(device, searchResp.Channel.Items)
	logger.Debug("Indexer candidates after triage", "count", len(candidates))

	if s.availClient != nil && s.availClient.BaseURL != "" && (contentIDs.ImdbID != "" || contentIDs.TvdbID != "") {
		releases, _ := s.availClient.GetReleases(contentIDs.ImdbID, contentIDs.TvdbID, contentIDs.Season, contentIDs.Episode, availIndexers)
		if releases != nil && len(releases.Releases) > 0 {
			ourProviders := make(map[string]bool)
			for _, h := range s.validator.GetProviderHosts() {
				ourProviders[strings.ToLower(h)] = true
			}
			cachedAvailable := make(map[string]bool)
			cachedUnhealthyForUs := make(map[string]bool)
			for _, r := range releases.Releases {
				if r.Available {
					cachedAvailable[r.URL] = true
				} else if len(ourProviders) > 0 && len(r.Summary) > 0 {
					// Only treat as unhealthy if it's unhealthy for OUR providers (per Summary)
					ourReported := 0
					ourHealthy := 0
					for host, status := range r.Summary {
						if ourProviders[strings.ToLower(host)] {
							ourReported++
							if status.Healthy {
								ourHealthy++
							}
						}
					}
					if ourReported > 0 && ourHealthy == 0 {
						cachedUnhealthyForUs[r.URL] = true
					}
				}
			}
			// Filter out indexer candidates that AvailNZB marks as unhealthy for our providers
			if len(cachedUnhealthyForUs) > 0 {
				before := len(candidates)
				filtered := candidates[:0]
				for _, c := range candidates {
					if !cachedUnhealthyForUs[c.Result.ReleaseDetailsURL()] {
						filtered = append(filtered, c)
					}
				}
				candidates = filtered
				logger.Debug("Filtered candidates by AvailNZB unhealthy (our providers)", "removed", before-len(candidates), "remaining", len(candidates))
			}
			if len(cachedAvailable) > 0 {
				sort.SliceStable(candidates, func(i, j int) bool {
					ci := cachedAvailable[candidates[i].Result.ReleaseDetailsURL()]
					cj := cachedAvailable[candidates[j].Result.ReleaseDetailsURL()]
					return ci && !cj
				})
			}
		}
	}

	remaining := maxStreams - len(streams)
	maxAttempts := remaining * 2
	if maxAttempts < 6 {
		maxAttempts = 6
	}
	sem := make(chan struct{}, 6)
	resultChan := make(chan Stream, len(candidates))
	var mu sync.Mutex
	validated, attempted := 0, 0
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup

	for _, candidate := range candidates {
		mu.Lock()
		if attempted >= maxAttempts || validated >= remaining {
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
			case <-ctx.Done():
				return
			}
			mu.Lock()
			if validated >= remaining {
				mu.Unlock()
				return
			}
			mu.Unlock()

			stream, err := s.validateCandidate(ctx, cand, device, contentIDs)
			if err != nil {
				logger.Trace("validateCandidate failed", "title", cand.Result.Title, "err", err)
				return
			}
			mu.Lock()
			if validated < remaining {
				resultChan <- stream
				validated++
				if validated >= remaining {
					cancel()
				}
			}
			mu.Unlock()
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
			streams = append(streams, stream)
			if len(streams) >= maxStreams {
				goto doneCollect
			}
		case <-timeout:
			cancel()
			goto doneCollect
		case <-ctx.Done():
			goto doneCollect
		}
	}
doneCollect:

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		logger.Warn("Some validation goroutines may still be running")
	}

	sort.Slice(streams, func(i, j int) bool {
		return getQualityScore(streams[i].Name) > getQualityScore(streams[j].Name)
	})
	logger.Info("Returning validated streams", "count", len(streams))
	return streams, nil
}

// warmAvailNZBCache validates one indexer candidate that is not already in knownURLs and reports it to AvailNZB.
// Called in a goroutine when we already have enough streams from AvailNZB, to grow the cache for future requests.
func (s *Server) warmAvailNZBCache(ctx context.Context, req indexer.SearchRequest, contentIDs *session.AvailReportMeta, knownURLs map[string]bool) {
	if s.availClient == nil || s.availClient.BaseURL == "" || (contentIDs.ImdbID == "" && contentIDs.TvdbID == "") {
		return
	}
	searchResp, err := s.indexer.Search(req)
	if err != nil {
		logger.Debug("AvailNZB cache warm: search failed", "err", err)
		return
	}
	candidates := s.triageCandidates(nil, searchResp.Channel.Items)
	for _, cand := range candidates {
		detailsURL := cand.Result.ReleaseDetailsURL()
		if detailsURL == "" || knownURLs[detailsURL] {
			continue
		}
		item := cand.Result
		var nzbData []byte
		if item.SourceIndexer != nil {
			nzbData, err = item.SourceIndexer.DownloadNZB(item.Link)
		} else {
			nzbData, err = s.indexer.DownloadNZB(item.Link)
		}
		if err != nil {
			continue
		}
		nzbParsed, err := nzb.Parse(bytes.NewReader(nzbData))
		if err != nil {
			continue
		}
		validationResults := s.validator.ValidateNZB(ctx, nzbParsed)
		if len(validationResults) == 0 {
			continue
		}
		bestResult := validation.GetBestProvider(validationResults)
		if bestResult == nil {
			continue
		}
		streamSize := nzbParsed.TotalSize()
		meta := availnzb.ReportMeta{ReleaseName: item.Title, Size: streamSize}
		meta.ImdbID = contentIDs.ImdbID
		meta.TvdbID = contentIDs.TvdbID
		meta.Season = contentIDs.Season
		meta.Episode = contentIDs.Episode
		if err := s.availClient.ReportAvailability(detailsURL, bestResult.Host, true, meta); err != nil {
			logger.Debug("AvailNZB cache warm: report failed", "title", item.Title, "err", err)
			return
		}
		logger.Debug("AvailNZB cache warm: reported", "title", item.Title)
		return
	}
	logger.Debug("AvailNZB cache warm: no new candidate validated")
}

// validateCandidate validates a single candidate and returns a stream
func (s *Server) validateCandidate(ctx context.Context, cand triage.Candidate, device *auth.Device, contentIDs *session.AvailReportMeta) (Stream, error) {
	item := cand.Result
	logger.Trace("validateCandidate start", "title", item.Title)

	// Get indexer name for logging / reporting
	var indexerName string
	if item.SourceIndexer != nil {
		if item.ActualIndexer != "" {
			indexerName = item.ActualIndexer
		} else {
			indexerName = item.SourceIndexer.Name()
			if strings.HasPrefix(indexerName, "Prowlarr:") {
				indexerName = strings.TrimSpace(strings.TrimPrefix(indexerName, "Prowlarr:"))
			}
		}
	}

	// Check AvailNZB for pre-download validation (GET /api/v1/status?url=...) using details URL
	skipValidation := false
	providerHosts := s.validator.GetProviderHosts()
	releaseDetailsURL := item.ReleaseDetailsURL()
	if releaseDetailsURL != "" && len(providerHosts) > 0 && s.availClient != nil && s.availClient.BaseURL != "" {
		logger.Trace("validateCandidate: CheckPreDownload start", "title", item.Title)
		isHealthy, lastUpdated, _, err := s.availClient.CheckPreDownload(releaseDetailsURL, providerHosts)
		logger.Trace("validateCandidate: CheckPreDownload done", "title", item.Title, "skipValidation", err == nil && isHealthy, "err", err)
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
		sessionID = fmt.Sprintf("%x", md5.Sum([]byte(item.GUID)))
		streamSize = item.Size

		if streamSize == 0 {
			logger.Warn("Indexer did not provide file size", "title", item.Title, "indexer", indexerName)
		}

		logger.Info("Deferring NZB download (Lazy)", "title", item.Title, "session_id", sessionID)
		logger.Trace("validateCandidate: CreateDeferredSession start", "title", item.Title)

		_, err := s.sessionManager.CreateDeferredSession(
			sessionID,
			item.Link,                // NZB download URL for lazy fetch
			item.ReleaseDetailsURL(), // details URL for AvailNZB reporting
			indexerName,
			item.Title,
			item.SourceIndexer,
			item.GUID,
			contentIDs,
			item.Size, // 0 if unknown
		)
		logger.Trace("validateCandidate: CreateDeferredSession done", "title", item.Title, "err", err)
		if err != nil {
			return Stream{}, fmt.Errorf("failed to create deferred session: %w", err)
		}
	} else {
		// IMMEDIATE - Download and validate
		logger.Debug("Downloading NZB for validation", "title", item.Title)

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
		logger.Trace("validateCandidate: ValidateNZB start", "title", item.Title)
		validationResults := s.validator.ValidateNZB(ctx, nzbParsed)
		logger.Trace("validateCandidate: ValidateNZB done", "title", item.Title, "results", len(validationResults))
		if len(validationResults) == 0 {
			return Stream{}, fmt.Errorf("no valid providers")
		}

		bestResult := validation.GetBestProvider(validationResults)
		if bestResult == nil {
			return Stream{}, fmt.Errorf("no best provider")
		}

		// Async report to AvailNZB (POST /api/v1/report); report no longer includes download_link
		go func() {
			meta := availnzb.ReportMeta{ReleaseName: item.Title, Size: item.Size}
			if contentIDs != nil {
				meta.ImdbID = contentIDs.ImdbID
				meta.TvdbID = contentIDs.TvdbID
				meta.Season = contentIDs.Season
				meta.Episode = contentIDs.Episode
			}
			if meta.ImdbID == "" && meta.TvdbID == "" {
				logger.Debug("AvailNZB report skipped (good): no imdb_id or tvdb_id for this content", "url", item.ReleaseDetailsURL())
				return
			}
			if err := s.availClient.ReportAvailability(item.ReleaseDetailsURL(), bestResult.Host, true, meta); err != nil {
				logger.Debug("AvailNZB report failed", "err", err, "url", item.ReleaseDetailsURL())
			}
		}()

		// Store NZB in session manager
		logger.Trace("validateCandidate: CreateSession start", "title", item.Title)
		_, err = s.sessionManager.CreateSession(sessionID, nzbParsed, item.GUID, item.ReleaseDetailsURL(), contentIDs, item.Title, item.Link, item.Size)
		logger.Trace("validateCandidate: CreateSession done", "title", item.Title, "err", err)
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
				releaseURL := sess.ReleaseURL
				if releaseURL == "" {
					releaseURL = sess.NZBURL
				}
				if releaseURL == "" {
					return
				}
				meta := availnzb.ReportMeta{ReleaseName: sess.ReportReleaseName, Size: sess.ReportSize}
				if sess.ReportImdbID != "" {
					meta.ImdbID = sess.ReportImdbID
				} else if sess.ReportTvdbID != "" {
					meta.TvdbID = sess.ReportTvdbID
					meta.Season = sess.ReportSeason
					meta.Episode = sess.ReportEpisode
				}
				if meta.ImdbID == "" && meta.TvdbID == "" {
					return // API requires movie or TV IDs
				}
				if meta.ReleaseName == "" {
					return // API requires release_name
				}
				providerURL := "ALL"
				if hosts := s.validator.GetProviderHosts(); len(hosts) > 0 {
					providerURL = hosts[0]
				}
				_ = s.availClient.ReportAvailability(releaseURL, providerURL, false, meta)
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

	// Cancel session context when HTTP request is cancelled (client disconnects)
	// This ensures stream operations stop when client disconnects
	go func() {
		<-r.Context().Done()
		logger.Debug("HTTP request cancelled, cancelling session context", "session", sessionID)
		sess.Close()
	}()

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

	// Wrap ResponseWriter with rolling write timeout to detect abandoned clients.
	// When client stops reading, writes block; after 10 min the connection closes
	// so EndPlayback runs and NNTP connections are released.
	w = newWriteTimeoutResponseWriter(w, 10*time.Minute)

	// Handle streaming using standard library ServeContent
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

	// Create Session
	// Use hash of path as ID to allow repeating same path
	sessionID := fmt.Sprintf("debug-%x", nzbPath)
	// Or use NZB hash
	// sessionID := nzbParsed.Hash()

	// Create/Get Session (no release URL or report meta for debug upload path)
	sess, err := s.sessionManager.CreateSession(sessionID, nzbParsed, "", "", nil, "", "", 0)
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
	triage *triage.Service, avail *availnzb.Client, availNZBIndexerHosts []string,
	tmdbClient *tmdb.Client, tvdbClient *tvdb.Client, deviceManager *auth.DeviceManager) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.baseURL = baseURL
	s.indexer = indexer
	s.validator = validator
	s.triageService = triage
	s.availClient = avail
	s.availNZBIndexerHosts = availNZBIndexerHosts
	s.tmdbClient = tmdbClient
	s.tvdbClient = tvdbClient
	s.deviceManager = deviceManager
	// Note: sessionManager pools are updated separately via sessionManager.UpdatePools
}

// writeTimeoutResponseWriter wraps http.ResponseWriter and sets a rolling write deadline
// before each Write. This detects abandoned clients (e.g. crashed app, network drop)
// when the TCP connection stays half-open: writes block until the deadline, then the
// connection closes so EndPlayback runs and resources are released.
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
