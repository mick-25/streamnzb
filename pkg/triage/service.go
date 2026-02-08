package triage

import (
	"sort"
	"strconv"
	"strings"

	"streamnzb/pkg/config"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/parser"
)

// Candidate represents a filtered search result suitable for deep inspection
type Candidate struct {
	Result   indexer.Item
	Metadata *parser.ParsedRelease
	Group    string // 4K, 1080p, 720p, SD
	Score    int
}

// Service implements smart triage logic
type Service struct {
	MaxPerGroup  int
	FilterConfig *config.FilterConfig
}

// NewService creates a new triage service
func NewService(maxPerGroup int, filterConfig *config.FilterConfig) *Service {
	return &Service{
		MaxPerGroup:  maxPerGroup,
		FilterConfig: filterConfig,
	}
}

// Filter processes search results and returns the best candidates
func (s *Service) Filter(results []indexer.Item) []Candidate {
	// Apply PTT-based filtering FIRST
	var filtered []indexer.Item
	
	for _, res := range results {
		// Parse title
		parsed := parser.ParseReleaseTitle(res.Title)
		
		// Check if it passes filters
		if s.FilterConfig != nil {
			if !s.shouldInclude(res, parsed) {
				continue // Skip this result
			}
		}
		
		filtered = append(filtered, res)
	}
	
	// Group items
	groups := make(map[string][]Candidate)

	for _, res := range filtered {
		// Parse title again (we could optimize by caching parsed results)
		parsed := parser.ParseReleaseTitle(res.Title)

		// Determine group
		group := determineGroup(parsed)

		// Calculate score
		score := calculateScore(res, parsed)
		
		// Apply score boost for preferred attributes
		if s.FilterConfig != nil {
			score += scoreBoost(s.FilterConfig, parsed)
		}

		candidate := Candidate{
			Result:   res,
			Metadata: parsed,
			Group:    group,
			Score:    score,
		}

		groups[group] = append(groups[group], candidate)
	}

	// Select best candidates
	var selected []Candidate

	// Processing order (priority)
	priorities := []string{"4k", "1080p", "720p"}

	for _, groupName := range priorities {
		candidates, ok := groups[groupName]
		if !ok || len(candidates) == 0 {
			continue
		}

		// Use Round-Robin selection to ensure indexer diversity
		// This prevents one indexer (with high grab counts) from dominating the quota
		count := s.MaxPerGroup
		balanced := roundRobinSelect(candidates, count)

		selected = append(selected, balanced...)
	}

	return selected
}

// shouldInclude checks if a release passes all filter criteria
func (s *Service) shouldInclude(res indexer.Item, parsed *parser.ParsedRelease) bool {
	cfg := s.FilterConfig
	
	// Quality filters
	if !checkQuality(cfg, parsed) {
		return false
	}
	
	// Resolution filters
	if !checkResolution(cfg, parsed) {
		return false
	}
	
	// Codec filters
	if !checkCodec(cfg, parsed) {
		return false
	}
	
	// Audio filters
	if !checkAudio(cfg, parsed) {
		return false
	}
	
	// HDR filters
	if !checkHDR(cfg, parsed) {
		return false
	}
	
	// Language filters
	if !checkLanguages(cfg, parsed) {
		return false
	}
	
	// Other filters
	if !checkOther(cfg, parsed) {
		return false
	}
	
	// Group filters
	if !checkGroup(cfg, parsed) {
		return false
	}
	
	// Size filters
	if !checkSize(cfg, res) {
		return false
	}
	
	return true
}

func determineGroup(p *parser.ParsedRelease) string {
	res := strings.ToLower(p.Resolution)

	if strings.Contains(res, "2160") || strings.Contains(res, "4k") {
		return "4k"
	}
	if strings.Contains(res, "1080") {
		return "1080p"
	}
	if strings.Contains(res, "720") {
		return "720p"
	}

	return "sd"
}

func calculateScore(res indexer.Item, p *parser.ParsedRelease) int {
	// Base score is download count (popularity)
	score := 0
	grabsStr := res.GetAttribute("grabs")
	if grabsStr != "" {
		if val, err := strconv.Atoi(grabsStr); err == nil {
			score = val
		}
	}

	// Penalize strictly bad quality (CAM/TS)
	// We want these at the absolute bottom, even if popular
	if strings.Contains(strings.ToLower(p.Quality), "cam") ||
		strings.Contains(strings.ToLower(p.Quality), "telesync") ||
		strings.Contains(strings.ToLower(p.Quality), "ts") {
		score -= 1000000
	}

	return score
}

// roundRobinSelect picks top candidates ensuring indexer diversity
func roundRobinSelect(candidates []Candidate, n int) []Candidate {
	if n <= 0 {
		return nil
	}

	// Group by Indexer
	byIndexer := make(map[string][]Candidate)
	var indexerNames []string

	for _, c := range candidates {
		name := "unknown"
		if c.Result.SourceIndexer != nil {
			name = c.Result.SourceIndexer.Name()
		}

		if _, exists := byIndexer[name]; !exists {
			indexerNames = append(indexerNames, name)
		}
		byIndexer[name] = append(byIndexer[name], c)
	}

	// Sort candidates within each indexer by score (desc)
	for name := range byIndexer {
		list := byIndexer[name]
		sort.Slice(list, func(i, j int) bool {
			return list[i].Score > list[j].Score
		})
		byIndexer[name] = list
	}

	// Sort indexer names for deterministic iteration
	sort.Strings(indexerNames)

	// Round Robin selection
	var selected []Candidate

	for len(selected) < n {
		addedAnything := false

		for _, name := range indexerNames {
			if len(selected) >= n {
				break
			}

			list := byIndexer[name]
			if len(list) > 0 {
				// Pop best from this indexer
				selected = append(selected, list[0])
				byIndexer[name] = list[1:]
				addedAnything = true
			}
		}

		if !addedAnything {
			break // Run out of candidates
		}
	}

	return selected
}
