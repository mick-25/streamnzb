package loader

import (
	"errors"
	"io"
	"sync"
)

// BufferedStream provides a ReadSeeker leveraging background pre-fetching.
type BufferedStream struct {
	file       *File
	offset     int64
	
	// Communication with background worker
	dataChan   chan []byte
	errChan    chan error
	closeChan  chan struct{}
	seekChan   chan int64
	
	// Current buffer being read
	currentBuf []byte
	bufOffset  int
	
	workerOnce sync.Once
}

func NewBufferedStream(f *File) *BufferedStream {
	bs := &BufferedStream{
		file:      f,
		dataChan:  make(chan []byte, 5), // Buffer 5 segments ahead
		errChan:   make(chan error, 1),
		closeChan: make(chan struct{}),
		seekChan:  make(chan int64),
	}
	go bs.worker()
	return bs
}

func (s *BufferedStream) worker() {
	var currentOffset int64 = 0
	
	// Initial seek handling or start from 0
	select {
	case off := <-s.seekChan:
		currentOffset = off
	default:
	}

	for {
		// Calculate which segment index satisfies currentOffset
		segIdx := s.file.FindSegmentIndex(currentOffset)
		if segIdx == -1 {
			// EOF or Invalid
			select {
			case s.errChan <- io.EOF:
				// EOF sent, now wait for seek or close
				select {
				case <-s.closeChan:
					return
				case off := <-s.seekChan:
					currentOffset = off
					continue
				}
			case <-s.closeChan:
				return
			case off := <-s.seekChan:
				currentOffset = off
				continue
			}
		}

		// Loop through segments starting from segIdx
		for i := segIdx; i < len(s.file.segments); i++ {
			// Check for seek or close before fetching
			select {
			case <-s.closeChan:
				return
			case off := <-s.seekChan:
				currentOffset = off
				// Break inner loop to restart finding segment
				goto ResetLoop
			default:
			}

			// Determine if we need to trim the start of this segment
			// (If seek landed in the middle of a segment)
			seg := s.file.segments[i]
			
			// If our currentOffset is way past this segment (shouldn't happen if logic proper), skip
			if currentOffset >= seg.EndOffset {
				continue
			}

			data, err := s.file.getSegmentData(i)
			if err != nil {
				select {
				case s.errChan <- err:
				case <-s.closeChan:
					return
				case off := <-s.seekChan:
					currentOffset = off
					goto ResetLoop
				}
				// If we sent error, we probably should stop or retry? 
				// For now, pause/stop.
				return 
			}

			// Handle partial segment if offset is inside
			startDelta := currentOffset - seg.StartOffset
			if startDelta > 0 && startDelta < int64(len(data)) {
				data = data[startDelta:]
			}
			// If startDelta is negative, logic error, but safe to ignore
			
			// Send data
			select {
			case s.dataChan <- data:
				currentOffset += int64(len(data))
			case <-s.closeChan:
				return
			case off := <-s.seekChan:
				currentOffset = off
				goto ResetLoop
			}
		}
		
		// If we ran out of segments, send EOF
		select {
		case s.errChan <- io.EOF:
		case <-s.closeChan:
			return
		case off := <-s.seekChan:
			currentOffset = off
			goto ResetLoop
		}
		
		// Idle if EOF managed
		select {
		case <-s.closeChan:
			return
		case off := <-s.seekChan:
			currentOffset = off
			goto ResetLoop
		}

	ResetLoop:
	}
}

func (s *BufferedStream) Read(p []byte) (n int, err error) {
	if len(s.currentBuf) == 0 {
		// Fetch next block
		select {
		case buf := <-s.dataChan:
			s.currentBuf = buf
			s.bufOffset = 0
		case err := <-s.errChan:
			return 0, err
		case <-s.closeChan:
			return 0, io.ErrClosedPipe
		}
	}

	// Copy from currentBuf
	available := len(s.currentBuf) - s.bufOffset
	toCopy := len(p)
	if available < toCopy {
		toCopy = available
	}

	copy(p, s.currentBuf[s.bufOffset:s.bufOffset+toCopy])
	s.bufOffset += toCopy
	s.offset += int64(toCopy)
	
	if s.bufOffset >= len(s.currentBuf) {
		s.currentBuf = nil // Release
	}

	return toCopy, nil
}

func (s *BufferedStream) Seek(offset int64, whence int) (int64, error) {
	var newOffset int64
	switch whence {
	case io.SeekStart:
		newOffset = offset
	case io.SeekCurrent:
		newOffset = s.offset + offset
	case io.SeekEnd:
		newOffset = s.file.totalSize + offset
	default:
		return 0, errors.New("invalid whence")
	}

	if newOffset < 0 {
		return 0, errors.New("seek out of bounds")
	}

	// Reset state
	s.currentBuf = nil
	s.bufOffset = 0
	s.offset = newOffset
	
	// Send seek command to worker
	// We need to ensure we don't deadock if worker is blocked on sending dataChan
	// But worker selects on seekChan.
	// HOWEVER, if worker is blocked on dataChan <- data, it will select seekChan.
	
	// Create a new channel to drain potential old data?
	// No, the select in worker handles it.
	
	select {
	case s.seekChan <- newOffset:
	case <-s.closeChan:
		return 0, io.ErrClosedPipe
	}
	
	// Drain dataChan so we don't read old data?
	// The worker loops breaks on seek, but there might be data in channel buffer.
	// We must flush the channel.
	Loop:
	for {
		select {
		case <-s.dataChan:
			// Discard
		case <-s.errChan:
			// Discard error from previous operation
		default:
			break Loop
		}
	}
	
	// log.Printf("Seeked to %d (Drained channels)", newOffset)
	return newOffset, nil
}

func (s *BufferedStream) Tell() int64 {
	return s.offset
}

func (s *BufferedStream) Close() error {
	s.workerOnce.Do(func() {
		close(s.closeChan)
	})
	return nil
}
