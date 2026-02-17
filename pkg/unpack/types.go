package unpack

import (
	"context"
	"io"
)

// UnpackableFile abstracts a file that can be scanned/unpacked.
// Implemented by loader.File (physical) and VirtualFile (nested).
type UnpackableFile interface {
	Name() string
	Size() int64
	OpenStream() (io.ReadSeekCloser, error)
	OpenReaderAt(ctx context.Context, offset int64) (io.ReadCloser, error)
	ReadAt(p []byte, off int64) (int, error)
}
