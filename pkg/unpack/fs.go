package unpack

import (
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
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
		name := ExtractFilename(f.Name())
		m[name] = f
	}
	return &NZBFS{files: m}
}

func NewNZBFSFromMap(files map[string]UnpackableFile) *NZBFS {
	return &NZBFS{files: files}
}

func (n *NZBFS) Open(name string) (fs.File, error) {
	// Try exact match first (supports full paths)
	f, ok := n.files[name]
	if !ok {
		// Fallback: try basename (handles ./file.rar or path mismatch)
		base := filepath.Base(name)
		f, ok = n.files[base]
	}

	if !ok {
		logger.Debug("NZBFS: File not found", "name", name)
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
		Name:   ExtractFilename(f.Name()),
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
