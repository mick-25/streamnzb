package triage

import (
	"streamnzb/pkg/config"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/parser"
	"strings"
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
		return true // No resolution info, allow it
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
func checkCodec(cfg *config.FilterConfig, p *parser.ParsedRelease) bool {
	if p.Codec == "" {
		return true
	}
	
	codec := strings.ToLower(p.Codec)
	
	// Check blocked codecs
	for _, blocked := range cfg.BlockedCodecs {
		if strings.Contains(codec, strings.ToLower(blocked)) {
			return false
		}
	}
	
	// Check allowed codecs (if specified)
	if len(cfg.AllowedCodecs) > 0 {
		allowed := false
		for _, allowedCodec := range cfg.AllowedCodecs {
			if strings.Contains(codec, strings.ToLower(allowedCodec)) {
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

// checkAudio validates audio filters
func checkAudio(cfg *config.FilterConfig, p *parser.ParsedRelease) bool {
	// Check required audio
	if len(cfg.RequiredAudio) > 0 {
		hasRequired := false
		for _, required := range cfg.RequiredAudio {
			for _, audio := range p.Audio {
				if strings.Contains(strings.ToLower(audio), strings.ToLower(required)) {
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
			for _, allowed := range cfg.AllowedAudio {
				if strings.Contains(strings.ToLower(audio), strings.ToLower(allowed)) {
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

// checkHDR validates HDR filters
func checkHDR(cfg *config.FilterConfig, p *parser.ParsedRelease) bool {
	hasHDR := len(p.HDR) > 0
	
	// Check if HDR is required
	if cfg.RequireHDR && !hasHDR {
		return false
	}
	
	// Check blocked HDR types (e.g., block DV)
	for _, hdr := range p.HDR {
		for _, blocked := range cfg.BlockedHDR {
			if strings.Contains(strings.ToLower(hdr), strings.ToLower(blocked)) {
				return false
			}
		}
	}
	
	// Check if SDR should be blocked
	if cfg.BlockSDR {
		for _, hdr := range p.HDR {
			if strings.ToLower(hdr) == "sdr" {
				return false
			}
		}
	}
	
	// Check allowed HDR types (if specified)
	if len(cfg.AllowedHDR) > 0 && hasHDR {
		hasAllowed := false
		for _, hdr := range p.HDR {
			for _, allowed := range cfg.AllowedHDR {
				if strings.Contains(strings.ToLower(hdr), strings.ToLower(allowed)) {
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
	// PTT doesn't have a direct "Dubbed" field, but we can infer from languages
	// This is a simplified check - you may need to adjust based on your needs
	
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
	
	// Min bit depth (if PTT provides this info)
	// Note: PTT doesn't have BitDepth in the current ParsedRelease struct
	// You may need to add this field to parser.ParsedRelease
	
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
func checkSize(cfg *config.FilterConfig, res indexer.Item) bool {
	if res.Size == 0 {
		return true // No size info, allow it
	}
	
	sizeGB := float64(res.Size) / (1024 * 1024 * 1024)
	
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
