package triage

import (
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/search/parser"
	"testing"
)

// Test Quality Filtering
func TestCheckQuality(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.FilterConfig
		parsed     *parser.ParsedRelease
		shouldPass bool
	}{
		{
			name: "WEB-DL passes when allowed",
			cfg: &config.FilterConfig{
				AllowedQualities: []string{"WEB-DL", "BluRay"},
			},
			parsed: &parser.ParsedRelease{
				Quality: "WEB-DL",
			},
			shouldPass: true,
		},
		{
			name: "CAM rejected when not allowed",
			cfg: &config.FilterConfig{
				AllowedQualities: []string{"WEB-DL", "BluRay"},
			},
			parsed: &parser.ParsedRelease{
				Quality: "CAM",
			},
			shouldPass: false,
		},
		{
			name: "Blocked quality rejected",
			cfg: &config.FilterConfig{
				BlockedQualities: []string{"CAM", "TS"},
			},
			parsed: &parser.ParsedRelease{
				Quality: "CAM",
			},
			shouldPass: false,
		},
		{
			name: "Non-blocked quality passes",
			cfg: &config.FilterConfig{
				BlockedQualities: []string{"CAM"},
			},
			parsed: &parser.ParsedRelease{
				Quality: "WEB-DL",
			},
			shouldPass: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := checkQuality(tt.cfg, tt.parsed)
			if result != tt.shouldPass {
				t.Errorf("checkQuality() = %v, want %v", result, tt.shouldPass)
			}
		})
	}
}

// Test Audio Filtering
func TestCheckAudio(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.FilterConfig
		parsed     *parser.ParsedRelease
		shouldPass bool
	}{
		{
			name: "Required audio present passes",
			cfg: &config.FilterConfig{
				RequiredAudio: []string{"Atmos"},
			},
			parsed: &parser.ParsedRelease{
				Audio: []string{"DDP5.1", "Atmos"},
			},
			shouldPass: true,
		},
		{
			name: "Required audio missing rejected",
			cfg: &config.FilterConfig{
				RequiredAudio: []string{"Atmos"},
			},
			parsed: &parser.ParsedRelease{
				Audio: []string{"DDP5.1"},
			},
			shouldPass: false,
		},
		{
			name: "Allowed audio passes",
			cfg: &config.FilterConfig{
				AllowedAudio: []string{"DDP", "TrueHD"},
			},
			parsed: &parser.ParsedRelease{
				Audio: []string{"DDP5.1"},
			},
			shouldPass: true,
		},
		{
			name: "Non-allowed audio rejected",
			cfg: &config.FilterConfig{
				AllowedAudio: []string{"DDP", "TrueHD"},
			},
			parsed: &parser.ParsedRelease{
				Audio: []string{"AAC"},
			},
			shouldPass: false,
		},
		{
			name: "Min channels 5.1 passes",
			cfg: &config.FilterConfig{
				MinChannels: "5.1",
			},
			parsed: &parser.ParsedRelease{
				Channels: []string{"7.1"},
			},
			shouldPass: true,
		},
		{
			name: "Below min channels rejected",
			cfg: &config.FilterConfig{
				MinChannels: "5.1",
			},
			parsed: &parser.ParsedRelease{
				Channels: []string{"2.0"},
			},
			shouldPass: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := checkAudio(tt.cfg, tt.parsed)
			if result != tt.shouldPass {
				t.Errorf("checkAudio() = %v, want %v", result, tt.shouldPass)
			}
		})
	}
}

// Test HDR Filtering
func TestCheckHDR(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.FilterConfig
		parsed     *parser.ParsedRelease
		shouldPass bool
	}{
		{
			name: "HDR required and present passes",
			cfg: &config.FilterConfig{
				RequireHDR: true,
			},
			parsed: &parser.ParsedRelease{
				HDR: []string{"HDR10"},
			},
			shouldPass: true,
		},
		{
			name: "HDR required but missing rejected",
			cfg: &config.FilterConfig{
				RequireHDR: true,
			},
			parsed: &parser.ParsedRelease{
				HDR: []string{},
			},
			shouldPass: false,
		},
		{
			name: "Blocked HDR type rejected",
			cfg: &config.FilterConfig{
				BlockedHDR: []string{"DV"},
			},
			parsed: &parser.ParsedRelease{
				HDR: []string{"DV", "HDR10"},
			},
			shouldPass: false,
		},
		{
			name: "SDR blocked and present rejected",
			cfg: &config.FilterConfig{
				BlockSDR: true,
			},
			parsed: &parser.ParsedRelease{
				HDR: []string{"SDR"},
			},
			shouldPass: false,
		},
		{
			name: "Allowed HDR type passes",
			cfg: &config.FilterConfig{
				AllowedHDR: []string{"HDR10", "HDR10+"},
			},
			parsed: &parser.ParsedRelease{
				HDR: []string{"HDR10"},
			},
			shouldPass: true,
		},
		{
			name: "Non-allowed HDR type rejected",
			cfg: &config.FilterConfig{
				AllowedHDR: []string{"HDR10"},
			},
			parsed: &parser.ParsedRelease{
				HDR: []string{"DV"},
			},
			shouldPass: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := checkHDR(tt.cfg, tt.parsed)
			if result != tt.shouldPass {
				t.Errorf("checkHDR() = %v, want %v", result, tt.shouldPass)
			}
		})
	}
}

// Test Language Filtering
func TestCheckLanguages(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.FilterConfig
		parsed     *parser.ParsedRelease
		shouldPass bool
	}{
		{
			name: "Required language present passes",
			cfg: &config.FilterConfig{
				RequiredLanguages: []string{"English"},
			},
			parsed: &parser.ParsedRelease{
				Languages: []string{"English"},
			},
			shouldPass: true,
		},
		{
			name: "Required language missing rejected",
			cfg: &config.FilterConfig{
				RequiredLanguages: []string{"English"},
			},
			parsed: &parser.ParsedRelease{
				Languages: []string{"German"},
			},
			shouldPass: false,
		},
		{
			name: "Allowed language passes",
			cfg: &config.FilterConfig{
				AllowedLanguages: []string{"English", "German"},
			},
			parsed: &parser.ParsedRelease{
				Languages: []string{"English"},
			},
			shouldPass: true,
		},
		{
			name: "Non-allowed language rejected",
			cfg: &config.FilterConfig{
				AllowedLanguages: []string{"English"},
			},
			parsed: &parser.ParsedRelease{
				Languages: []string{"French"},
			},
			shouldPass: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := checkLanguages(tt.cfg, tt.parsed)
			if result != tt.shouldPass {
				t.Errorf("checkLanguages() = %v, want %v", result, tt.shouldPass)
			}
		})
	}
}

// Test Other Filters (CAM, Proper, Repack)
func TestCheckOther(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.FilterConfig
		parsed     *parser.ParsedRelease
		shouldPass bool
	}{
		{
			name: "CAM blocked and present rejected",
			cfg: &config.FilterConfig{
				BlockCam: true,
			},
			parsed: &parser.ParsedRelease{
				Quality: "CAM",
			},
			shouldPass: false,
		},
		{
			name: "TS blocked and present rejected",
			cfg: &config.FilterConfig{
				BlockCam: true,
			},
			parsed: &parser.ParsedRelease{
				Quality: "TeleSync",
			},
			shouldPass: false,
		},
		{
			name: "Proper required and present passes",
			cfg: &config.FilterConfig{
				RequireProper: true,
			},
			parsed: &parser.ParsedRelease{
				Proper: true,
			},
			shouldPass: true,
		},
		{
			name: "Proper required but missing rejected",
			cfg: &config.FilterConfig{
				RequireProper: true,
			},
			parsed: &parser.ParsedRelease{
				Proper: false,
			},
			shouldPass: false,
		},
		{
			name: "Repack not allowed and present rejected",
			cfg: &config.FilterConfig{
				AllowRepack: false,
			},
			parsed: &parser.ParsedRelease{
				Repack: true,
			},
			shouldPass: false,
		},
		{
			name: "Repack allowed and present passes",
			cfg: &config.FilterConfig{
				AllowRepack: true,
			},
			parsed: &parser.ParsedRelease{
				Repack: true,
			},
			shouldPass: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := checkOther(tt.cfg, tt.parsed)
			if result != tt.shouldPass {
				t.Errorf("checkOther() = %v, want %v", result, tt.shouldPass)
			}
		})
	}
}

// Test Group Filtering
func TestCheckGroup(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.FilterConfig
		parsed     *parser.ParsedRelease
		shouldPass bool
	}{
		{
			name: "Blocked group rejected",
			cfg: &config.FilterConfig{
				BlockedGroups: []string{"YIFY", "RARBG"},
			},
			parsed: &parser.ParsedRelease{
				Group: "YIFY",
			},
			shouldPass: false,
		},
		{
			name: "Non-blocked group passes",
			cfg: &config.FilterConfig{
				BlockedGroups: []string{"YIFY"},
			},
			parsed: &parser.ParsedRelease{
				Group: "FLUX",
			},
			shouldPass: true,
		},
		{
			name: "Empty group passes",
			cfg: &config.FilterConfig{
				BlockedGroups: []string{"YIFY"},
			},
			parsed: &parser.ParsedRelease{
				Group: "",
			},
			shouldPass: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := checkGroup(tt.cfg, tt.parsed)
			if result != tt.shouldPass {
				t.Errorf("checkGroup() = %v, want %v", result, tt.shouldPass)
			}
		})
	}
}
