package unpack

import (
	"strings"
)

// Extension constants
const (
	ExtRar  = ".rar"
	ExtZip  = ".zip"
	Ext7z   = ".7z"
	ExtIso  = ".iso"
	ExtMkv  = ".mkv"
	ExtMp4  = ".mp4"
	ExtAvi  = ".avi"
	ExtPar2 = ".par2"
	ExtNfo  = ".nfo"
	ExtNzb  = ".nzb"
)

// ExtractFilename attempts to find a clean filename in the subject string.
// It handles quoted strings and removes common NZB suffixes like (1/23).
func ExtractFilename(subject string) string {
	// 1. Try to extract from quotes first "filename.ext"
	if start := strings.Index(subject, "\""); start != -1 {
		if end := strings.Index(subject[start+1:], "\""); end != -1 {
			return subject[start+1 : start+1+end]
		}
	}

	// 2. Clean common NZB suffixes
	clean := strings.TrimSpace(subject)
	
	// Remove trailing (x/y) or [x/y]
	if idx := strings.LastIndex(clean, " ("); idx != -1 {
		// Verify it looks like (1/2)
		suffix := clean[idx:]
		if strings.Contains(suffix, "/") && strings.HasSuffix(suffix, ")") {
			clean = strings.TrimSpace(clean[:idx])
		}
	}
	
	// Remove trailing " yEnc"
	if strings.HasSuffix(clean, " yEnc") {
		clean = strings.TrimSuffix(clean, " yEnc")
		clean = strings.TrimSpace(clean)
	}

	return clean
}

// IsVideoFile checks if the filename has a common video extension.
func IsVideoFile(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ExtMkv) || 
	       strings.HasSuffix(lower, ExtMp4) || 
	       strings.HasSuffix(lower, ExtAvi)
}

// IsArchiveFile checks if the filename involves an archive (RAR, Zip, 7z, ISO).
func IsArchiveFile(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ExtRar) || 
	       strings.HasSuffix(lower, ExtZip) || 
	       strings.HasSuffix(lower, Ext7z) || 
	       strings.HasSuffix(lower, ExtIso) ||
	       IsRarPart(lower)
}

// IsSampleFile checks if the filename looks like a sample/trailer.
func IsSampleFile(name string) bool {
	lower := strings.ToLower(name)
	// Check specifically for "sample" in the name, but simplistic check might be enough
	return strings.Contains(lower, "sample")
}

// IsRarPart checks if extension is .rXX (e.g. .r01, .r99)
func IsRarPart(name string) bool {
	if len(name) < 4 {
		return false
	}

	// Check last 4 chars: .rNN
	ext := name[len(name)-4:]
	if ext[0] != '.' || ext[1] != 'r' {
		return false
	}

	// Check digits
	return isDigit(ext[2]) && isDigit(ext[3])
}

// IsMiddleRarVolume checks if a RAR file is a middle volume (not the first)
func IsMiddleRarVolume(name string) bool {
	name = strings.ToLower(name)
	
	// Match .partXX.rar format
	if strings.Contains(name, ".part") && strings.HasSuffix(name, ExtRar) {
		// First volumes: .part1.rar, .part01.rar, .part001.rar
		// Note: we check for "part1.", "part01.", "part001."
		// Because the dot after part number is important (part10 vs part1)
		if strings.Contains(name, ".part1.rar") || 
		   strings.Contains(name, ".part01.rar") || 
		   strings.Contains(name, ".part001.rar") {
			return false
		}
		// Any other .partXX.rar is a middle volume
		return true
	}
	
	// Match .r01, .r02, etc. (but not .r00 or .rar)
	if len(name) >= 4 && name[len(name)-4:len(name)-2] == ".r" {
		lastTwo := name[len(name)-2:]
		// .r00 or .rar is first volume, .r01+ are middle volumes
		if lastTwo != "ar" && lastTwo != "00" {
			return true
		}
	}
	
	return false
}

func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}
