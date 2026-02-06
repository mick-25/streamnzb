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
	
	VolFile      UnpackableFile
	VolOffset    int64 
}

type VirtualStream struct {
	parts       []virtualPart
	totalSize   int64
	
	currentOffset int64
	
	dataChan   chan []byte
	errChan    chan error
	closeChan  chan struct{}
	seekChan   chan int64
	
	currentBuf []byte
	bufOffset  int
	
	currentReader io.ReadCloser
	currentPartIdx int
	
	workerOnce sync.Once
}

func NewVirtualStream(parts []virtualPart, totalSize int64, startOffset int64) *VirtualStream {
	vs := &VirtualStream{
		parts:     parts,
		totalSize: totalSize,
		dataChan:  make(chan []byte, 50),
		errChan:   make(chan error, 1),
		closeChan: make(chan struct{}),
		seekChan:  make(chan int64),
		currentPartIdx: -1,
	}
	go vs.worker(startOffset)
	return vs
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
				case <-s.closeChan: return
				case off := <-s.seekChan: currentOffset = off
				}
			case <-s.closeChan: return
			case off := <-s.seekChan: currentOffset = off
			}
			continue
		}
		
		var activePart *virtualPart
		var partIdx int
		for i := range s.parts {
			if currentOffset >= s.parts[i].VirtualStart && currentOffset < s.parts[i].VirtualEnd {
				activePart = &s.parts[i]
				partIdx = i
				break
			}
		}
		
		if activePart == nil {
			logger.Error("VirtualStream: offset not mapped", "offset", currentOffset, "totalSize", s.totalSize, "parts", len(s.parts))
			select {
			case s.errChan <- fmt.Errorf("offset %d not mapped", currentOffset):
				return
			case <-s.closeChan: return
			}
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
				case <-s.closeChan: return
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
	if len(s.currentBuf) == 0 {
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
	
	return toCopy, nil
}

func (s *VirtualStream) Seek(offset int64, whence int) (int64, error) {
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
		return 0, errors.New("seek out of bounds")
	}
	
	s.currentBuf = nil
	s.bufOffset = 0
	
	select {
	case s.seekChan <- target:
	case <-s.closeChan:
		return 0, io.ErrClosedPipe
	}
	
	Loop:
	for {
		select {
		case <-s.dataChan:
		case <-s.errChan:
		default:
			break Loop
		}
	}
	
	logger.Debug("VirtualStream: seek complete", "target", target, "whence", whence)
	s.currentOffset = target
	return target, nil
}

func (s *VirtualStream) Close() error {
	s.workerOnce.Do(func() {
		close(s.closeChan)
	})
	return nil
}
