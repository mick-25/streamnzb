package loader

import (
	"errors"
	"io"
	"sync"
)

// BufferedStream provides a ReadSeekSeaker by wrapping SmartStream
// It allows seeking by restarting the underlying linear SmartStream.
type BufferedStream struct {
	file          *File
	currentStream io.ReadCloser
	offset        int64
	mu            sync.Mutex
}

func NewBufferedStream(f *File) *BufferedStream {
	// Start at 0
	return &BufferedStream{
		file:          f,
		currentStream: f.OpenSmartStream(0),
		offset:        0,
	}
}

func (s *BufferedStream) Read(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.currentStream == nil {
		return 0, io.ErrClosedPipe
	}

	n, err = s.currentStream.Read(p)
	s.offset += int64(n)
	return n, err
}

func (s *BufferedStream) Seek(offset int64, whence int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.currentStream == nil {
		return 0, io.ErrClosedPipe
	}

	var newOffset int64
	switch whence {
	case io.SeekStart:
		newOffset = offset
	case io.SeekCurrent:
		newOffset = s.offset + offset
	case io.SeekEnd:
		newOffset = s.file.Size() + offset
	default:
		return 0, errors.New("invalid whence")
	}

	if newOffset < 0 {
		return 0, errors.New("seek out of bounds")
	}

	// Optimization: If seeking to current offset, do nothing
	if newOffset == s.offset {
		return newOffset, nil
	}

	// Close existing stream
	s.currentStream.Close()

	// Start new SmartStream at new offset
	s.currentStream = s.file.OpenSmartStream(newOffset)
	s.offset = newOffset

	return newOffset, nil
}

func (s *BufferedStream) Tell() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.offset
}

func (s *BufferedStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.currentStream != nil {
		err := s.currentStream.Close()
		s.currentStream = nil
		return err
	}
	return nil
}
