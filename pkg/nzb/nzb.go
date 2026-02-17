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

	"golang.org/x/net/html/charset"

	"github.com/MunifTanjim/go-ptt"
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

// GetContentFiles returns all files related to the main content (e.g. all rar volumes)
func (n *NZB) GetContentFiles() []*FileInfo {
	infos := n.GetFileInfo()

	// 1. Identify "Main" file logic extended to groups
	var mainPattern string
	var maxSize int64
	var mainIsArchive bool
	_ = mainIsArchive // Prevent unused error if needed later, or just remove

	// First pass: Find the "main" content (largest video or archive)
	for _, info := range infos {
		if info.IsSample || info.IsExtra {
			continue
		}

		if info.Size > maxSize {
			// Check if it's a valid content type
			if info.IsVideo || isArchivePart(info.Extension) || isSplitArchivePart(info.Extension) || info.Extension == ".rar" || info.Extension == ".7z" {
				maxSize = info.Size
				mainPattern = getFilePattern(info.Filename)
				mainIsArchive = !info.IsVideo
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

	return contentFiles
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
func (n *NZB) CompressionType() string {
	contentFiles := n.GetContentFiles()
	if len(contentFiles) == 0 {
		return "direct"
	}
	lowerName := func(s string) string { return strings.ToLower(s) }
	for _, info := range contentFiles {
		ext := lowerName(info.Extension)
		filename := lowerName(info.Filename)
		// Check 7z first (e.g. file.7z.001 has ext .001 - must not be misclassified as rar)
		if ext == ".7z" || strings.Contains(filename, ".7z.001") {
			return "7z"
		}
		if ext == ".rar" || isArchivePart(ext) || isSplitArchivePart(ext) {
			return "rar"
		}
	}
	return "direct"
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
