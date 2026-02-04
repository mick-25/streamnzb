package unpack

import (
	"io"
)

// VirtualFile implements UnpackableFile for a file inside an archive.
type VirtualFile struct {
	name   string
	size   int64
	parts  []virtualPart
}

func NewVirtualFile(name string, size int64, parts []virtualPart) *VirtualFile {
	return &VirtualFile{
		name:   name,
		size:   size,
		parts:  parts,
	}
}

func (f *VirtualFile) Name() string {
	return f.name
}

func (f *VirtualFile) Size() int64 {
	return f.size
}

func (f *VirtualFile) OpenStream() (io.ReadSeekCloser, error) {
	// Create a new independent stream for this file
	return NewVirtualStream(f.parts, f.size, 0), nil
}

func (f *VirtualFile) OpenReaderAt(offset int64) (io.ReadCloser, error) {
	// Create a stream starting at offset (optimized)
	return NewVirtualStream(f.parts, f.size, offset), nil
}

func (f *VirtualFile) ReadAt(p []byte, off int64) (int, error) {
	s := NewVirtualStream(f.parts, f.size, 0)
	defer s.Close()
	
	if _, err := s.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}
	
	n, err := io.ReadFull(s, p)
	// io.ReadFull returns io.ErrUnexpectedEOF if n > 0 && n < len(p)
	// ReaderAt allows returning EOF in that case (if limits hit)
	if err == io.ErrUnexpectedEOF {
		return n, io.EOF
	}
	return n, err
}
