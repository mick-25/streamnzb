package unpack

import (
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"streamnzb/pkg/logger"
)

type NZBFS struct {
	files map[string]UnpackableFile
}

func NewNZBFS(files []UnpackableFile) *NZBFS {
	m := make(map[string]UnpackableFile)
	for _, f := range files {
		// Clean the name to match request
		name := extractFilename(f.Name())
		m[name] = f
	}
	return &NZBFS{files: m}
}

func NewNZBFSFromMap(files map[string]UnpackableFile) *NZBFS {
	return &NZBFS{files: files}
}

// extractFilename attempts to find a filename in the subject string.
// Very basic implementation.
func extractFilename(subject string) string {
	// 1. Try to extract from quotes first "filename.ext"
	if start := strings.Index(subject, "\""); start != -1 {
		if end := strings.Index(subject[start+1:], "\""); end != -1 {
			return subject[start+1 : start+1+end]
		}
	}

	// 2. Clean common NZB suffixes if no quotes found or fallthrough
	// Remove (1/23) or [1/23] patterns at end
	// Simple approach: Split by common separators and look for extension?
	// Or just trim specific patterns.
	
	clean := strings.TrimSpace(subject)
	
	// Remove trailing (x/y) or [x/y]
	// iterate backwards?
	// Regex is expensive but safest here.
	// But let's try manual trimming to avoid importing regexp if not needed
	// Actually we need regexp for robustness.
	// But let's look for " (" and trim if it ends with ")"
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

func (n *NZBFS) Open(name string) (fs.File, error) {
	// rardecode might request "./movie.part02.rar"
	base := filepath.Base(name)
	
	f, ok := n.files[base]
	if !ok {
		logger.Debug("NZBFS: File not found", "name", base)
		return nil, fs.ErrNotExist
	}
	// NZBFS: Opening
	
	stream, err := f.OpenStream()
	if err != nil {
		return nil, fmt.Errorf("failed to open stream: %w", err)
	}

	return &FileWrapper{
		Stream: stream,
		File:   f,
		Name:   extractFilename(f.Name()),
		Size:   f.Size(),
	}, nil
}

type FileWrapper struct {
	Stream io.ReadSeekCloser
	File   UnpackableFile
	Name   string
	Size   int64
}

func (fw *FileWrapper) Stat() (fs.FileInfo, error) {
	// logger.Debug("NZBFS: Stat", "name", fw.Name)
	return &FileInfo{name: fw.Name, size: fw.Size}, nil
}

func (fw *FileWrapper) Read(p []byte) (int, error) {
	// logger.Debug("NZBFS: Read START", "name", fw.Name, "len", len(p))
	n, err := fw.Stream.Read(p)
	if err != nil {
		logger.Debug("NZBFS: Read Error", "name", fw.Name, "len", len(p), "err", err)
	} else {
		// logger.Debug("NZBFS: Read OK", "name", fw.Name, "len", len(p), "n", n)
	}
	return n, err
}

func (fw *FileWrapper) Seek(offset int64, whence int) (int64, error) {
	logger.Debug("NZBFS: Seek", "name", fw.Name, "offset", offset, "whence", whence)
	return fw.Stream.Seek(offset, whence)
}

func (fw *FileWrapper) Close() error {
	logger.Debug("NZBFS: Close", "name", fw.Name)
	return fw.Stream.Close()
}

func (fw *FileWrapper) ReadAt(p []byte, off int64) (int, error) {
	// Delegate to concurrent-safe loader.File.ReadAt
	// logger.Debug("NZBFS: ReadAt START", "name", fw.Name, "off", off, "len", len(p))
	n, err := fw.File.ReadAt(p, off)
	if err != nil {
		logger.Debug("NZBFS: ReadAt Error", "name", fw.Name, "off", off, "len", len(p), "err", err)
	}
	return n, err
}

// FileInfo mocks fs.FileInfo
type FileInfo struct {
	name string
	size int64
}

func (fi *FileInfo) Name() string       { return fi.name }
func (fi *FileInfo) Size() int64        { return fi.size }
func (fi *FileInfo) Mode() fs.FileMode  { return 0444 }
func (fi *FileInfo) ModTime() time.Time { return time.Now() }
func (fi *FileInfo) IsDir() bool        { return false }
func (fi *FileInfo) Sys() interface{}   { return nil }
