package loader

import (
	"context"
	"errors"
	"io"
	"streamnzb/pkg/logger"
	"strings"
	"sync"
	"time"
)

// SmartStream is a port of AltMount's UsenetReader.
// It provides high-performance linear streaming with read-ahead buffering.
type SmartStream struct {
	file        *File
	startOffset int64

	// State
	currentSegIdx  int
	currentReader  io.Reader // Reader for the current segment body
	currentSegBody io.ReadCloser

	// Buffering
	segmentCache    map[int][]byte
	downloadingSegs map[int]bool
	downloadCond    *sync.Cond
	mu              sync.Mutex

	// Configuration
	maxBufferBytes int64
	maxWorkers     int

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed bool

	// Progress tracking to detect paused streams
	lastReadTime time.Time
	lastSegIdx   int // Track which segment was last read to detect progress
}

func NewSmartStream(f *File, startOffset int64) *SmartStream {
	ctx, cancel := context.WithCancel(context.Background())

	s := &SmartStream{
		file:            f,
		startOffset:     startOffset,
		segmentCache:    make(map[int][]byte),
		downloadingSegs: make(map[int]bool),
		maxBufferBytes:  64 * 1024 * 1024, // 64MB buffer
		maxWorkers:      15,               // Default cap
		ctx:             ctx,
		cancel:          cancel,
		lastReadTime:    time.Now(),
	}

	// Adjust maxWorkers to not exceed available connections
	totalConns := f.TotalConnections()
	if totalConns > 0 && totalConns < s.maxWorkers {
		s.maxWorkers = totalConns
	}
	s.downloadCond = sync.NewCond(&s.mu)

	// Find starting segment
	s.currentSegIdx = f.FindSegmentIndex(startOffset)
	if s.currentSegIdx == -1 {
		// If offset is EOF, handle gracefully
		if startOffset >= f.totalSize {
			s.currentSegIdx = len(f.segments)
		} else {
			s.currentSegIdx = 0
		}
	}

	// Start background downloader immediately
	s.wg.Add(1)
	go s.downloadManager()

	// Boostrap: Trigger first fetch immediately without waiting for loop cycle
	s.downloadCond.Broadcast()

	return s
}

func (s *SmartStream) Read(p []byte) (n int, err error) {
	if s.closed {
		return 0, io.ErrClosedPipe
	}

	s.mu.Lock()
	if s.currentSegIdx >= len(s.file.segments) {
		s.mu.Unlock()
		return 0, io.EOF
	}
	s.mu.Unlock()

	// Ensure we have a current reader
	if s.currentReader == nil {
		if err := s.advanceToNextSegment(); err != nil {
			return 0, err
		}
	}

	n, err = s.currentReader.Read(p)

	// Update progress tracking on successful read
	if n > 0 {
		s.mu.Lock()
		s.lastReadTime = time.Now()
		s.lastSegIdx = s.currentSegIdx
		s.mu.Unlock()
	}

	if err == io.EOF {
		// Finished current segment, move to next
		s.closeCurrentSegment()

		s.mu.Lock()
		s.currentSegIdx++
		s.lastReadTime = time.Now()
		s.lastSegIdx = s.currentSegIdx
		s.mu.Unlock()

		// If we read partial data, return it with nil error (caller will call Read again)
		// loops back to advanceToNextSegment on next call
		if n > 0 {
			return n, nil
		}

		// Tail recursion for next segment immediately
		return s.Read(p)
	}

	return n, err
}

func (s *SmartStream) advanceToNextSegment() error {
	s.mu.Lock()
	idx := s.currentSegIdx
	if idx >= len(s.file.segments) {
		s.mu.Unlock()
		return io.EOF
	}

	// Wait for data
	for {
		if data, ok := s.segmentCache[idx]; ok {
			// Found data!
			// Update progress tracking - we're advancing to a new segment
			s.lastReadTime = time.Now()
			s.lastSegIdx = idx

			// Create reader
			// Adjust for startOffset if it's the first segment
			startPos := int64(0)
			if idx == s.file.FindSegmentIndex(s.startOffset) {
				seg := s.file.segments[idx]
				if s.startOffset > seg.StartOffset {
					startPos = s.startOffset - seg.StartOffset
				}
			}

			// Safety check bounds
			if startPos >= int64(len(data)) {
				// Should not happen unless offset > segment size?
				// Just treat as empty
				s.currentReader = &emptyReader{}
			} else {
				s.currentReader = &sliceReader{data: data, pos: startPos}
			}

			// Remove from cache? Keep until closed?
			// UsenetReader removes on EOF. We will remove when we close current segment (Next Read)
			// But for memory safety, we can rely on downloadManager cleaning up?
			// No, downloadManager fills. We consume.
			// Ideally we remove from cache NOW so downloadManager can fill more?
			// Consumed data is returned to user.
			// Let's keep it in cache until done reading

			s.mu.Unlock()
			return nil
		}

		// Check if download failed or context cancelled
		if s.ctx.Err() != nil {
			s.mu.Unlock()
			return s.ctx.Err()
		}

		// Wait on condition variable with timeout to prevent indefinite blocking
		done := make(chan struct{}, 1)
		go func() {
			s.mu.Lock()
			s.downloadCond.Wait()
			s.mu.Unlock()
			done <- struct{}{}
		}()
		s.mu.Unlock()
		select {
		case <-done:
			s.mu.Lock()
			// Re-acquire lock for next iteration of loop
		case <-time.After(2 * time.Minute):
			return context.DeadlineExceeded
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
}

func (s *SmartStream) closeCurrentSegment() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove processed segment from cache to free memory
	delete(s.segmentCache, s.currentSegIdx)
	s.currentReader = nil
	s.downloadCond.Broadcast() // Notify manager that space is available
}

func (s *SmartStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	s.cancel()
	s.downloadCond.Broadcast()
	s.wg.Wait()
	return nil
}

func (s *SmartStream) downloadManager() {
	defer s.wg.Done()

	// Simple semaphore for concurrency
	sem := make(chan struct{}, s.maxWorkers)

	// Fast-start: Immediately queue the first few segments
	// This prevents the loop delay from affecting startup
	s.mu.Lock()
	startIdx := s.currentSegIdx
	for i := 0; i < 5; i++ {
		target := startIdx + i
		if target < len(s.file.segments) {
			s.downloadingSegs[target] = true
			select {
			case sem <- struct{}{}:
				go func(idx int) {
					defer func() { <-sem }()
					data, err := s.file.DownloadSegment(s.ctx, idx)
					s.mu.Lock()
					delete(s.downloadingSegs, idx)
					if err == nil {
						s.segmentCache[idx] = data
						s.downloadCond.Broadcast()
					}
					s.mu.Unlock()
				}(target)
			default:
				s.downloadingSegs[target] = false
				delete(s.downloadingSegs, target)
			}
		}
	}
	s.mu.Unlock()

	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		s.mu.Lock()
		current := s.currentSegIdx
		// Calculate buffer usage
		var bufferUsed int64
		for _, data := range s.segmentCache {
			bufferUsed += int64(len(data))
		}

		// If buffer full, check if reader is making progress
		if bufferUsed > s.maxBufferBytes {
			// Check if reader has made progress recently (within last 30 seconds)
			// If not, the stream is likely paused - use timeout to prevent deadlock
			timeSinceLastRead := time.Since(s.lastReadTime)
			hasProgress := timeSinceLastRead < 30*time.Second && s.lastSegIdx >= current

			if !hasProgress {
				// Stream appears paused - reduce buffer by clearing old segments
				// Keep only segments near current position to free memory
				keepAhead := 5  // Keep 5 segments ahead
				keepBehind := 2 // Keep 2 segments behind (for seeking)
				clearedAny := false
				// Track segments being cleared so we can cancel their downloads
				clearedSegs := make(map[int]bool)
				for segIdx := range s.segmentCache {
					if segIdx < current-keepBehind || segIdx > current+keepAhead {
						delete(s.segmentCache, segIdx)
						clearedSegs[segIdx] = true
						clearedAny = true
					}
				}

				// Cancel any ongoing downloads for cleared segments
				// This releases connections back to the pool promptly
				for segIdx := range s.downloadingSegs {
					if clearedSegs[segIdx] {
						// Mark as not downloading - the download will be cancelled
						// when context is checked, and connection will be returned
						delete(s.downloadingSegs, segIdx)
					}
				}

				// Broadcast to wake up any waiting readers after clearing segments
				if clearedAny {
					s.downloadCond.Broadcast()
				}

				// Wait with timeout to prevent deadlock and allow periodic checks
				s.mu.Unlock()
				timer := time.NewTimer(5 * time.Second)
				select {
				case <-s.ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
					// Continue loop to re-check
				}
				continue
			}

			// Reader is active, wait normally (will be woken by closeCurrentSegment)
			s.downloadCond.Wait()
			s.mu.Unlock()
			continue
		}

		// Queue downloads
		// Look ahead 20 segments or up to buffer limit
		started := 0
		for i := 0; i < 20; i++ {
			targetIdx := current + i
			if targetIdx >= len(s.file.segments) {
				break
			}

			if _, cached := s.segmentCache[targetIdx]; cached {
				continue
			}
			if s.downloadingSegs[targetIdx] {
				continue
			}

			// Start download
			s.downloadingSegs[targetIdx] = true
			started++

			// Launch worker
			// MUST NOT BLOCK HERE
			select {
			case sem <- struct{}{}:
				go func(idx int) {
					defer func() { <-sem }()

					data, err := s.file.DownloadSegment(s.ctx, idx) // reusing method from File

					s.mu.Lock()
					delete(s.downloadingSegs, idx)
					if err == nil {
						s.segmentCache[idx] = data
						s.downloadCond.Broadcast() // Wake up reader
					} else {
						isCanceled := errors.Is(err, context.Canceled) ||
							err == context.Canceled ||
							strings.Contains(err.Error(), "canceled") ||
							strings.Contains(err.Error(), "cancelled")
						if !isCanceled {
							logger.Error("SmartStream download fail", "seg", idx, "err", err)
						}
					}
					s.mu.Unlock()
				}(targetIdx)
			default:
				// Workers full, stop queuing
				// But mark as not downloading so we try again?
				s.downloadingSegs[targetIdx] = false
				// Actually if we break here, we just retry next loop.
				// The map set to true prevents dupes.
				// Correct: set back to false if not launched
				delete(s.downloadingSegs, targetIdx)
			}

			if len(sem) == cap(sem) {
				break
			}
		}
		s.mu.Unlock()

		// Sleep briefly to prevent tight loop if idle
		time.Sleep(50 * time.Millisecond)
	}
}

// Helpers

type sliceReader struct {
	data []byte
	pos  int64
}

func (r *sliceReader) Read(p []byte) (n int, err error) {
	if r.pos >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n = copy(p, r.data[r.pos:])
	r.pos += int64(n)
	return n, nil
}

type emptyReader struct{}

func (r *emptyReader) Read(p []byte) (int, error) { return 0, io.EOF }
