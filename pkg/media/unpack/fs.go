package unpack

import (
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"time"
)

// NZBFS implements fs.FS for rardecode, mapping filenames to UnpackableFile streams.
type NZBFS struct {
	files map[string]UnpackableFile
}

func NewNZBFSFromMap(files map[string]UnpackableFile) *NZBFS {
	return &NZBFS{files: files}
}

func (n *NZBFS) Open(name string) (fs.File, error) {
	f, ok := n.files[name]
	if !ok {
		f, ok = n.files[filepath.Base(name)]
	}
	if !ok {
		return nil, fs.ErrNotExist
	}

	stream, err := f.OpenStream()
	if err != nil {
		return nil, fmt.Errorf("failed to open stream: %w", err)
	}

	return &fileWrapper{
		stream: stream,
		file:   f,
		name:   ExtractFilename(f.Name()),
		size:   f.Size(),
	}, nil
}

type fileWrapper struct {
	stream io.ReadSeekCloser
	file   UnpackableFile
	name   string
	size   int64
}

func (fw *fileWrapper) Stat() (fs.FileInfo, error) {
	return &fileInfo{name: fw.name, size: fw.size}, nil
}

func (fw *fileWrapper) Read(p []byte) (int, error)            { return fw.stream.Read(p) }
func (fw *fileWrapper) Seek(off int64, whence int) (int64, error) { return fw.stream.Seek(off, whence) }
func (fw *fileWrapper) Close() error                           { return fw.stream.Close() }
func (fw *fileWrapper) ReadAt(p []byte, off int64) (int, error) { return fw.file.ReadAt(p, off) }

type fileInfo struct {
	name string
	size int64
}

func (fi *fileInfo) Name() string      { return fi.name }
func (fi *fileInfo) Size() int64       { return fi.size }
func (fi *fileInfo) Mode() fs.FileMode { return 0444 }
func (fi *fileInfo) ModTime() time.Time { return time.Time{} }
func (fi *fileInfo) IsDir() bool       { return false }
func (fi *fileInfo) Sys() interface{}  { return nil }
