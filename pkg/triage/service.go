package triage

import (
	"sort"
	"strconv"
	"strings"
	"time"

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
	FilterConfig *config.FilterConfig
	SortConfig   config.SortConfig
}

// NewService creates a new triage service
func NewService(filterConfig *config.FilterConfig, sortConfig config.SortConfig) *Service {
	return &Service{
		FilterConfig: filterConfig,
		SortConfig:   sortConfig,
	}
}

// Filter processes search results and returns candidates sorted by score
func (s *Service) Filter(results []indexer.Item) []Candidate {
	var candidates []Candidate
	
	for _, res := range results {
		// Parse title
		parsed := parser.ParseReleaseTitle(res.Title)
		
		// Check if it passes filters
		if s.FilterConfig != nil {
			if !s.shouldInclude(res, parsed) {
				continue // Skip this result
			}
		}
		
		// Determine group (preserved for metadata but no longer used for selection)
		group := determineGroup(parsed)

		// Calculate score
		score := s.calculateScore(res, parsed)
		
		// Apply score boost for preferred attributes
		score += scoreBoost(s.SortConfig, parsed)

		candidates = append(candidates, Candidate{
			Result:   res,
			Metadata: parsed,
			Group:    group,
			Score:    score,
		})
	}
	
	// Deduplicate releases (keep highest scored version of each unique release)
	candidates = s.deduplicateReleases(candidates)
	
	// Sort candidates by Score (descending)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	return candidates
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

func (s *Service) calculateScore(res indexer.Item, p *parser.ParsedRelease) int {
	// 1. Resolution Priority (Primary Sort)
	resolutionScore := 0
	group := determineGroup(p)
	if weight, ok := s.SortConfig.ResolutionWeights[group]; ok {
		resolutionScore = weight
	} else if weight, ok := s.SortConfig.ResolutionWeights["sd"]; ok && (resolutionScore == 0) {
		resolutionScore = weight
	}

	// 2. Attribute Boosts (Secondary Sort)
	attributeBoost := 0

	// Codec boost
	if p.Codec != "" {
		for name, weight := range s.SortConfig.CodecWeights {
			if strings.Contains(strings.ToLower(p.Codec), strings.ToLower(name)) {
				attributeBoost += weight
				break
			}
		}
	}

	// Audio boost
	for _, audio := range p.Audio {
		for name, weight := range s.SortConfig.AudioWeights {
			if strings.Contains(strings.ToLower(audio), strings.ToLower(name)) {
				attributeBoost += weight
				// Note: We don't break here to allow multiple audio boosts (e.g., "Atmos" + "5.1")
			}
		}
	}

	// Quality boost
	if p.Quality != "" {
		for name, weight := range s.SortConfig.QualityWeights {
			if strings.Contains(strings.ToLower(p.Quality), strings.ToLower(name)) {
				attributeBoost += weight
				break
			}
		}
	}

	// Visual tag boost (HDR and 3D)
	// Combine HDR and 3D into visual tags
	// PTT ThreeD formats: "3D", "3D HSBS", "3D SBS", "3D HOU", "3D OU"
	if s.SortConfig.VisualTagWeights != nil && len(s.SortConfig.VisualTagWeights) > 0 {
		visualTags := make([]string, 0)
		visualTags = append(visualTags, p.HDR...)
		if p.ThreeD != "" {
			// Use the actual 3D format, but also check for "3D" weight
			visualTags = append(visualTags, p.ThreeD)
		}
		for _, tag := range visualTags {
			tagLower := strings.ToLower(tag)
			for name, weight := range s.SortConfig.VisualTagWeights {
				nameLower := strings.ToLower(name)
				// Direct match
				if strings.Contains(tagLower, nameLower) {
					attributeBoost += weight
					break
				}
				// Special handling: "3D" weight applies to all 3D formats
				if nameLower == "3d" && strings.HasPrefix(tagLower, "3d") {
					attributeBoost += weight
					break
				}
			}
		}
	}

	// 3. Age Score
	ageScore := 0.0
	if res.PubDate != "" {
		pubTime, err := time.Parse(time.RFC1123Z, res.PubDate)
		if err != nil {
			pubTime, err = time.Parse(time.RFC1123, res.PubDate)
		}
		
		if err == nil {
			ageHours := time.Since(pubTime).Hours()
			// Invert so newer = higher score
			ageScore = (100000.0 - ageHours) * s.SortConfig.AgeWeight
		}
	}

	score := resolutionScore + attributeBoost + int(ageScore)

	// 4. Popularity (Grabs)
	if grabsStr := res.GetAttribute("grabs"); grabsStr != "" {
		if grabs, err := strconv.Atoi(grabsStr); err == nil {
			score += int(float64(grabs) * s.SortConfig.GrabWeight)
		}
	}

	return score
}

// deduplicateReleases removes duplicate releases based on normalized name
// Keeps the release with the highest score (best indexer, most grabs, etc.)
func (s *Service) deduplicateReleases(candidates []Candidate) []Candidate {
	seen := make(map[string]*Candidate)
	
	for i := range candidates {
		candidate := &candidates[i]
		
		// Normalize release name for comparison
		normalized := normalizeReleaseName(candidate.Metadata)
		
		existing, exists := seen[normalized]
		if !exists {
			seen[normalized] = candidate
			continue
		}
		
		// Keep the better release (higher score)
		if candidate.Score > existing.Score {
			seen[normalized] = candidate
		}
	}
	
	// Convert map back to slice
	result := make([]Candidate, 0, len(seen))
	for _, candidate := range seen {
		result = append(result, *candidate)
	}
	
	return result
}

// normalizeReleaseName creates a normalized key for deduplication
func normalizeReleaseName(p *parser.ParsedRelease) string {
	// Build normalized key from core attributes
	parts := []string{}
	
	if p.Title != "" {
		parts = append(parts, strings.ToLower(p.Title))
	}
	if p.Year != 0 {
		parts = append(parts, strconv.Itoa(p.Year))
	}
	if p.Season != 0 {
		parts = append(parts, "s"+strconv.Itoa(p.Season))
	}
	if p.Episode != 0 {
		parts = append(parts, "e"+strconv.Itoa(p.Episode))
	}
	if p.Resolution != "" {
		parts = append(parts, strings.ToLower(p.Resolution))
	}
	if p.Quality != "" {
		parts = append(parts, strings.ToLower(p.Quality))
	}
	if p.Codec != "" {
		parts = append(parts, strings.ToLower(p.Codec))
	}
	if p.Group != "" {
		parts = append(parts, strings.ToLower(p.Group))
	}
	
	return strings.Join(parts, "|")
}


