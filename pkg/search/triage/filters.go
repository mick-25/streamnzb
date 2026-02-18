package triage

import (
	"strings"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/search/parser"
	"streamnzb/pkg/release"
)

// checkQuality validates quality filters
func checkQuality(cfg *config.FilterConfig, p *parser.ParsedRelease) bool {
	quality := strings.ToLower(p.Quality)

	// Check blocked qualities
	for _, blocked := range cfg.BlockedQualities {
		if strings.Contains(quality, strings.ToLower(blocked)) {
			return false
		}
	}

	// Check allowed qualities (if specified)
	if len(cfg.AllowedQualities) > 0 {
		allowed := false
		for _, allowedQuality := range cfg.AllowedQualities {
			if strings.Contains(quality, strings.ToLower(allowedQuality)) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}

	return true
}

// checkResolution validates resolution filters
func checkResolution(cfg *config.FilterConfig, p *parser.ParsedRelease) bool {
	if p.Resolution == "" {
		// If resolution is unknown and we have min/max filters, reject it
		// This prevents SD/unknown content from bypassing resolution filters
		if cfg.MinResolution != "" || cfg.MaxResolution != "" {
			return false
		}
		return true // Only allow if no resolution filters configured
	}

	res := strings.ToLower(p.Resolution)

	// Define resolution hierarchy
	resolutions := map[string]int{
		"240p":  240,
		"360p":  360,
		"480p":  480,
		"576p":  576,
		"720p":  720,
		"1080p": 1080,
		"1440p": 1440,
		"2160p": 2160,
		"4k":    2160,
		"2k":    1440,
	}

	// Get current resolution value
	currentValue := 0
	for key, value := range resolutions {
		if strings.Contains(res, key) {
			currentValue = value
			break
		}
	}

	if currentValue == 0 {
		return true // Unknown resolution, allow it
	}

	// Check min resolution
	if cfg.MinResolution != "" {
		if minValue, ok := resolutions[strings.ToLower(cfg.MinResolution)]; ok {
			if currentValue < minValue {
				return false
			}
		}
	}

	// Check max resolution
	if cfg.MaxResolution != "" {
		if maxValue, ok := resolutions[strings.ToLower(cfg.MaxResolution)]; ok {
			if currentValue > maxValue {
				return false
			}
		}
	}

	return true
}

// checkCodec validates codec filters
// PTT returns normalized codec names: AVC (for avc/h264/x264), HEVC (for hevc/h265/x265), MPEG-2, DivX, Xvid
func checkCodec(cfg *config.FilterConfig, p *parser.ParsedRelease) bool {
	if p.Codec == "" {
		// If codec is unknown and we have codec filters, reject it
		// This prevents AV1/unknown codecs from bypassing allowed codec filters
		if len(cfg.AllowedCodecs) > 0 || len(cfg.BlockedCodecs) > 0 {
			return false
		}
		return true // Only allow if no codec filters configured
	}

	codec := strings.ToLower(p.Codec)

	// Normalize codec aliases to match PTT output
	// PTT normalizes: h264/x264 -> AVC, h265/x265 -> HEVC
	// But we check against user input which might use aliases
	codecAliases := map[string][]string{
		"avc":   {"avc", "h264", "x264", "h.264"},
		"hevc":  {"hevc", "h265", "x265", "h.265"},
		"mpeg-2": {"mpeg-2", "mpeg2", "mpeg"},
		"divx":  {"divx", "dvix"},
		"xvid":  {"xvid"},
	}

	// Check blocked codecs
	for _, blocked := range cfg.BlockedCodecs {
		blockedLower := strings.ToLower(blocked)
		// Check direct match or alias match
		if strings.Contains(codec, blockedLower) {
			return false
		}
		// Check if blocked codec is an alias of current codec
		for normalized, aliases := range codecAliases {
			if strings.Contains(codec, normalized) {
				for _, alias := range aliases {
					if strings.Contains(blockedLower, alias) {
						return false
					}
				}
			}
		}
	}

	// Check allowed codecs (if specified)
	if len(cfg.AllowedCodecs) > 0 {
		allowed := false
		for _, allowedCodec := range cfg.AllowedCodecs {
			allowedLower := strings.ToLower(allowedCodec)
			// Check direct match
			if strings.Contains(codec, allowedLower) {
				allowed = true
				break
			}
			// Check if allowed codec is an alias of current codec
			for normalized, aliases := range codecAliases {
				if strings.Contains(codec, normalized) {
					for _, alias := range aliases {
						if strings.Contains(allowedLower, alias) {
							allowed = true
							break
						}
					}
				}
				if allowed {
					break
				}
			}
			if allowed {
				break
			}
		}
		if !allowed {
			return false
		}
	}

	return true
}

// checkAudio validates audio filters
// PTT returns: DTS Lossless, DTS Lossy, Atmos, TrueHD, FLAC, DDP, EAC3, DD, AC3, AAC, PCM, OPUS, HQ, MP3
func checkAudio(cfg *config.FilterConfig, p *parser.ParsedRelease) bool {
	// Check required audio
	if len(cfg.RequiredAudio) > 0 {
		hasRequired := false
		for _, required := range cfg.RequiredAudio {
			requiredLower := strings.ToLower(required)
			for _, audio := range p.Audio {
				audioLower := strings.ToLower(audio)
				// Use Contains to match "DTS Lossless" when user specifies "DTS"
				if strings.Contains(audioLower, requiredLower) || strings.Contains(requiredLower, audioLower) {
					hasRequired = true
					break
				}
			}
			if hasRequired {
				break
			}
		}
		if !hasRequired {
			return false
		}
	}

	// Check allowed audio (if specified)
	if len(cfg.AllowedAudio) > 0 && len(p.Audio) > 0 {
		hasAllowed := false
		for _, audio := range p.Audio {
			audioLower := strings.ToLower(audio)
			for _, allowed := range cfg.AllowedAudio {
				allowedLower := strings.ToLower(allowed)
				// Use Contains to match "DTS Lossless" when user specifies "DTS"
				// Also handle "DDP" matching "DDP" or "DDP+" (EAC3)
				if strings.Contains(audioLower, allowedLower) || strings.Contains(allowedLower, audioLower) {
					hasAllowed = true
					break
				}
			}
			if hasAllowed {
				break
			}
		}
		if !hasAllowed {
			return false
		}
	}

	// Check min channels
	if cfg.MinChannels != "" && len(p.Channels) > 0 {
		channelHierarchy := map[string]float64{
			"mono":   1.0,
			"2.0":    2.0,
			"stereo": 2.0,
			"5.1":    5.1,
			"7.1":    7.1,
		}

		minValue, minOk := channelHierarchy[strings.ToLower(cfg.MinChannels)]
		if minOk {
			hasMinChannels := false
			for _, ch := range p.Channels {
				if chValue, ok := channelHierarchy[strings.ToLower(ch)]; ok {
					if chValue >= minValue {
						hasMinChannels = true
						break
					}
				}
			}
			if !hasMinChannels {
				return false
			}
		}
	}

	return true
}

// checkHDR validates visual tag filters (HDR and 3D)
// PTT ThreeD formats: "3D", "3D HSBS", "3D SBS", "3D HOU", "3D OU"
func checkHDR(cfg *config.FilterConfig, p *parser.ParsedRelease) bool {
	// Combine HDR and 3D into visual tags
	visualTags := make([]string, 0)
	visualTags = append(visualTags, p.HDR...)
	// Add 3D format if present (preserve full format like "3D HSBS" or normalize to "3D")
	if p.ThreeD != "" {
		// Normalize 3D formats: all 3D variants should match "3D" filter
		// But preserve the actual format for display
		visualTags = append(visualTags, p.ThreeD)
	}
	hasVisualTag := len(visualTags) > 0

	// Check if visual tag is required
	if cfg.RequireHDR && !hasVisualTag {
		return false
	}

	// Check blocked visual tag types (e.g., block DV or 3D)
	// For 3D, match any 3D format when user blocks "3D"
	for _, tag := range visualTags {
		tagLower := strings.ToLower(tag)
		for _, blocked := range cfg.BlockedHDR {
			blockedLower := strings.ToLower(blocked)
			// Direct match
			if strings.Contains(tagLower, blockedLower) {
				return false
			}
			// Special handling: "3D" filter blocks all 3D formats (3D, 3D HSBS, etc.)
			if blockedLower == "3d" && strings.HasPrefix(tagLower, "3d") {
				return false
			}
		}
	}

	// Check if SDR should be blocked
	if cfg.BlockSDR {
		for _, tag := range visualTags {
			if strings.ToLower(tag) == "sdr" {
				return false
			}
		}
	}

	// Check allowed visual tag types (if specified)
	// For 3D, match any 3D format when user allows "3D"
	if len(cfg.AllowedHDR) > 0 && hasVisualTag {
		hasAllowed := false
		for _, tag := range visualTags {
			tagLower := strings.ToLower(tag)
			for _, allowed := range cfg.AllowedHDR {
				allowedLower := strings.ToLower(allowed)
				// Direct match
				if strings.Contains(tagLower, allowedLower) {
					hasAllowed = true
					break
				}
				// Special handling: "3D" filter allows all 3D formats (3D, 3D HSBS, etc.)
				if allowedLower == "3d" && strings.HasPrefix(tagLower, "3d") {
					hasAllowed = true
					break
				}
			}
			if hasAllowed {
				break
			}
		}
		if !hasAllowed {
			return false
		}
	}

	return true
}

// checkLanguages validates language filters
func checkLanguages(cfg *config.FilterConfig, p *parser.ParsedRelease) bool {
	// Check required languages
	if len(cfg.RequiredLanguages) > 0 {
		hasRequired := false
		for _, required := range cfg.RequiredLanguages {
			for _, lang := range p.Languages {
				if strings.EqualFold(lang, required) {
					hasRequired = true
					break
				}
			}
			if hasRequired {
				break
			}
		}
		if !hasRequired {
			return false
		}
	}

	// Check allowed languages (if specified)
	if len(cfg.AllowedLanguages) > 0 && len(p.Languages) > 0 {
		hasAllowed := false
		for _, lang := range p.Languages {
			for _, allowed := range cfg.AllowedLanguages {
				if strings.EqualFold(lang, allowed) {
					hasAllowed = true
					break
				}
			}
			if hasAllowed {
				break
			}
		}
		if !hasAllowed {
			return false
		}
	}

	// Check if dubbed should be blocked
	if cfg.BlockDubbed && p.Dubbed {
		return false
	}

	return true
}

// checkOther validates other miscellaneous filters
func checkOther(cfg *config.FilterConfig, p *parser.ParsedRelease) bool {
	// Block CAM/TS/TC
	if cfg.BlockCam {
		quality := strings.ToLower(p.Quality)
		if strings.Contains(quality, "cam") ||
			strings.Contains(quality, "telesync") ||
			strings.Contains(quality, "ts") ||
			strings.Contains(quality, "tc") ||
			strings.Contains(quality, "telecine") {
			return false
		}
	}

	// Require proper
	if cfg.RequireProper && !p.Proper {
		return false
	}

	// Allow repack
	if !cfg.AllowRepack && p.Repack {
		return false
	}

	// Block hardcoded subtitles
	if cfg.BlockHardcoded && p.Hardcoded {
		return false
	}

	// Min bit depth filter
	if cfg.MinBitDepth != "" && p.BitDepth != "" {
		bitDepthHierarchy := map[string]int{
			"8bit":  8,
			"10bit": 10,
			"12bit": 12,
		}
		
		minValue, minOk := bitDepthHierarchy[strings.ToLower(cfg.MinBitDepth)]
		if minOk {
			currentValue, currentOk := bitDepthHierarchy[strings.ToLower(p.BitDepth)]
			if !currentOk || currentValue < minValue {
				return false
			}
		}
	}

	return true
}

// checkGroup validates group filters
func checkGroup(cfg *config.FilterConfig, p *parser.ParsedRelease) bool {
	if p.Group == "" {
		return true
	}

	group := strings.ToLower(p.Group)

	// Check blocked groups
	for _, blocked := range cfg.BlockedGroups {
		if strings.EqualFold(group, blocked) {
			return false
		}
	}

	// Check preferred groups (if specified)
	// Preferred groups don't block, they just boost score
	// So we allow all groups here

	return true
}

// checkSize validates size filters
func checkSize(cfg *config.FilterConfig, rel *release.Release) bool {
	if rel == nil {
		return false
	}
	// Always reject 0-byte or negative sizes (corrupt/invalid releases)
	if rel.Size <= 0 {
		return false
	}

	sizeGB := float64(rel.Size) / (1024 * 1024 * 1024)

	// Check min size
	if cfg.MinSizeGB > 0 && sizeGB < cfg.MinSizeGB {
		return false
	}

	// Check max size
	if cfg.MaxSizeGB > 0 && sizeGB > cfg.MaxSizeGB {
		return false
	}

	return true
}

// scoreBoost calculates score boost based on preferred attributes
func scoreBoost(sortCfg config.SortConfig, p *parser.ParsedRelease) int {
	boost := 0

	// Boost for preferred groups
	if p.Group != "" {
		for _, preferred := range sortCfg.PreferredGroups {
			if strings.EqualFold(p.Group, preferred) {
				boost += 1000 // Significant boost for preferred groups
				break
			}
		}
	}

	// Note: Language boost would require parser to extract language info
	// For now, preferred_languages is available in config for future use

	return boost
}
