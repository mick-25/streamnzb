package unpack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"streamnzb/pkg/media/loader"
	"streamnzb/pkg/core/logger"
)

// virtualPart maps a range of the virtual file to a physical volume location.
type virtualPart struct {
	VirtualStart int64
	VirtualEnd   int64
	VolFile      UnpackableFile
	VolOffset    int64
}

// VirtualStream provides io.ReadSeekCloser over a set of virtual parts.
// It is fully synchronous -- no background goroutines or channels.
// Each Read/Seek call directly operates on the underlying volume readers.
type VirtualStream struct {
	parts     []virtualPart
	totalSize int64
	ctx       context.Context

	mu            sync.Mutex
	offset        int64
	currentReader io.ReadCloser
	currentPart   int
	closed        bool
}

func NewVirtualStream(ctx context.Context, parts []virtualPart, totalSize int64, startOffset int64) *VirtualStream {
	return &VirtualStream{
		parts:       parts,
		totalSize:   totalSize,
		ctx:         ctx,
		offset:      startOffset,
		currentPart: -1,
	}
}

func (s *VirtualStream) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.readLocked(p)
}

// readLocked performs the actual read with s.mu already held.
func (s *VirtualStream) readLocked(p []byte) (int, error) {
	if s.closed {
		return 0, io.ErrClosedPipe
	}

	if s.offset >= s.totalSize {
		return 0, io.EOF
	}

	select {
	case <-s.ctx.Done():
		return 0, s.ctx.Err()
	default:
	}

	part, partIdx := s.findPart(s.offset)
	if part == nil {
		return 0, fmt.Errorf("offset %d not mapped in %d parts", s.offset, len(s.parts))
	}

	logger.Trace("VirtualStream.readLocked: before ensureReader", "offset", s.offset, "partIdx", partIdx, "bufSize", len(p))
	if err := s.ensureReader(part, partIdx); err != nil {
		logger.Trace("VirtualStream.readLocked: ensureReader failed", "err", err)
		return 0, err
	}
	logger.Trace("VirtualStream.readLocked: after ensureReader", "offset", s.offset, "partIdx", partIdx)

	remaining := part.VirtualEnd - s.offset
	buf := p
	if int64(len(buf)) > remaining {
		buf = buf[:remaining]
	}

	logger.Trace("VirtualStream.readLocked: calling reader.Read", "offset", s.offset, "bufSize", len(buf))
	n, err := s.currentReader.Read(buf)
	logger.Trace("VirtualStream.readLocked: reader.Read returned", "n", n, "err", err, "offset", s.offset)
	s.offset += int64(n)

	if err == io.EOF {
		logger.Trace("VirtualStream.readLocked: EOF, closing reader", "n", n)
		s.closeReader()
		// Advance past current part so next iteration finds the next one.
		// This prevents an infinite loop when a reader returns (0, EOF)
		// because the underlying volume data is shorter than the blueprint.
		if s.offset < part.VirtualEnd {
			s.offset = part.VirtualEnd
		}
		if n > 0 {
			return n, nil
		}
		if s.offset < s.totalSize {
			return s.readLocked(p)
		}
		return 0, io.EOF
	}

	return n, err
}

func (s *VirtualStream) Seek(offset int64, whence int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return 0, io.ErrClosedPipe
	}

	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = s.offset + offset
	case io.SeekEnd:
		target = s.totalSize + offset
	default:
		return 0, errors.New("invalid whence")
	}

	if target < 0 || target > s.totalSize {
		return 0, errors.New("seek out of bounds")
	}

	if target == s.offset {
		logger.Trace("VirtualStream.Seek: no-op", "offset", s.offset)
		return target, nil
	}

	logger.Debug("VirtualStream.Seek: start", "from", s.offset, "to", target, "whence", whence, "currentPart", s.currentPart)
	logger.Trace("VirtualStream.Seek: start", "from", s.offset, "to", target, "whence", whence, "currentPart", s.currentPart)

	// Check if we're staying in the same part - if so, reuse the reader and seek within it
	part, partIdx := s.findPart(target)
	if part != nil && s.currentReader != nil && s.currentPart == partIdx {
		// Same part - seek within the existing reader (reuse connection)
		localOff := target - part.VirtualStart
		volOff := part.VolOffset + localOff
		logger.Trace("VirtualStream.Seek: same part, reusing reader", "partIdx", partIdx, "volOff", volOff)
		
		// Prefetch target segment even when reusing reader (SegmentReader.Seek handles this too, but
		// doing it here ensures it starts immediately when http.ServeContent calls Seek)
		if volFile, ok := part.VolFile.(*loader.File); ok && volOff > 0 {
			if err := volFile.EnsureSegmentMap(); err == nil {
				if segIdx := volFile.FindSegmentIndex(volOff); segIdx >= 0 {
					if _, cached := volFile.GetCachedSegment(segIdx); !cached {
						logger.Debug("VirtualStream.Seek: prefetching segment in same part", "segIdx", segIdx, "volOff", volOff)
						logger.Trace("VirtualStream.Seek: prefetching segment in same part", "segIdx", segIdx)
						done := volFile.StartDownloadSegment(s.ctx, segIdx)
						logger.Debug("VirtualStream.Seek: prefetch registered (same part)", "segIdx", segIdx, "hasChannel", done != nil)
					}
				}
			}
		}
		
		if seeker, ok := s.currentReader.(io.Seeker); ok {
			// Calculate seek offset relative to current reader position
			currentLocalOff := s.offset - part.VirtualStart
			currentVolOff := part.VolOffset + currentLocalOff
			seekDelta := volOff - currentVolOff
			if seekDelta != 0 {
				newPos, err := seeker.Seek(seekDelta, io.SeekCurrent)
				if err == nil {
					logger.Trace("VirtualStream.Seek: seeked within reader", "delta", seekDelta, "newPos", newPos)
					s.offset = target
					return target, nil
				}
				logger.Trace("VirtualStream.Seek: seek failed, will recreate", "err", err)
				// Seek failed, fall through to close and recreate
			} else {
				// Already at the right position
				logger.Trace("VirtualStream.Seek: already at position")
				s.offset = target
				return target, nil
			}
		} else {
			logger.Trace("VirtualStream.Seek: reader not seekable, will recreate")
		}
	} else {
		if part != nil {
			logger.Trace("VirtualStream.Seek: different part", "oldPart", s.currentPart, "newPart", partIdx)
		} else {
			logger.Trace("VirtualStream.Seek: part not found for target", "target", target)
		}
	}

	// Different part or seek failed - close current reader; a new one will be opened on next Read
	logger.Trace("VirtualStream.Seek: closing reader, will recreate on next Read", "oldPart", s.currentPart)
	s.closeReader()
	s.offset = target
	
	// Prefetch the target segment immediately when seeking (before Read() is called).
	// This ensures the segment is downloading while http.ServeContent processes the Range header.
	if part != nil {
		localOff := target - part.VirtualStart
		volOff := part.VolOffset + localOff
		if volFile, ok := part.VolFile.(*loader.File); ok && volOff > 0 {
			logger.Debug("VirtualStream.Seek: prefetching target segment", "volOff", volOff, "partIdx", partIdx)
			logger.Trace("VirtualStream.Seek: prefetching target segment", "volOff", volOff, "partIdx", partIdx)
			if err := volFile.EnsureSegmentMap(); err == nil {
				if segIdx := volFile.FindSegmentIndex(volOff); segIdx >= 0 {
					if _, cached := volFile.GetCachedSegment(segIdx); !cached {
						logger.Debug("VirtualStream.Seek: starting prefetch", "segIdx", segIdx, "volOff", volOff)
						logger.Trace("VirtualStream.Seek: starting prefetch", "segIdx", segIdx)
						done := volFile.StartDownloadSegment(s.ctx, segIdx)
						logger.Debug("VirtualStream.Seek: prefetch registered", "segIdx", segIdx, "hasChannel", done != nil)
						logger.Trace("VirtualStream.Seek: prefetch registered", "segIdx", segIdx, "hasChannel", done != nil)
					} else {
						logger.Debug("VirtualStream.Seek: segment already cached", "segIdx", segIdx)
						logger.Trace("VirtualStream.Seek: segment already cached", "segIdx", segIdx)
					}
				} else {
					logger.Debug("VirtualStream.Seek: segment index not found", "volOff", volOff)
				}
			} else {
				logger.Debug("VirtualStream.Seek: EnsureSegmentMap failed", "err", err)
			}
		} else {
			logger.Debug("VirtualStream.Seek: not RAR volume or volOff=0", "isLoaderFile", ok, "volOff", volOff)
		}
	}
	
	return target, nil
}

func (s *VirtualStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true
	s.closeReader()
	return nil
}

// --- internal ---

func (s *VirtualStream) findPart(offset int64) (*virtualPart, int) {
	left, right := 0, len(s.parts)-1
	for left <= right {
		mid := (left + right) / 2
		p := &s.parts[mid]
		if offset >= p.VirtualStart && offset < p.VirtualEnd {
			return p, mid
		}
		if offset < p.VirtualStart {
			right = mid - 1
		} else {
			left = mid + 1
		}
	}
	return nil, -1
}

func (s *VirtualStream) ensureReader(part *virtualPart, partIdx int) error {
	if s.currentReader != nil && s.currentPart == partIdx {
		logger.Trace("VirtualStream.ensureReader: reader already open", "partIdx", partIdx)
		return nil
	}

	logger.Trace("VirtualStream.ensureReader: creating new reader", "partIdx", partIdx, "currentPart", s.currentPart)
	s.closeReader()

	localOff := s.offset - part.VirtualStart
	volOff := part.VolOffset + localOff
	logger.Trace("VirtualStream.ensureReader: calculated offsets", "localOff", localOff, "volOff", volOff)

	// For loader.File volumes, prefetch the target segment before opening the reader.
	if volFile, ok := part.VolFile.(*loader.File); ok && volOff > 0 {
		logger.Trace("VirtualStream.ensureReader: RAR volume detected", "volOff", volOff)
		if err := volFile.EnsureSegmentMap(); err == nil {
			if segIdx := volFile.FindSegmentIndex(volOff); segIdx >= 0 {
				cached := false
				if _, cached = volFile.GetCachedSegment(segIdx); !cached {
					logger.Trace("VirtualStream.ensureReader: starting prefetch", "segIdx", segIdx, "volOff", volOff)
					done := volFile.StartDownloadSegment(s.ctx, segIdx)
					logger.Trace("VirtualStream.ensureReader: prefetch registered", "segIdx", segIdx, "hasChannel", done != nil)
				} else {
					logger.Trace("VirtualStream.ensureReader: segment already cached", "segIdx", segIdx)
				}
			} else {
				logger.Trace("VirtualStream.ensureReader: segment index not found", "volOff", volOff)
			}
		} else {
			logger.Trace("VirtualStream.ensureReader: EnsureSegmentMap failed", "err", err)
		}
	} else {
		logger.Trace("VirtualStream.ensureReader: not RAR volume or volOff=0", "isLoaderFile", ok, "volOff", volOff)
	}

	logger.Trace("VirtualStream.ensureReader: opening reader", "partIdx", partIdx, "volOff", volOff)
	r, err := part.VolFile.OpenReaderAt(s.ctx, volOff)
	if err != nil {
		logger.Trace("VirtualStream.ensureReader: OpenReaderAt failed", "err", err, "partIdx", partIdx, "volOff", volOff)
		return fmt.Errorf("open volume part %d at offset %d: %w", partIdx, volOff, err)
	}

	s.currentReader = r
	s.currentPart = partIdx
	logger.Trace("VirtualStream.ensureReader: reader opened successfully", "partIdx", partIdx)
	return nil
}

func (s *VirtualStream) closeReader() {
	if s.currentReader != nil {
		s.currentReader.Close()
		s.currentReader = nil
		s.currentPart = -1
	}
}
