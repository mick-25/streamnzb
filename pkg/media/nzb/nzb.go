package nzb

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/MunifTanjim/go-ptt"
	"golang.org/x/net/html/charset"

	"streamnzb/pkg/core/logger"
)

type NZB struct {
	XMLName xml.Name `xml:"nzb"`
	Head    Head     `xml:"head"`
	Files   []File   `xml:"file"`
}

type Head struct {
	Meta []Meta `xml:"meta"`
}

type Meta struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type File struct {
	Poster   string    `xml:"poster,attr"`
	Date     int64     `xml:"date,attr"`
	Subject  string    `xml:"subject,attr"`
	Groups   []string  `xml:"groups>group"`
	Segments []Segment `xml:"segments>segment"`
}

type Segment struct {
	Bytes  int64  `xml:"bytes,attr"`
	Number int    `xml:"number,attr"`
	ID     string `xml:",chardata"`
}

// FileInfo contains parsed information about an NZB file
type FileInfo struct {
	File       *File
	Filename   string
	Extension  string
	Size       int64
	IsVideo    bool
	IsSample   bool
	IsExtra    bool
	ParsedInfo *ptt.Result
}

func Parse(r io.Reader) (*NZB, error) {
	var nzb NZB
	decoder := xml.NewDecoder(r)
	decoder.CharsetReader = charset.NewReaderLabel
	if err := decoder.Decode(&nzb); err != nil {
		return nil, err
	}
	return &nzb, nil
}

func ParseFile(path string) (*NZB, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

// Hash generates a unique hash for this NZB (for caching)
func (n *NZB) Hash() string {
	if len(n.Files) == 0 {
		return ""
	}

	// Use first file's subject as identifier
	h := sha256.New()
	h.Write([]byte(n.Files[0].Subject))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// CalculateID computes the SHA-1 hash of the first Message-ID for AvailNZB.
func (n *NZB) CalculateID() string {
	if len(n.Files) == 0 || len(n.Files[0].Segments) == 0 {
		return ""
	}

	// Use the first segment ID of the first file as the primary Message-ID
	msgID := n.Files[0].Segments[0].ID
	msgID = strings.Trim(msgID, "<>")
	h := sha1.New()
	h.Write([]byte(msgID))
	return hex.EncodeToString(h.Sum(nil))
}

// TotalSize returns the total size of all files in bytes
func (n *NZB) TotalSize() int64 {
	var total int64
	for _, file := range n.Files {
		for _, seg := range file.Segments {
			total += seg.Bytes
		}
	}
	return total
}

// GetFileInfo returns parsed information about all files in the NZB
func (n *NZB) GetFileInfo() []*FileInfo {
	infos := make([]*FileInfo, 0, len(n.Files))

	for i := range n.Files {
		file := &n.Files[i]
		info := analyzeFile(file)
		infos = append(infos, info)
	}

	return infos
}

// GetLargestContentFile returns the single largest content file (video or archive), excluding samples/extras.
// Used for compression detection: the biggest file is the main content to inspect.
func (n *NZB) GetLargestContentFile() *FileInfo {
	infos := n.GetFileInfo()
	var largest *FileInfo
	var maxSize int64
	for _, info := range infos {
		if info.IsSample || info.IsExtra {
			continue
		}
		if info.Size <= maxSize {
			continue
		}
		if info.IsVideo || info.Extension == ".rar" || info.Extension == ".7z" ||
			isArchivePart(info.Extension) || isRarVolume(info.Extension) ||
			isSplitArchivePart(info.Extension) || isRarSplitPart(info.Extension, info.Filename) {
			maxSize = info.Size
			largest = info
		}
	}
	return largest
}

// GetContentFiles returns all files related to the main content (e.g. all rar volumes)
func (n *NZB) GetContentFiles() []*FileInfo {
	infos := n.GetFileInfo()

	// 1. Identify "Main" file logic extended to groups
	var mainPattern string
	var maxSize int64

	// First pass: Find the "main" content (largest video or archive)
	for _, info := range infos {
		if info.IsSample || info.IsExtra {
			continue
		}

		if info.Size > maxSize {
			// Check if it's a valid content type
			if info.IsVideo || info.Extension == ".rar" || info.Extension == ".7z" ||
				isArchivePart(info.Extension) || isRarVolume(info.Extension) ||
				isSplitArchivePart(info.Extension) || isRarSplitPart(info.Extension, info.Filename) {
				maxSize = info.Size
				mainPattern = getFilePattern(info.Filename)
			}
		}
	}

	// If no main content found, fallback to largest file overall
	if mainPattern == "" {
		for _, info := range infos {
			if info.IsSample || info.IsExtra {
				continue
			}
			if info.Size > maxSize {
				maxSize = info.Size
				mainPattern = getFilePattern(info.Filename)
			}
		}
	}

	// 2. Collect all files matching the main pattern
	var contentFiles []*FileInfo
	if mainPattern != "" {
		for _, info := range infos {
			if getFilePattern(info.Filename) == mainPattern {
				contentFiles = append(contentFiles, info)
			}
		}
	}

	if len(contentFiles) == 0 {
		logGetContentFilesEmpty(infos, mainPattern)
	}

	return contentFiles
}

// logGetContentFilesEmpty logs debug info when GetContentFiles returns empty
func logGetContentFilesEmpty(infos []*FileInfo, mainPattern string) {
	total := len(infos)
	samples := 0
	extras := 0
	subjects := make([]string, 0, 8)
	for _, info := range infos {
		if info.IsSample {
			samples++
		}
		if info.IsExtra {
			extras++
		}
		if len(subjects) < 8 {
			subjects = append(subjects, info.Filename)
		}
	}
	logger.Debug("GetContentFiles returned empty",
		"total_files", total,
		"samples", samples,
		"extras", extras,
		"main_pattern", mainPattern,
		"sample_filenames", subjects)
}

// getFilePattern simplifies filename to find related parts (e.g. "movie.part01.rar" -> "movie")
func getFilePattern(filename string) string {
	// Very simple grouping: remove numeric suffixes and extensions
	// "Release.Name.part01.rar" -> "release.name"
	// "Release.Name.r00" -> "release.name"
	// "Release.Name.mkv" -> "release.name"

	s := strings.ToLower(filename)

	// Remove extensions
	ext := filepath.Ext(s)
	s = strings.TrimSuffix(s, ext)

	// Remove common multipart suffixes
	// part01, vol01, .r01
	if idx := strings.LastIndex(s, ".part"); idx != -1 {
		s = s[:idx]
	}
	if idx := strings.LastIndex(s, ".vol"); idx != -1 {
		s = s[:idx]
	}

	// Handle .7z.001 style
	s = strings.TrimSuffix(s, ".7z")

	return strings.Trim(s, " .-_")
}

// IsRARRelease returns true if the main content of the release is RAR-based.
// RAR playback is not supported due to seeking issues.
func (n *NZB) IsRARRelease() bool {
	return n.CompressionType() == "rar"
}

// CompressionType returns the release compression type for AvailNZB: "rar", "7z", or "direct".
// Looks at the full release: RAR only if we find .rar or .r00-style files; .001-style is RAR only
// when the release also contains definitive RAR files (avoids 7z false positives).
func (n *NZB) CompressionType() string {
	contentFiles := n.GetContentFiles()
	if len(contentFiles) == 0 {
		return "direct"
	}

	// 1. Scan entire release for definitive 7z (.7z or .7z.001 in any filename)
	for _, info := range contentFiles {
		if info.Extension == ".7z" || strings.Contains(strings.ToLower(info.Filename), ".7z.001") {
			return "7z"
		}
	}

	// 2. Scan entire release for definitive RAR (.rar or .r00-style)
	hasRarFiles := false
	for _, info := range contentFiles {
		ext := strings.ToLower(info.Extension)
		if ext == ".rar" || isRarVolume(ext) {
			hasRarFiles = true
			break
		}
	}

	// 3. If we have .rar/.r00 anywhere, it's RAR (including when largest is .001-style)
	if hasRarFiles {
		largest := n.GetLargestContentFile()
		if largest != nil {
			logger.Debug("CompressionType detected rar",
				"filename", largest.Filename,
				"extension", largest.Extension,
				"reason", "release contains .rar or .r00-style files",
				"largest_size", largest.Size)
		}
		return "rar"
	}

	// 4. No .rar/.r00 in release - .001-style is ambiguous, treat as direct (likely 7z)
	largest := n.GetLargestContentFile()
	if largest == nil {
		return "direct"
	}
	ct, _ := compressionTypeFromFileWithReason(largest.Filename, largest.Extension)
	return ct
}

// compressionTypeFromFileWithReason returns compression type and a short reason for debugging.
func compressionTypeFromFileWithReason(filename, ext string) (string, string) {
	ext = strings.ToLower(ext)
	filenameLower := strings.ToLower(filename)

	// 7z first: .7z, .7z.001 (ext would be .001 but filename contains .7z.001)
	if ext == ".7z" || strings.Contains(filenameLower, ".7z.001") {
		return "7z", "ext=.7z or contains .7z.001"
	}
	if strings.HasSuffix(filenameLower, ".7z.001") || strings.HasSuffix(filenameLower, ".7z.0001") {
		return "7z", "suffix .7z.001/.7z.0001"
	}

	// RAR: .rar, .r00-.r99, .r001+ (RAR-specific patterns only).
	if ext == ".rar" {
		return "rar", "ext=.rar"
	}
	if isRarVolume(ext) {
		return "rar", "isRarVolume(ext)"
	}

	return "direct", ""
}

// isRarVolume matches .r00, .r01, .r99, .r001, .r100, .r999, etc.
func isRarVolume(ext string) bool {
	if len(ext) < 4 || !strings.HasPrefix(ext, ".r") {
		return false
	}
	for _, c := range ext[2:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// isRarSplitPart matches .001, .002, .0001 etc. when not 7z (caller checks 7z first).
func isRarSplitPart(ext, filename string) bool {
	if len(ext) < 3 || ext[0] != '.' {
		return false
	}
	for _, c := range ext[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// GetMainVideoFile returns the main video file from the NZB (Deprecated: use GetContentFiles)
func (n *NZB) GetMainVideoFile() *FileInfo {
	files := n.GetContentFiles()
	if len(files) > 0 {
		return files[0]
	}
	return nil
}

// analyzeFile extracts information from a file's subject line
func analyzeFile(file *File) *FileInfo {
	// Extract filename from subject
	// Subject format is usually: "filename" yEnc (1/50) or similar
	filename := ExtractFilename(file.Subject)

	// Calculate total size
	var size int64
	for _, seg := range file.Segments {
		size += seg.Bytes
	}

	// Get extension
	ext := strings.ToLower(filepath.Ext(filename))

	// Parse the filename for metadata
	parsed := ptt.Parse(filename)

	info := &FileInfo{
		File:       file,
		Filename:   filename,
		Extension:  ext,
		Size:       size,
		ParsedInfo: parsed,
	}

	// Determine file type
	info.IsVideo = isVideoExtension(ext)
	info.IsSample = isSampleFile(filename)
	info.IsExtra = isExtraFile(filename, ext)

	return info
}

// ExtractFilename extracts the filename from an NZB subject line
func ExtractFilename(subject string) string {
	// Common patterns:
	// "filename.mkv" yEnc (1/50)
	// filename.mkv (1/50)
	// [1/50] - "filename.mkv" yEnc

	// Try to find quoted filename
	if start := strings.Index(subject, "\""); start != -1 {
		if end := strings.Index(subject[start+1:], "\""); end != -1 {
			return subject[start+1 : start+1+end]
		}
	}

	// Try to extract before yEnc or (1/50) pattern
	subject = strings.TrimSpace(subject)

	if idx := strings.Index(subject, " yEnc"); idx != -1 {
		subject = subject[:idx]
	}

	if idx := strings.Index(subject, " ("); idx != -1 {
		subject = subject[:idx]
	}

	return strings.Trim(subject, "\"' ")
}

// isVideoExtension checks if the extension is a video format
func isVideoExtension(ext string) bool {
	videoExts := map[string]bool{
		".mkv": true, ".mp4": true, ".avi": true, ".m4v": true,
		".mov": true, ".wmv": true, ".flv": true, ".webm": true,
		".mpg": true, ".mpeg": true, ".m2ts": true, ".ts": true,
		".iso": true, ".vob": true,
		// Archives that likely contain video
		".rar": true, ".7z": true,
	}
	return videoExts[ext] || isArchivePart(ext)
}

func isArchivePart(ext string) bool {
	// Check for .r00, .r01, .r99 (RAR volume naming)
	if len(ext) == 4 && strings.HasPrefix(ext, ".r") {
		for _, c := range ext[2:] {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	return false
}

// isSplitArchivePart matches .001, .002, .117, etc. (RAR/7z split volume naming).
// 7z is checked before this in CompressionType so .7z.001 files are not misclassified.
func isSplitArchivePart(ext string) bool {
	if len(ext) != 4 {
		return false
	}
	return ext[0] == '.' &&
		ext[1] >= '0' && ext[1] <= '9' &&
		ext[2] >= '0' && ext[2] <= '9' &&
		ext[3] >= '0' && ext[3] <= '9'
}

// isSampleFile checks if the filename indicates a sample
func isSampleFile(filename string) bool {
	lower := strings.ToLower(filename)
	return strings.Contains(lower, "sample") ||
		strings.Contains(lower, "preview")
}

// isExtraFile checks if the file is an extra (subtitle, NFO, etc.)
func isExtraFile(filename string, ext string) bool {
	extraExts := map[string]bool{
		".nfo": true, ".txt": true, ".srt": true, ".sub": true,
		".idx": true, ".ass": true, ".ssa": true, ".vtt": true,
		".jpg": true, ".png": true, ".gif": true,
		// Parity files
		".par2": true,
	}

	if extraExts[ext] {
		return true
	}

	lower := strings.ToLower(filename)
	return strings.Contains(lower, "proof") ||
		strings.Contains(lower, "cover")
}
