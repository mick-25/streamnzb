package stremio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/availnzb"
	"streamnzb/pkg/loader"
	"streamnzb/pkg/logger"
	"streamnzb/pkg/nzb"
	"streamnzb/pkg/nzbhydra"
	"streamnzb/pkg/session"
	"streamnzb/pkg/triage"
	"streamnzb/pkg/unpack"
	"streamnzb/pkg/validation"
)

// Server represents the Stremio addon HTTP server
type Server struct {
	manifest       *Manifest
	baseURL        string
	hydraClient    *nzbhydra.Client
	validator      *validation.Checker
	sessionManager *session.Manager
	triageService  *triage.Service
	availClient    *availnzb.Client
	securityToken  string
}

// NewServer creates a new Stremio addon server
func NewServer(baseURL string, hydraClient *nzbhydra.Client, validator *validation.Checker, sessionMgr *session.Manager, triageService *triage.Service, availClient *availnzb.Client, securityToken string) *Server {
	actualBaseURL := baseURL
	if securityToken != "" {
		if !strings.HasSuffix(actualBaseURL, "/") {
			actualBaseURL += "/"
		}
		actualBaseURL += securityToken
	}

	return &Server{
		manifest:       NewManifest(),
		baseURL:        actualBaseURL,
		hydraClient:    hydraClient,
		validator:      validator,
		sessionManager: sessionMgr,
		triageService:  triageService,
		availClient:    availClient,
		securityToken:  securityToken,
	}
}

// SetupRoutes configures HTTP routes for the addon
func (s *Server) SetupRoutes(mux *http.ServeMux) {
	// Root handler for manifest and other routes
	// We use a custom handler to handle the optional token prefix
	finalHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		
		if s.securityToken != "" {
			// Path format: /{token}/manifest.json or /{token}/stream/...
			trimmedPath := strings.TrimPrefix(path, "/")
			parts := strings.SplitN(trimmedPath, "/", 2)
			
			if len(parts) < 1 || parts[0] != s.securityToken {
				logger.Error("Unauthorized request", "path", path, "remote", r.RemoteAddr)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
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
		} else {
			http.NotFound(w, r)
		}
	})

	mux.Handle("/", finalHandler)
}

// handleManifest serves the addon manifest
func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	logger.Debug("Manifest request", "remote", r.RemoteAddr)
	
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	
	data, err := s.manifest.ToJSON()
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

// searchAndValidate searches NZBHydra2 and validates article availability
func (s *Server) searchAndValidate(ctx context.Context, contentType, id string) ([]Stream, error) {
	// Determine search parameters based on ID type
	req := nzbhydra.SearchRequest{
		Limit: 1000,
	}
	
	// For series, extract IMDb ID and season/episode from "tt12345678:1:1" format
	searchID := id
	if contentType == "series" && strings.Contains(id, ":") {
		parts := strings.Split(id, ":")
		if len(parts) >= 3 {
			searchID = parts[0]           // Extract "tt12345678"
			req.Season = parts[1]         // Season number
			req.Episode = parts[2]        // Episode number
		} else if len(parts) > 0 {
			searchID = parts[0]
		}
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
	}
	
	// Debug: Log search parameters
	logger.Debug("NZBHydra2 search", "imdb", req.IMDbID, "cat", req.Cat, "season", req.Season, "ep", req.Episode)
	
	// Search NZBHydra2
	searchResp, err := s.hydraClient.Search(req)
	if err != nil {
		return nil, fmt.Errorf("NZBHydra2 search failed: %w", err)
	}
	
	logger.Info("Found NZB results", "count", len(searchResp.Channel.Items))
	
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

	// Limit concurrent validations to prevent connection pool exhaustion
	// With 5 candidates per group (up to 20 total), we can try to process them all 
	// in roughly one batch if we allow 20 concurrent validations.
	// Even if this exceeds pool size (queuing at pool level), it's faster than batching.
	sem := make(chan struct{}, 20)

	// Create cancellable context to stop pending downloads once we have enough results
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for _, candidate := range candidates {
		wg.Add(1)
		go func(cand triage.Candidate) {
			defer wg.Done()
			
			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()
			
			// Check if we should abort (early exit triggered)
			select {
			case <-ctx.Done():
				resultChan <- nzbResult{err: ctx.Err()}
				return
			default:
			}
			
			// Use candidate's result item
			item := cand.Result
			
			// Download NZB
			nzbData, err := s.hydraClient.DownloadNZB(item.Link)
			if err != nil {
				logger.Error("Failed to download NZB", "title", item.Title, "err", err)
				resultChan <- nzbResult{err: err}
				return
			}
			
			// Parse NZB
			nzbParsed, err := nzb.Parse(bytes.NewReader(nzbData))
			if err != nil {
				logger.Error("Failed to parse NZB", "title", item.Title, "err", err)
				resultChan <- nzbResult{err: err}
				return
			}
			
			// Validate availability via HEAD check
			// Pass cancelable context
			validationResults := s.validator.ValidateNZB(ctx, nzbParsed)
			if len(validationResults) == 0 {
				resultChan <- nzbResult{err: fmt.Errorf("validation failed")}
				return
			}
			
			// Find best provider
			bestResult := validation.GetBestProvider(validationResults)
			if bestResult == nil {
				resultChan <- nzbResult{err: fmt.Errorf("no provider available")}
				return
			}

			// Async report to AvailNZB
			go func() {
				nzbID := nzbParsed.CalculateID()
				if nzbID != "" {
					err := s.availClient.ReportAvailability(nzbID, bestResult.Host, true)
					if err != nil {
						logger.Error("Failed to report availability to AvailNZB", "nzb_id", nzbID, "err", err)
					} else {
						logger.Debug("Reported availability to AvailNZB", "nzb_id", nzbID, "provider", bestResult.Provider)
					}
				}
			}()

			// Create session ID from NZB hash
			sessionID := nzbParsed.Hash()
			
			// Store NZB in session manager
			sess, err := s.sessionManager.CreateSession(sessionID, nzbParsed)
			if err != nil {
				resultChan <- nzbResult{err: err}
				return
			}
			
			// Deep Inspection
			var displayTitle string = item.Title
			// Format size
			sizeGB := float64(nzbParsed.TotalSize()) / (1024 * 1024 * 1024)
			
			if len(sess.Files) > 0 {
				// Try to inspect RAR
				_, err := unpack.InspectRAR(sess.Files)
				logger.Debug("Inspected archive", "files", len(sess.Files), "err", err)
				if strings.Contains(err.Error(), "nested archive detected") {
					// True error (nested archive, no video in valid RAR, etc.)
					logger.Debug("RAR inspection failed", "title", item.Title, "err", err)
					s.sessionManager.DeleteSession(sessionID)
					resultChan <- nzbResult{err: fmt.Errorf("deep inspection failed: %w", err)}
					return
				}
			}

			// Create stream URL
			streamURL := fmt.Sprintf("%s/play/%s", s.baseURL, sessionID)

			// Build rich stream metadata from PTT
			stream := buildStreamMetadata(streamURL, displayTitle, cand, sizeGB, nzbParsed.TotalSize(), bestResult.Provider)

			logger.Info("Created stream", "name", stream.Name, "url", stream.URL)
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
	counts := make(map[string]int)
	
	for result := range resultChan {
		if result.err == nil {
			streams = append(streams, result.stream)
			
			// Track quotas
			// cand.Group is passed via nzbResult now
			// We need to add 'group' to nzbResult struct
			counts[result.group]++
			
			// Check if we have enough
			// Need 2 of 4k, 2 of 1080p, 2 of 720p
			has4k := counts["4k"] >= 2
			has1080p := counts["1080p"] >= 2
			has720p := counts["720p"] >= 2
			// SD is bonus
			
			if has4k && has1080p && has720p {
				logger.Info("Fast-Path: Quotas met. Cancelling checks.", "4k", counts["4k"], "1080p", counts["1080p"], "720p", counts["720p"])
				cancel() // Stop others!
				break // Return immediately to user
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

	// Select top 2 from each bucket
	var finalStreams []Stream
	priorities := []string{"4k", "1080p", "720p", "sd"}
	
	for _, bucketName := range priorities {
		bucketStreams := buckets[bucketName]
		
		// Sort by quality score within bucket to get best ones
		sort.Slice(bucketStreams, func(i, j int) bool {
			return getQualityScore(bucketStreams[i].Name) > getQualityScore(bucketStreams[j].Name)
		})
		
		// Take top 2
		count := 2
		if len(bucketStreams) < count {
			count = len(bucketStreams)
		}
		
		finalStreams = append(finalStreams, bucketStreams[:count]...)
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
		http.Error(w, "Session not found", http.StatusNotFound)
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
			http.Error(w, "No files available", http.StatusInternalServerError)
			return
		}
	}
	
	// Get media stream (handles RAR, 7z, and direct files)
	// Pass cached blueprint if available
	stream, name, size, bp, err := unpack.GetMediaStream(files, sess.Blueprint)
	if err != nil {
		logger.Error("Failed to open media stream", "id", sessionID, "err", err)
		http.Error(w, fmt.Sprintf("Error: %v", err), http.StatusInternalServerError)
		return
	}
	
	// Cache the blueprint if new one returned
	if bp != nil && sess.Blueprint == nil {
		sess.SetBlueprint(bp)
	}
	
	defer stream.Close()

	logger.Info("Serving media", "name", name, "size", size, "session", sessionID)
	
	// Set headers
	w.Header().Set("Content-Type", "video/mp4") // Stremio often prefers this or generic buffer
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	
	// Handle streaming using standard library ServeContent
	// This automatically handles Range requests, HEAD requests, and efficient copying.
	http.ServeContent(w, r, name, time.Time{}, stream)
	
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
