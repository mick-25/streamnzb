package triage

import (
	"sort"
	"strings"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/parser"
)

// Candidate represents a filtered search result suitable for deep inspection
type Candidate struct {
	Release     *release.Release
	Metadata    *parser.ParsedRelease
	Group       string // 4K, 1080p, 720p, SD
	Score       int
	QuerySource string // "id" or "text" — ID-based results are prioritized
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
func (s *Service) Filter(releases []*release.Release) []Candidate {
	var candidates []Candidate

	for _, rel := range releases {
		if rel == nil {
			continue
		}
		// Parse title
		parsed := parser.ParseReleaseTitle(rel.Title)

		// Check if it passes filters
		if s.FilterConfig != nil {
			if !s.shouldInclude(rel, parsed) {
				continue // Skip this result
			}
		}

		// Determine group (preserved for metadata but no longer used for selection)
		group := parsed.ResolutionGroup()

		// Calculate score
		score := s.calculateScore(rel, parsed)

		// Apply score boost for preferred attributes
		score += scoreBoost(s.SortConfig, parsed)

		// Prioritize ID-based results over text-based (ForceQuery dual search)
		if rel.QuerySource == "id" {
			score += 50_000_000 // Large boost so ID results sort first
		}

		querySource := rel.QuerySource
		if querySource == "" {
			querySource = "id"
		}
		candidates = append(candidates, Candidate{
			Release:     rel,
			Metadata:    parsed,
			Group:       group,
			Score:       score,
			QuerySource: querySource,
		})
	}

	candidates = s.deduplicateReleases(candidates)

	// Sort candidates by Score (descending)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	return candidates
}

// shouldInclude checks if a release passes all filter criteria
func (s *Service) shouldInclude(rel *release.Release, parsed *parser.ParsedRelease) bool {
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
	if !checkSize(cfg, rel) {
		return false
	}

	return true
}

func (s *Service) calculateScore(rel *release.Release, p *parser.ParsedRelease) int {
	// 1. Resolution Priority (Primary Sort)
	resolutionScore := 0
	group := p.ResolutionGroup()
	if weight, ok := s.SortConfig.ResolutionWeights[group]; ok {
		resolutionScore = weight
	} else if weight, ok := s.SortConfig.ResolutionWeights["sd"]; ok && (resolutionScore == 0) {
		resolutionScore = weight
	}

	// 2. Attribute Boosts (Secondary Sort)
	attributeBoost := 0

	// Codec boost: use max matching weight (order from PriorityList matters via weight values)
	if p.Codec != "" {
		if w := maxMatchingWeight(s.SortConfig.CodecWeights, strings.ToLower(p.Codec), true); w > 0 {
			attributeBoost += w
		}
	}

	// Audio boost: sum all matching (e.g. "Atmos" + "5.1")
	for _, audio := range p.Audio {
		for name, weight := range s.SortConfig.AudioWeights {
			if strings.Contains(strings.ToLower(audio), strings.ToLower(name)) {
				attributeBoost += weight
			}
		}
	}

	// Quality boost: use max matching weight
	if p.Quality != "" {
		if w := maxMatchingWeight(s.SortConfig.QualityWeights, strings.ToLower(p.Quality), true); w > 0 {
			attributeBoost += w
		}
	}

	// Visual tag boost (HDR and 3D)
	// Combine HDR and 3D into visual tags
	// PTT ThreeD formats: "3D", "3D HSBS", "3D SBS", "3D HOU", "3D OU"
	if s.SortConfig.VisualTagWeights != nil && len(s.SortConfig.VisualTagWeights) > 0 {
		visualTags := make([]string, 0)
		visualTags = append(visualTags, p.HDR...)
		if p.ThreeD != "" {
			visualTags = append(visualTags, p.ThreeD)
		}
		for _, tag := range visualTags {
			tagLower := strings.ToLower(tag)
			var maxWeight int
			for name, weight := range s.SortConfig.VisualTagWeights {
				nameLower := strings.ToLower(name)
				if strings.Contains(tagLower, nameLower) || (nameLower == "3d" && strings.HasPrefix(tagLower, "3d")) {
					if weight > maxWeight {
						maxWeight = weight
					}
				}
			}
			attributeBoost += maxWeight
		}
	}

	// 3. Age Score
	ageScore := 0.0
	if rel.PubDate != "" {
		pubTime, err := time.Parse(time.RFC1123Z, rel.PubDate)
		if err != nil {
			pubTime, err = time.Parse(time.RFC1123, rel.PubDate)
		}

		if err == nil {
			ageHours := time.Since(pubTime).Hours()
			// Invert so newer = higher score
			ageScore = (100000.0 - ageHours) * s.SortConfig.AgeWeight
		}
	}

	score := resolutionScore + attributeBoost + int(ageScore)

	// 4. Popularity (Grabs)
	if rel.Grabs > 0 {
		score += int(float64(rel.Grabs) * s.SortConfig.GrabWeight)
	}

	return score
}

// deduplicateReleases removes duplicate releases based on normalized name
// Keeps the release with the highest score (best indexer, most grabs, etc.)
func (s *Service) deduplicateReleases(candidates []Candidate) []Candidate {
	seen := make(map[string]*Candidate)

	for i := range candidates {
		candidate := &candidates[i]

		// Use release.NormalizeTitle for consistent comparison across the app
		normalized := release.NormalizeTitle(candidate.Release.Title)
		if normalized == "" {
			continue
		}

		existing, exists := seen[normalized]
		if !exists {
			seen[normalized] = candidate
			continue
		}

		// Keep the better release (higher score; on tie, prefer ID-based)
		if candidate.Score > existing.Score {
			seen[normalized] = candidate
		} else if candidate.Score == existing.Score && candidate.QuerySource == "id" && existing.QuerySource != "id" {
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

// maxMatchingWeight returns the highest weight from the map where value contains the key (case-insensitive).
// Ensures user's priority order (via weight values) is respected when multiple keys could match.
func maxMatchingWeight(weights map[string]int, value string, valueContainsKey bool) int {
	var max int
	for name, weight := range weights {
		nameLower := strings.ToLower(name)
		valLower := strings.ToLower(value)
		if valueContainsKey && strings.Contains(valLower, nameLower) {
			if weight > max {
				max = weight
			}
		}
	}
	return max
}
