package loader

import (
	"context"
	"errors"
	"io"
	"sync"

	"streamnzb/pkg/decode"
	"streamnzb/pkg/logger"
	"streamnzb/pkg/nntp"
	"streamnzb/pkg/nzb"
)

type File struct {
	nzbFile   *nzb.File
	pools     []*nntp.ClientPool
	estimator *SegmentSizeEstimator
	segments  []*Segment
	totalSize int64
	detected  bool
	mu        sync.Mutex

	// Single-segment cache to optimized scattered reads (e.g. header parsing)
	lastSegIndex int
	lastSegData  []byte
}

type Segment struct {
	nzb.Segment
	StartOffset int64
	EndOffset   int64
}

func NewFile(f *nzb.File, pools []*nntp.ClientPool, estimator *SegmentSizeEstimator) *File {
	segments := make([]*Segment, len(f.Segments))
	var offset int64
	// Initial estimation using NZB bytes
	for i, s := range f.Segments {
		segments[i] = &Segment{
			Segment:     s,
			StartOffset: offset,
			EndOffset:   offset + s.Bytes,
		}
		offset += s.Bytes
	}

	fl := &File{
		nzbFile:      f,
		pools:        pools,
		estimator:    estimator,
		segments:     segments,
		totalSize:    offset,
		lastSegIndex: -1,
	}

	return fl
}

func (f *File) ensureSegmentMap() error {
	f.mu.Lock()
	if f.detected {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()

	return f.detectSegmentSize()
}

func (f *File) detectSegmentSize() error {
	f.mu.Lock()
	if f.detected {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()

	// Check estimator first
	firstSegEncoded := f.segments[0].Bytes
	if f.estimator != nil {
		if decoded, ok := f.estimator.Get(firstSegEncoded); ok {
			f.mu.Lock()
			if f.detected {
				f.mu.Unlock()
				return nil
			}
			logger.Debug("Using estimated segment size", "name", f.Name(), "size", decoded)
			f.applySegmentSize(decoded)
			f.mu.Unlock()
			return nil
		}
	}

	// Download first segment (force download, bypass simple cache)
	data, err := f.DownloadSegment(context.Background(), 0)
	if err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.detected {
		return nil
	}

	if len(data) == 0 {
		return errors.New("empty first segment")
	}

	segSize := int64(len(data))
	logger.Debug("Detected segment size", "name", f.Name(), "size", segSize, "nzb_size", f.segments[0].Bytes)

	if f.estimator != nil {
		f.estimator.Set(f.segments[0].Bytes, segSize)
	}

	f.applySegmentSize(segSize)
	return nil
}

func (f *File) applySegmentSize(segSize int64) {
	var offset int64
	for i := range f.segments {
		f.segments[i].StartOffset = offset

		if i < len(f.segments)-1 {
			f.segments[i].EndOffset = offset + segSize
			offset += segSize
		} else {
			ratio := float64(segSize) / float64(f.segments[0].Bytes)
			estSize := int64(float64(f.segments[i].Bytes) * ratio)
			f.segments[i].EndOffset = offset + estSize
			offset += estSize
		}
	}

	f.totalSize = offset
	f.detected = true
	logger.Debug("Recalculated total decoded size", "size", f.totalSize)
}

func (f *File) Size() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.totalSize
}

func (f *File) ReadAt(p []byte, off int64) (n int, err error) {
	if err := f.ensureSegmentMap(); err != nil {
		return 0, err
	}
	if off >= f.totalSize {
		return 0, io.EOF
	}

	startSegIdx := -1
	// Simple scan (TODO: Binary search)
	for i, s := range f.segments {
		if off >= s.StartOffset && off < s.EndOffset {
			startSegIdx = i
			break
		}
	}

	if startSegIdx == -1 {
		return 0, io.EOF
	}

	currentOffset := off
	totalRead := 0

	for i := startSegIdx; i < len(f.segments) && totalRead < len(p); i++ {
		seg := f.segments[i]

		segInternalOffset := currentOffset - seg.StartOffset

		// Use smart getter
		data, err := f.getSegmentData(i)
		if err != nil {
			logger.Error("Error fetching segment", "index", i, "err", err)
			return totalRead, err
		}

		if segInternalOffset >= int64(len(data)) {
			// Should not happen ideally
			continue
		}

		copied := copy(p[totalRead:], data[segInternalOffset:])
		totalRead += copied
		currentOffset += int64(copied)
	}

	if totalRead < len(p) && currentOffset >= f.totalSize {
		return totalRead, io.EOF
	}

	return totalRead, nil
}

func (f *File) getSegmentData(index int) ([]byte, error) {
	f.mu.Lock()
	if f.lastSegIndex == index && f.lastSegData != nil {
		data := f.lastSegData
		f.mu.Unlock()
		return data, nil
	}
	f.mu.Unlock()

	// Not in cache, fetch it
	// We release lock during network IO to allow concurrency on other methods

	// TODO: Propagate context from ReadAt if available? Default to Background for now.
	data, err := f.DownloadSegment(context.Background(), index)
	if err != nil {
		return nil, err
	}

	// Update Cache
	f.mu.Lock()
	f.lastSegIndex = index
	f.lastSegData = data
	f.mu.Unlock()

	return data, nil
}

// TotalConnections returns the sum of all provider connection limits
func (f *File) TotalConnections() int {
	total := 0
	for _, p := range f.pools {
		total += p.MaxConn()
	}
	return total
}

// DownloadSegment performs the actual NNTP download (Exported for SmartStream)
func (f *File) DownloadSegment(ctx context.Context, index int) ([]byte, error) {
	// Optimization: Check single-segment cache first
	f.mu.Lock()
	if f.lastSegIndex == index && f.lastSegData != nil {
		data := f.lastSegData
		f.mu.Unlock()
		return data, nil
	}
	f.mu.Unlock()

	seg := f.segments[index]
	tried := make([]bool, len(f.pools))
	var lastErr error

	// Retry loop
	for attempt := 0; attempt < len(f.pools); attempt++ {
		// Check context before doing work
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		var client *nntp.Client
		var pool *nntp.ClientPool
		var poolIdx int = -1

		// 1. Try to grab a FREE connection from any untried pool (Priority Order)
		// This enables "Spillover" (Speed) - if Priority 0 is busy, we grab Priority 1 immediately.
		for i, p := range f.pools {
			if !tried[i] {
				if c, ok := p.TryGet(); ok {
					client = c
					pool = p
					poolIdx = i
					break
				}
			}
		}

		// 2. If no free connections, we MUST wait. Block on the highest priority untried pool.
		if client == nil {
			for i, p := range f.pools {
				if !tried[i] {
					var err error
					client, err = p.Get() // Blocking wait
					if err != nil {
						// This pool is broken (closed?)
						tried[i] = true
						lastErr = err
						continue
					}
					pool = p
					poolIdx = i
					break
				}
			}
		}

		// If we still have no client, it means we exhausted all pools (or they are all broken)
		if client == nil {
			break
		}

		// Execute Download
		if len(f.nzbFile.Groups) > 0 {
			client.Group(f.nzbFile.Groups[0])
		}

		r, err := client.Body(seg.ID)
		if err != nil {
			// Download failed
			pool.Put(client)
			tried[poolIdx] = true
			lastErr = err
			// Loop to try next pool
			continue
		}

		frame, err := decode.DecodeToBytes(r)
		if err != nil {
			// Decoding failed (corruption?)
			pool.Put(client)
			tried[poolIdx] = true
			lastErr = err
			continue
		}

		// Success!
		pool.Put(client)
		return frame.Data, nil
	}

	// Error handling: If all providers fail, ZERO-FILL to keep stream alive
	logger.Debug("Segment failed on all providers, zero-filling", "index", index, "err", lastErr)

	// Maintain stream alignment by returning exactly what matches the current offsets
	size := int(seg.EndOffset - seg.StartOffset)
	if size < 0 {
		size = 0
	} // Safety check

	return make([]byte, size), nil
}

func (f *File) Name() string {
	// Simple extraction: look for "filename.ext" in subject?
	// Often it's in quotes or just standard part of subject.
	// For prototype, let's assume standard format or just return Subject.
	// Better: extract the string ending in .rar or .rXX or .mkv

	// Quick hack:
	return f.nzbFile.Subject
}

// OpenStream creates a new BufferedStream starting at offset 0 (or Seek later).
func (f *File) OpenStream() (io.ReadSeekCloser, error) {
	// Ensure we have correct size map
	if err := f.ensureSegmentMap(); err != nil {
		logger.Error("Error ensuring segment detection", "err", err)
		// Proceed anyway? BufferedStream might fail.
		return nil, err
	}
	return NewBufferedStream(f), nil
}

// OpenSmartStream creates a high-performance linear reader
func (f *File) OpenSmartStream(offset int64) io.ReadCloser {
	if err := f.ensureSegmentMap(); err != nil {
		logger.Error("Error ensuring segment detection", "err", err)
	}
	return NewSmartStream(f, offset)
}

// OpenReaderAt implements UnpackableFile.OpenReaderAt
func (f *File) OpenReaderAt(offset int64) (io.ReadCloser, error) {
	// Wrapper typically returns errors, but OpenSmartStream handles it internally/async.
	// We check detection first.
	if err := f.ensureSegmentMap(); err != nil {
		return nil, err
	}
	return NewSmartStream(f, offset), nil
}

// FindSegmentIndex logic exposed for BufferedStream
func (f *File) FindSegmentIndex(offset int64) int {
	// TODO: Binary search
	for i, s := range f.segments {
		if offset >= s.StartOffset && offset < s.EndOffset {
			return i
		}
	}
	return -1
}

// SegmentSizeEstimator shares detected sizes across files to avoid redundant probing.
type SegmentSizeEstimator struct {
	// List of known sizes for fuzzy matching
	entries []sizeEntry
	mu      sync.RWMutex
}

type sizeEntry struct {
	encoded int64
	decoded int64
}

func NewSegmentSizeEstimator() *SegmentSizeEstimator {
	return &SegmentSizeEstimator{
		entries: make([]sizeEntry, 0, 4),
	}
}

func (e *SegmentSizeEstimator) Get(encodedSize int64) (int64, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, entry := range e.entries {
		diff := entry.encoded - encodedSize
		if diff < 0 {
			diff = -diff
		}

		// If encoded size is within 4KB (arbitrary tolerance for yEnc overhead variation)
		// We assume it maps to the same decoded size.
		// Standard segment sizes are usually far apart (384KB, 512KB, 768KB, etc.)
		if diff < 4096 {
			return entry.decoded, true
		}
	}
	return 0, false
}

func (e *SegmentSizeEstimator) Set(encodedSize, decodedSize int64) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Check if already covered
	for _, entry := range e.entries {
		diff := entry.encoded - encodedSize
		if diff < 0 {
			diff = -diff
		}
		if diff < 4096 {
			// Already have a close-enough entry
			return
		}
	}

	e.entries = append(e.entries, sizeEntry{
		encoded: encodedSize,
		decoded: decodedSize,
	})
}
