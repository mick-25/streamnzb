package parser

import (
	"strconv"
	"strings"

	"github.com/MunifTanjim/go-ptt"
)

// ParsedRelease contains parsed metadata from a release title
// Matches PTT parser output structure
type ParsedRelease struct {
	Title      string
	Year       int
	Resolution string
	Quality    string
	Codec      string
	Audio      []string
	Channels   []string
	HDR        []string
	Container  string
	Group      string
	Season     int
	Episode    int

	// Additional metadata
	Languages []string
	Network   string
	Repack    bool
	Proper    bool
	Extended  bool
	Unrated   bool
	ThreeD    string
	Size      string

	// PTT fields we were missing
	BitDepth  string // e.g., "8bit", "10bit", "12bit"
	Dubbed    bool   // Dubbed audio track
	Hardcoded bool   // Hardcoded subtitles
}

// ParseReleaseTitle parses a release title using go-ptt
func ParseReleaseTitle(title string) *ParsedRelease {
	info := ptt.Parse(title)

	parsed := &ParsedRelease{
		Title:      info.Title,
		Resolution: info.Resolution,
		Quality:    info.Quality,
		Codec:      info.Codec,
		Audio:      info.Audio,
		Channels:   info.Channels,
		HDR:        info.HDR,
		Container:  info.Container,
		Group:      info.Group,
		Languages:  info.Languages,
		Network:    info.Network,
		Repack:     info.Repack,
		Proper:     info.Proper,
		Extended:   info.Extended,
		Unrated:    info.Unrated,
		ThreeD:     info.ThreeD,
		Size:       info.Size,
		BitDepth:   info.BitDepth,
		Dubbed:     info.Dubbed,
		Hardcoded:  info.Hardcoded,
	}

	// Convert year from string to int
	if info.Year != "" {
		if year, err := strconv.Atoi(info.Year); err == nil {
			parsed.Year = year
		}
	}

	// Extract season/episode if available
	if len(info.Seasons) > 0 {
		parsed.Season = info.Seasons[0]
	}
	if len(info.Episodes) > 0 {
		parsed.Episode = info.Episodes[0]
	}

	return parsed
}

// ResolutionGroup returns the resolution group (4k, 1080p, 720p, sd) from parsed metadata.
func (p *ParsedRelease) ResolutionGroup() string {
	if p == nil {
		return "sd"
	}
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
