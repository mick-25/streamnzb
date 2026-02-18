package unpack

import "strings"

const (
	ExtRar  = ".rar"
	ExtZip  = ".zip"
	Ext7z   = ".7z"
	ExtIso  = ".iso"
	ExtMkv  = ".mkv"
	ExtMp4  = ".mp4"
	ExtAvi  = ".avi"
	ExtM2ts = ".m2ts"
	ExtTs   = ".ts"
	ExtVob  = ".vob"
	ExtWmv  = ".wmv"
	ExtFlv  = ".flv"
	ExtWebm = ".webm"
	ExtMov  = ".mov"
	ExtPar2 = ".par2"
	ExtNfo  = ".nfo"
	ExtNzb  = ".nzb"
)

var videoExts = []string{
	ExtMkv, ExtMp4, ExtAvi, ExtM2ts, ExtTs,
	ExtVob, ExtWmv, ExtFlv, ExtWebm, ExtMov,
}

// ExtractFilename extracts a clean filename from an NZB subject line.
func ExtractFilename(subject string) string {
	// Quoted filename takes priority
	if start := strings.Index(subject, "\""); start != -1 {
		if end := strings.Index(subject[start+1:], "\""); end != -1 {
			return subject[start+1 : start+1+end]
		}
	}

	clean := strings.TrimSpace(subject)

	// Strip trailing (x/y) or [x/y] segment counters
	if idx := strings.LastIndex(clean, " ("); idx != -1 {
		suffix := clean[idx:]
		if strings.Contains(suffix, "/") && strings.HasSuffix(suffix, ")") {
			clean = strings.TrimSpace(clean[:idx])
		}
	}
	if idx := strings.LastIndex(clean, " ["); idx != -1 {
		suffix := clean[idx:]
		if strings.Contains(suffix, "/") && strings.HasSuffix(suffix, "]") {
			clean = strings.TrimSpace(clean[:idx])
		}
	}

	// Strip trailing " yEnc"
	clean = strings.TrimSuffix(clean, " yEnc")
	return strings.TrimSpace(clean)
}

func IsVideoFile(name string) bool {
	lower := strings.ToLower(name)
	for _, ext := range videoExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

func IsArchiveFile(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ExtRar) ||
		strings.HasSuffix(lower, ExtZip) ||
		strings.HasSuffix(lower, Ext7z) ||
		strings.HasSuffix(lower, ExtIso) ||
		IsRarPart(lower) ||
		IsSplitArchivePart(lower)
}

func IsSampleFile(name string) bool {
	return strings.Contains(strings.ToLower(name), "sample")
}

// IsRarPart returns true for .rNN extensions (e.g. .r01, .r99).
func IsRarPart(name string) bool {
	if len(name) < 4 {
		return false
	}
	ext := name[len(name)-4:]
	return ext[0] == '.' && ext[1] == 'r' && isDigit(ext[2]) && isDigit(ext[3])
}

// IsMiddleRarVolume returns true for non-first RAR volumes.
func IsMiddleRarVolume(name string) bool {
	name = strings.ToLower(name)

	// .partXX.rar: first = part1/part01/part001
	if strings.Contains(name, ".part") && strings.HasSuffix(name, ExtRar) {
		if strings.Contains(name, ".part1.rar") ||
			strings.Contains(name, ".part01.rar") ||
			strings.Contains(name, ".part001.rar") {
			return false
		}
		return true
	}

	// .rNN: first = .r00, middle = .r01+
	if len(name) >= 4 && name[len(name)-4:len(name)-2] == ".r" {
		last := name[len(name)-2:]
		if last != "ar" && last != "00" {
			return true
		}
	}
	return false
}

// IsSplitArchivePart returns true for .zNN and .NNN split extensions.
func IsSplitArchivePart(name string) bool {
	if len(name) < 4 {
		return false
	}
	ext := strings.ToLower(name[len(name)-4:])

	// .zNN (zip/7z split)
	if ext[0] == '.' && ext[1] == 'z' && isDigit(ext[2]) && isDigit(ext[3]) {
		return true
	}
	// .NNN (7z/HJSplit)
	if ext[0] == '.' && isDigit(ext[1]) && isDigit(ext[2]) && isDigit(ext[3]) {
		return true
	}
	return false
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }
