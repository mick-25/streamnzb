package triage

import (
	"sort"
	"strings"

	"streamnzb/pkg/indexer"
	"streamnzb/pkg/parser"
)

// Candidate represents a filtered search result suitable for deep inspection
type Candidate struct {
	Result     indexer.Item
	Metadata   *parser.ParsedRelease
	Group      string // 4K, 1080p, 720p, SD
	Score      int
}

// Service implements smart triage logic
type Service struct {
	MaxPerGroup int
}

// NewService creates a new triage service
func NewService(maxPerGroup int) *Service {
	return &Service{
		MaxPerGroup: maxPerGroup,
	}
}

// Filter processes search results and returns the best candidates
func (s *Service) Filter(results []indexer.Item) []Candidate {
	// Group items
	groups := make(map[string][]Candidate)
	
	for _, res := range results {
		// Parse title
		parsed := parser.ParseReleaseTitle(res.Title)
		
		// Determine group
		group := determineGroup(parsed)
		
		// Calculate score
		score := calculateScore(res, parsed)
		
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
	priorities := []string{"4k", "1080p", "720p", "sd"}
	
	for _, groupName := range priorities {
		candidates, ok := groups[groupName]
		if !ok || len(candidates) == 0 {
			continue
		}
		
		// Sort by score descending
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].Score > candidates[j].Score
		})
		
		// Take top N
		count := s.MaxPerGroup
		if count > len(candidates) {
			count = len(candidates)
		}
		
		selected = append(selected, candidates[:count]...)
	}
	
	return selected
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
	score := 0
	
	// Prefer larger files (within reason) - weak signal but useful
	// We assume larger likely better quality for same resolution
	// Capped at 10 points
	sizeGB := float64(res.Size) / (1024 * 1024 * 1024)
	if sizeGB > 0 {
		score += int(sizeGB) 
	}
	
	// Prefer newer releases (less chance of takedown?) or older?
	// Actually older might have health issues, but 'repost' is good.
	// Let's rely on keywords.
	
	if p.Repack {
		score += 50
	}
	if p.Proper {
		score += 50
	}
	if p.Extended {
		score += 20
	}
	
	// Penalize CAM/TS
	if strings.Contains(strings.ToLower(p.Quality), "cam") || 
	   strings.Contains(strings.ToLower(p.Quality), "telesync") || 
	   strings.Contains(strings.ToLower(p.Quality), "ts") {
		score -= 1000
	}
	
	return score
}
