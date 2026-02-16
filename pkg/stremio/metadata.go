package stremio

import (
	"fmt"
	"strings"

	"streamnzb/pkg/parser"
	"streamnzb/pkg/triage"
)

// buildStreamMetadata creates a rich Stream object with PTT metadata
func buildStreamMetadata(url, filename string, cand triage.Candidate, sizeGB float64, totalBytes int64) Stream {
	meta := cand.Metadata

	// Build stream name (left side - provider/quality badge)
	name := buildStreamName(meta, cand.Group)

	// Build detailed description (right side - technical details)
	description := buildDetailedDescription(meta, sizeGB, filename)

	// Create behavior hints
	hints := &BehaviorHints{
		NotWebReady: false,
		BingeGroup:  fmt.Sprintf("streamnzb|%s", cand.Group),
		VideoSize:   totalBytes,
		Filename:    filename,
	}

	return Stream{
		URL:           url,
		Name:          name,
		Description:   description,
		BehaviorHints: hints,
		StreamType:    "usenet",
	}
}

// buildStreamName creates the left-side name (provider badge + quality)
func buildStreamName(meta *parser.ParsedRelease, group string) string {
	parts := []string{}

	// Resolution
	parts = append(parts, strings.ToUpper(group))

	// Source type
	if meta.Quality != "" {
		// Simplify quality string
		quality := meta.Quality
		quality = strings.ReplaceAll(quality, "Blu-ray", "BluRay")
		quality = strings.ReplaceAll(quality, "WEB-DL", "WEB")
		parts = append(parts, quality)
	}

	return strings.Join(parts, " ")
}

// getQualityEmoji returns emoji based on source quality
func getQualityEmoji(meta *parser.ParsedRelease) string {
	quality := strings.ToLower(meta.Quality)

	if strings.Contains(quality, "remux") {
		return "⚡" // REMUX
	}
	if strings.Contains(quality, "bluray") || strings.Contains(quality, "blu-ray") {
		if len(meta.HDR) > 0 || meta.ThreeD != "" {
			return "🔥" // Visual tag BluRay (HDR/3D)
		}
		return "💿" // BluRay
	}
	if strings.Contains(quality, "web-dl") || strings.Contains(quality, "webdl") {
		return "📡" // WEB-DL
	}
	if strings.Contains(quality, "webrip") {
		return "🌐" // WEBRip
	}
	if strings.Contains(quality, "hdtv") {
		return "📺" // HDTV
	}

	return "🎬"
}

// buildDetailedDescription creates the right-side technical details
func buildDetailedDescription(meta *parser.ParsedRelease, sizeGB float64, filename string) string {
	lines := []string{}

	// Line 1: Source + Codec + Quality
	line1 := []string{}
	if meta.Quality != "" {
		line1 = append(line1, fmt.Sprintf("📡 %s", meta.Quality))
	}
	if meta.Codec != "" {
		codec := strings.ToUpper(meta.Codec)
		codec = strings.ReplaceAll(codec, "H.265", "HEVC")
		codec = strings.ReplaceAll(codec, "H.264", "AVC")
		codec = strings.ReplaceAll(codec, "X265", "HEVC")
		codec = strings.ReplaceAll(codec, "X264", "AVC")
		line1 = append(line1, fmt.Sprintf("🎞️ %s", codec))
	}
	if meta.Container != "" {
		line1 = append(line1, fmt.Sprintf("📦 %s", strings.ToUpper(meta.Container)))
	}
	if len(line1) > 0 {
		lines = append(lines, strings.Join(line1, " "))
	}

	// Line 2: Visual Tags (HDR/3D) + Audio
	// PTT ThreeD formats: "3D", "3D HSBS", "3D SBS", "3D HOU", "3D OU"
	line2 := []string{}
	visualTags := make([]string, 0)
	visualTags = append(visualTags, meta.HDR...)
	if meta.ThreeD != "" {
		// Preserve the actual 3D format from PTT
		visualTags = append(visualTags, meta.ThreeD)
	}
	if len(visualTags) > 0 {
		tags := strings.Join(visualTags, "|")
		line2 = append(line2, fmt.Sprintf("📺 %s", tags))
	}
	if len(meta.Audio) > 0 {
		audio := meta.Audio[0]
		if len(meta.Channels) > 0 {
			audio = fmt.Sprintf("%s %s", audio, meta.Channels[0])
		}
		line2 = append(line2, fmt.Sprintf("🎧 %s", audio))
	}
	if len(line2) > 0 {
		lines = append(lines, strings.Join(line2, " • "))
	}

	// Line 3: Special flags
	flags := []string{}
	if meta.Proper {
		flags = append(flags, "⚡ PROPER")
	}
	if meta.Repack {
		flags = append(flags, "🔄 REPACK")
	}
	if meta.Extended {
		flags = append(flags, "⏱️ EXTENDED")
	}
	if meta.Unrated {
		flags = append(flags, "🔞 UNRATED")
	}
	if meta.ThreeD != "" {
		flags = append(flags, "🕶️ 3D")
	}
	if len(flags) > 0 {
		lines = append(lines, strings.Join(flags, " "))
	}

	// Line 4: Size + Release Group
	line4 := []string{}
	if sizeGB > 0 {
		line4 = append(line4, fmt.Sprintf("💾 %.2f GB", sizeGB))
	} else {
		line4 = append(line4, "💾 Size Unknown")
	}
	if meta.Group != "" {
		line4 = append(line4, fmt.Sprintf("👥 %s", meta.Group))
	}
	lines = append(lines, strings.Join(line4, " • "))

	// Line 5: Languages
	if len(meta.Languages) > 0 {
		langs := strings.Join(meta.Languages, " | ")
		lines = append(lines, fmt.Sprintf("🌍 %s", langs))
	}

	// Line 6: Filename
	lines = append(lines, fmt.Sprintf("📄 %s", filename))

	return strings.Join(lines, "\n")
}
