package loader

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/logger"
)

// SegmentReader provides linear reading with read-ahead prefetching.
// It uses the File's shared segment cache so concurrent readers on the
// same volume benefit from each other's downloads.
type SegmentReader struct {
	file   *File
	ctx    context.Context
	cancel context.CancelFunc
	parent context.Context // Store parent to recreate context on seek

	mu         sync.Mutex
	segIdx     int
	segOff     int64 // byte offset within current segment
	offset     int64 // virtual offset in file
	closed     bool

	// Prefetch
	prefetchWg sync.WaitGroup
	prefetching map[int]bool
}

func NewSegmentReader(parent context.Context, f *File, startOffset int64) *SegmentReader {
	ctx, cancel := context.WithCancel(parent)
	sr := &SegmentReader{
		file:        f,
		ctx:         ctx,
		cancel:      cancel,
		parent:      parent,
		offset:      startOffset,
		prefetching: make(map[int]bool),
	}

	idx := f.FindSegmentIndex(startOffset)
	if idx == -1 {
		if startOffset >= f.Size() {
			sr.segIdx = len(f.segments)
		} else {
			sr.segIdx = 0
		}
	} else {
		sr.segIdx = idx
		sr.segOff = startOffset - f.segments[idx].StartOffset
	}

	logger.Debug("NewSegmentReader: created", "startOffset", startOffset, "segIdx", sr.segIdx, "segOff", sr.segOff, "hasInFlight", f.HasInFlightDownload(sr.segIdx) != nil)
	
	// Start prefetching immediately for sequential reads after seek.
	// This ensures segments are downloading while the first Read() processes.
	// Prefetch uses StartDownloadSegment to register synchronously before returning.
	maxWorkers := f.TotalConnections()
	if maxWorkers > 15 {
		maxWorkers = 15
	}
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	ahead := maxWorkers
	
	for i := 0; i < ahead; i++ { // Include current segment so it's in-flight before first Read()
		segIdx := sr.segIdx + i
		if segIdx >= len(f.segments) {
			break
		}
		if _, ok := f.GetCachedSegment(segIdx); ok {
			continue
		}
		if f.HasInFlightDownload(segIdx) != nil {
			continue // Already prefetching
		}
		// Use StartDownloadSegment to register synchronously
		_ = f.StartDownloadSegment(sr.ctx, segIdx)
	}
	
	return sr
}

func (r *SegmentReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	if r.segIdx >= len(r.file.segments) {
		r.mu.Unlock()
		return 0, io.EOF
	}
	segIdx := r.segIdx
	segOff := r.segOff
	r.mu.Unlock()

	// Download the segment the caller actually needs FIRST, before
	// spawning any prefetch goroutines. This guarantees the sync read
	// gets an NNTP connection without competing against prefetch.
	data, err := r.waitForSegment(segIdx)
	if err != nil {
		return 0, err
	}

	if segOff >= int64(len(data)) {
		r.mu.Lock()
		r.segIdx++
		r.segOff = 0
		r.mu.Unlock()
		if r.segIdx >= len(r.file.segments) {
			return 0, io.EOF
		}
		return r.Read(p)
	}

	n := copy(p, data[segOff:])

	r.mu.Lock()
	r.segOff += int64(n)
	r.offset += int64(n)
	if r.segOff >= int64(len(data)) {
		r.segIdx++
		r.segOff = 0
		r.file.EvictCachedSegmentsBefore(r.segIdx - 2)
	}
	r.mu.Unlock()

	// Start prefetch AFTER the sync read succeeds. This way prefetch
	// goroutines only use connections that the active read doesn't need.
	r.startPrefetch()

	return n, nil
}

func (r *SegmentReader) waitForSegment(index int) ([]byte, error) {
	logger.Trace("SegmentReader.waitForSegment: start", "segIdx", index)
	
	// Fast path: already in shared cache
	if data, ok := r.file.GetCachedSegment(index); ok {
		logger.Trace("SegmentReader.waitForSegment: cache hit", "segIdx", index)
		return data, nil
	}

	logger.Trace("SegmentReader.waitForSegment: cache miss", "segIdx", index)

	// Get or start the download. Use StartDownloadSegment so we always register
	// before waiting - this avoids races and ensures consistent channel-based waiting.
	// HasInFlightDownload returns existing channel; StartDownloadSegment registers
	// and returns a channel (or immediate closed channel if cached).
	done := r.file.HasInFlightDownload(index)
	if done == nil {
		done = r.file.StartDownloadSegment(r.ctx, index)
		logger.Debug("SegmentReader.waitForSegment: started download, waiting", "segIdx", index)
	} else {
		logger.Debug("SegmentReader.waitForSegment: found in-flight download, waiting", "segIdx", index)
	}

	select {
	case <-done:
		logger.Debug("SegmentReader.waitForSegment: download completed", "segIdx", index)
		if data, ok := r.file.GetCachedSegment(index); ok {
			logger.Debug("SegmentReader.waitForSegment: got from cache after wait", "segIdx", index, "size", len(data))
			return data, nil
		}
		// Download may have failed - fall through to synchronous retry
		logger.Debug("SegmentReader.waitForSegment: not in cache after wait, retrying sync", "segIdx", index)
	case <-r.ctx.Done():
		logger.Debug("SegmentReader.waitForSegment: context cancelled while waiting", "segIdx", index)
		if data, ok := r.file.GetCachedSegment(index); ok {
			logger.Debug("SegmentReader.waitForSegment: got from cache after context cancellation", "segIdx", index, "size", len(data))
			return data, nil
		}
		return nil, r.ctx.Err()
	}

	// Fallback: sync download (e.g. previous async failed)
	data, err := r.file.DownloadSegment(r.ctx, index)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (r *SegmentReader) startPrefetch() {
	r.mu.Lock()
	current := r.segIdx
	r.mu.Unlock()

	maxWorkers := r.file.TotalConnections()
	if maxWorkers > 15 {
		maxWorkers = 15
	}
	if maxWorkers < 1 {
		maxWorkers = 1
	}

	// Prefetch only as many segments as we have connections.
	// Going beyond this just queues goroutines that block on pool.Get,
	// adding contention without any throughput benefit.
	ahead := maxWorkers

	r.mu.Lock()
	for i := 0; i < ahead; i++ {
		idx := current + i
		if idx >= len(r.file.segments) {
			break
		}
		if _, ok := r.file.GetCachedSegment(idx); ok {
			continue
		}
		if r.prefetching[idx] {
			continue
		}
		r.prefetching[idx] = true
		r.prefetchWg.Add(1)
		go func(segIdx int) {
			defer r.prefetchWg.Done()
			defer func() {
				r.mu.Lock()
				delete(r.prefetching, segIdx)
				r.mu.Unlock()
			}()
			_, err := r.file.DownloadSegment(r.ctx, segIdx)
			if err != nil && !isContextErr(err) {
				logger.Error("Prefetch failed", "seg", segIdx, "err", err)
			}
		}(idx)
	}
	r.mu.Unlock()
}

// Seek implements io.Seeker. On seek, closes the current position and
// repositions. The shared segment cache means previously-downloaded
// data is still available.
func (r *SegmentReader) Seek(offset int64, whence int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return 0, io.ErrClosedPipe
	}

	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = r.offset + offset
	case io.SeekEnd:
		target = r.file.Size() + offset
	default:
		return 0, errors.New("invalid whence")
	}

	if target < 0 || target > r.file.Size() {
		return 0, errors.New("seek out of bounds")
	}

	if target == r.offset {
		return target, nil
	}

	// Cancel any ongoing prefetch operations since we're seeking to a new position
	// The context cancellation will stop them gracefully
	r.cancel()
	// Create new context for the new position (using same parent)
	r.ctx, r.cancel = context.WithCancel(r.parent)
	// Clear prefetching map - old prefetch goroutines will exit via context cancellation
	r.prefetching = make(map[int]bool)
	
	r.offset = target
	if target >= r.file.Size() {
		r.segIdx = len(r.file.segments)
		r.segOff = 0
	} else {
		idx := r.file.FindSegmentIndex(target)
		if idx == -1 {
			r.segIdx = len(r.file.segments)
			r.segOff = 0
		} else {
			r.segIdx = idx
			r.segOff = target - r.file.segments[idx].StartOffset
			
			// Prefetch same window as NewSegmentReader: target + next ahead-1 segments.
			// Uses StartDownloadSegment so segments are in-flight before first Read().
			maxWorkers := r.file.TotalConnections()
			if maxWorkers > 15 {
				maxWorkers = 15
			}
			if maxWorkers < 1 {
				maxWorkers = 1
			}
			for i := 0; i < maxWorkers; i++ {
				segIdx := idx + i
				if segIdx >= len(r.file.segments) {
					break
				}
				if _, ok := r.file.GetCachedSegment(segIdx); ok {
					continue
				}
				if r.file.HasInFlightDownload(segIdx) != nil {
					continue
				}
				_ = r.file.StartDownloadSegment(r.ctx, segIdx)
			}
		}
	}

	return target, nil
}

func (r *SegmentReader) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()

	r.cancel()

	done := make(chan struct{})
	go func() {
		r.prefetchWg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	return nil
}

func isContextErr(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "canceled") || strings.Contains(s, "cancelled")
}
