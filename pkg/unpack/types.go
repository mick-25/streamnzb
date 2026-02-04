package unpack

import "io"

// UnpackableFile abstracts a file that can be scanned/unpacked.
// Implemented by loader.File (physical) and VirtualFile (nested).
type UnpackableFile interface {
	Name() string
	Size() int64
	OpenStream() (io.ReadSeekCloser, error)
	// OpenReaderAt returns a high-performance linear reader starting at offset
	OpenReaderAt(offset int64) (io.ReadCloser, error)
	ReadAt(p []byte, off int64) (int, error)
}
