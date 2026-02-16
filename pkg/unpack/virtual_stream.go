package unpack

import (
	"errors"
	"fmt"
	"io"
	"streamnzb/pkg/logger"
	"sync"
)

// virtualPart maps a range of the virtual file to a physical location
type virtualPart struct {
	VirtualStart int64
	VirtualEnd   int64

	VolFile   UnpackableFile
	VolOffset int64
}

type VirtualStream struct {
	parts     []virtualPart
	totalSize int64

	currentOffset int64
	mu            sync.Mutex // Protects currentOffset, currentBuf, bufOffset, bufferedStart, bufferedEnd

	dataChan  chan []byte
	errChan   chan error
	closeChan chan struct{}
	seekChan  chan int64

	currentBuf []byte
	bufOffset  int

	currentReader  io.ReadCloser
	currentPartIdx int

	// Buffer tracking for smart seek optimization
	bufferedStart int64 // Start of currently buffered range
	bufferedEnd   int64 // End of currently buffered range

	workerOnce sync.Once
}

func NewVirtualStream(parts []virtualPart, totalSize int64, startOffset int64) *VirtualStream {
	vs := &VirtualStream{
		parts:          parts,
		totalSize:      totalSize,
		dataChan:       make(chan []byte, 50),
		errChan:        make(chan error, 1),
		closeChan:      make(chan struct{}),
		seekChan:       make(chan int64),
		currentPartIdx: -1,
		bufferedStart:   -1,
		bufferedEnd:     -1,
	}
	go vs.worker(startOffset)
	return vs
}

// findPart uses binary search to find the part containing the given offset
// Returns (part pointer, part index) or (nil, -1) if not found
func (s *VirtualStream) findPart(offset int64) (*virtualPart, int) {
	// Binary search since parts are sorted by VirtualStart
	left, right := 0, len(s.parts)-1
	for left <= right {
		mid := (left + right) / 2
		part := &s.parts[mid]
		if offset >= part.VirtualStart && offset < part.VirtualEnd {
			return part, mid
		}
		if offset < part.VirtualStart {
			right = mid - 1
		} else {
			left = mid + 1
		}
	}
	return nil, -1
}

func (s *VirtualStream) worker(initialOffset int64) {
	var currentOffset int64 = initialOffset
	const chunkSize = 1024 * 1024 // 1MB chunks

	select {
	case off := <-s.seekChan:
		currentOffset = off
	default:
	}

	for {
		if currentOffset >= s.totalSize {
			select {
			case s.errChan <- io.EOF:
				select {
				case <-s.closeChan:
					return
				case off := <-s.seekChan:
					currentOffset = off
				}
			case <-s.closeChan:
				return
			case off := <-s.seekChan:
				currentOffset = off
			}
			continue
		}

		// Binary search for part lookup (O(log n) instead of O(n))
		activePart, partIdx := s.findPart(currentOffset)

		if activePart == nil {
			logger.Error("VirtualStream: offset not mapped", "offset", currentOffset, "totalSize", s.totalSize, "parts", len(s.parts))
			select {
			case s.errChan <- fmt.Errorf("offset %d not mapped", currentOffset):
				return
			case <-s.closeChan:
				return
			}
			continue
		}

		remaining := activePart.VirtualEnd - currentOffset

		// Optimize: Use cached reader if possible
		if s.currentReader == nil || s.currentPartIdx != partIdx {
			// Close old reader
			if s.currentReader != nil {
				s.currentReader.Close()
				s.currentReader = nil
			}

			// Open new Stream at offset using efficient OpenReaderAt
			localOff := currentOffset - activePart.VirtualStart
			volOff := activePart.VolOffset + localOff

			r, err := activePart.VolFile.OpenReaderAt(volOff)
			if err != nil {
				select {
				case s.errChan <- err:
					return
				case <-s.closeChan:
					return
				}
			}

			logger.Debug("VirtualStream: opening volume", "partIdx", partIdx, "volFile", activePart.VolFile.Name(), "volOffset", volOff, "virtualOffset", currentOffset)
			s.currentReader = r
			s.currentPartIdx = partIdx
		}

		// Read from stream
		readSize := int64(chunkSize)
		if readSize > remaining {
			readSize = remaining
		}

		buf := make([]byte, readSize)
		n, err := s.currentReader.Read(buf)

		if n > 0 {
			// Update buffered range tracking atomically
			// Track the range of data we're about to send
			s.mu.Lock()
			if s.bufferedStart == -1 {
				s.bufferedStart = currentOffset
			}
			s.bufferedEnd = currentOffset + int64(n)
			s.mu.Unlock()
			
			// Send data
			select {
			case s.dataChan <- buf[:n]:
			case <-s.closeChan:
				s.currentReader.Close()
				return
			case off := <-s.seekChan:
				currentOffset = off
				s.currentReader.Close()
				s.currentReader = nil
				// Reset buffer tracking on seek
				s.mu.Lock()
				s.bufferedStart = -1
				s.bufferedEnd = -1
				s.mu.Unlock()
				continue
			}
			currentOffset += int64(n)
		}

		if err != nil {
			if err == io.EOF {
				// EOF on this part - move to next part
				logger.Debug("VirtualStream: EOF on volume", "partIdx", s.currentPartIdx, "advancing to", activePart.VirtualEnd)
				s.currentReader.Close()
				s.currentReader = nil
				// Advance to end of this part so we move to the next one
				currentOffset = activePart.VirtualEnd
			} else {
				select {
				case s.errChan <- err:
				case <-s.closeChan:
					s.currentReader.Close()
					return
				case off := <-s.seekChan:
					currentOffset = off
					s.currentReader.Close()
					s.currentReader = nil
				}
			}
		}
	}
}

func (s *VirtualStream) Read(p []byte) (n int, err error) {
	s.mu.Lock()
	if len(s.currentBuf) == 0 {
		s.mu.Unlock()
		select {
		case buf := <-s.dataChan:
			s.mu.Lock()
			s.currentBuf = buf
			s.bufOffset = 0
			s.mu.Unlock()
		case err := <-s.errChan:
			return 0, err
		case <-s.closeChan:
			return 0, io.ErrClosedPipe
		}
		s.mu.Lock()
	}

	available := len(s.currentBuf) - s.bufOffset
	toCopy := len(p)
	if available < toCopy {
		toCopy = available
	}

	copy(p, s.currentBuf[s.bufOffset:s.bufOffset+toCopy])
	s.bufOffset += toCopy
	s.currentOffset += int64(toCopy)

	if s.bufOffset >= len(s.currentBuf) {
		s.currentBuf = nil
	}
	s.mu.Unlock()

	return toCopy, nil
}

func (s *VirtualStream) Seek(offset int64, whence int) (int64, error) {
	s.mu.Lock()
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = s.currentOffset + offset
	case io.SeekEnd:
		target = s.totalSize + offset
	}

	if target < 0 || target > s.totalSize {
		s.mu.Unlock()
		return 0, errors.New("seek out of bounds")
	}

	// Optimization: If seeking to current position, do nothing
	if target == s.currentOffset {
		s.mu.Unlock()
		return target, nil
	}

	currentOffset := s.currentOffset
	bufferedEnd := s.bufferedEnd
	s.mu.Unlock()

	// Optimization: If seeking forward within buffered range, just update offset
	// This avoids restarting the worker and discarding buffered data
	if target > currentOffset && bufferedEnd > 0 && target <= bufferedEnd {
		s.mu.Lock()
		skipBytes := target - s.currentOffset
		if skipBytes <= int64(len(s.currentBuf)-s.bufOffset) {
			// Can skip within current buffer
			s.bufOffset += int(skipBytes)
			s.currentOffset = target
			if s.bufOffset >= len(s.currentBuf) {
				s.currentBuf = nil
				s.bufOffset = 0
			}
			s.mu.Unlock()
			logger.Debug("VirtualStream: fast forward seek within buffer", "target", target, "skipped", skipBytes)
			return target, nil
		}
		// Need to skip more than current buffer, but still within buffered range
		// Drain current buffer and continue reading
		s.currentBuf = nil
		s.bufOffset = 0
		s.currentOffset = target
		s.mu.Unlock()
		logger.Debug("VirtualStream: forward seek within buffered range", "target", target)
		return target, nil
	}

	// Full seek - reset buffer and notify worker
	s.mu.Lock()
	s.currentBuf = nil
	s.bufOffset = 0
	s.bufferedStart = -1
	s.bufferedEnd = -1
	s.mu.Unlock()

	select {
	case s.seekChan <- target:
	case <-s.closeChan:
		return 0, io.ErrClosedPipe
	}

	// Drain buffered data to ensure clean state
Loop:
	for {
		select {
		case <-s.dataChan:
		case <-s.errChan:
		default:
			break Loop
		}
	}

	s.mu.Lock()
	s.currentOffset = target
	s.mu.Unlock()

	logger.Debug("VirtualStream: seek complete", "target", target, "whence", whence)
	return target, nil
}

func (s *VirtualStream) Close() error {
	s.workerOnce.Do(func() {
		close(s.closeChan)
	})
	return nil
}
