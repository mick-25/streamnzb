package loader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"streamnzb/pkg/decode"
	"streamnzb/pkg/logger"
	"streamnzb/pkg/nntp"
	"streamnzb/pkg/nzb"
)

// MaxZeroFills is the maximum number of segments we zero-fill before returning
// an error. Beyond this, playback would be too corrupted to be useful.
const MaxZeroFills = 10

// ErrTooManyZeroFills is returned when segment downloads fail on all providers
// more than MaxZeroFills times. Callers can use errors.Is to detect and redirect.
var ErrTooManyZeroFills = errors.New("too many failed segments")

type Segment struct {
	nzb.Segment
	StartOffset int64
	EndOffset   int64
}

// inflight tracks a segment download in progress so concurrent callers
// wait for the same result instead of issuing duplicate NNTP requests.
type inflight struct {
	done chan struct{}
	data []byte
	err  error
}

type File struct {
	nzbFile   *nzb.File
	pools     []*nntp.ClientPool
	estimator *SegmentSizeEstimator
	segments  []*Segment
	totalSize int64
	detected  bool
	ctx       context.Context
	mu        sync.Mutex

	segCache   map[int][]byte
	segCacheMu sync.RWMutex

	inflightMu sync.Mutex
	inflightDL map[int]*inflight

	zeroFillMu   sync.Mutex
	zeroFillCount int
}

func NewFile(ctx context.Context, f *nzb.File, pools []*nntp.ClientPool, estimator *SegmentSizeEstimator) *File {
	segments := make([]*Segment, len(f.Segments))
	var offset int64
	for i, s := range f.Segments {
		segments[i] = &Segment{
			Segment:     s,
			StartOffset: offset,
			EndOffset:   offset + s.Bytes,
		}
		offset += s.Bytes
	}
	return &File{
		nzbFile:    f,
		pools:      pools,
		estimator:  estimator,
		segments:   segments,
		totalSize:  offset,
		ctx:        ctx,
		segCache:   make(map[int][]byte),
		inflightDL: make(map[int]*inflight),
	}
}

func (f *File) Name() string { return f.nzbFile.Subject }

func (f *File) Size() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.totalSize
}

func (f *File) SegmentCount() int { return len(f.segments) }

func (f *File) TotalConnections() int {
	total := 0
	for _, p := range f.pools {
		total += p.MaxConn()
	}
	return total
}

// --- Segment size detection ---

func (f *File) EnsureSegmentMap() error {
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

	firstEncoded := f.segments[0].Bytes
	if f.estimator != nil {
		if decoded, ok := f.estimator.Get(firstEncoded); ok {
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

	data, err := f.DownloadSegment(f.ctx, 0)
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

// --- Segment lookup (binary search) ---

func (f *File) FindSegmentIndex(offset int64) int {
	idx := sort.Search(len(f.segments), func(i int) bool {
		return f.segments[i].EndOffset > offset
	})
	if idx < len(f.segments) && offset >= f.segments[idx].StartOffset {
		return idx
	}
	return -1
}

// --- Shared segment cache ---

func (f *File) GetCachedSegment(index int) ([]byte, bool) {
	f.segCacheMu.RLock()
	data, ok := f.segCache[index]
	f.segCacheMu.RUnlock()
	return data, ok
}

func (f *File) PutCachedSegment(index int, data []byte) {
	f.segCacheMu.Lock()
	f.segCache[index] = data
	f.segCacheMu.Unlock()
}

func (f *File) EvictCachedSegmentsBefore(minIndex int) {
	f.segCacheMu.Lock()
	for idx := range f.segCache {
		if idx < minIndex {
			delete(f.segCache, idx)
		}
	}
	f.segCacheMu.Unlock()
}

// PrewarmSegment downloads a segment by index in the background.
// Used to pre-cache data that video players predictably request
// (e.g. end-of-file for MKV Cues).
func (f *File) PrewarmSegment(index int) {
	if index < 0 || index >= len(f.segments) {
		return
	}
	if _, ok := f.GetCachedSegment(index); ok {
		return
	}
	go f.DownloadSegment(f.ctx, index)
}

// --- Segment download ---

// HasInFlightDownload checks if a segment download is already in progress.
// Returns the done channel if in-flight, nil otherwise.
func (f *File) HasInFlightDownload(index int) <-chan struct{} {
	f.inflightMu.Lock()
	defer f.inflightMu.Unlock()
	if fl, ok := f.inflightDL[index]; ok {
		return fl.done
	}
	return nil
}

// StartDownloadSegment starts a segment download asynchronously and returns immediately.
// The download is registered in inflightDL synchronously before returning.
// Returns the done channel that will be closed when the download completes.
func (f *File) StartDownloadSegment(ctx context.Context, index int) <-chan struct{} {
	logger.Debug("File.StartDownloadSegment: start", "segIdx", index)
	logger.Trace("File.StartDownloadSegment: start", "segIdx", index)
	
	// Fast path: already cached
	if _, ok := f.GetCachedSegment(index); ok {
		logger.Debug("File.StartDownloadSegment: already cached", "segIdx", index)
		logger.Trace("File.StartDownloadSegment: already cached", "segIdx", index)
		done := make(chan struct{})
		close(done)
		return done
	}

	// Check if already in-flight
	f.inflightMu.Lock()
	if fl, ok := f.inflightDL[index]; ok {
		f.inflightMu.Unlock()
		logger.Trace("File.StartDownloadSegment: already in-flight", "segIdx", index)
		return fl.done
	}
	// Register new download synchronously
	fl := &inflight{done: make(chan struct{})}
	f.inflightDL[index] = fl
	f.inflightMu.Unlock()

	logger.Debug("File.StartDownloadSegment: registered, starting async download", "segIdx", index)
	logger.Trace("File.StartDownloadSegment: registered, starting async download", "segIdx", index)

	// Start download asynchronously
	go func() {
		logger.Debug("File.StartDownloadSegment: async download started", "segIdx", index)
		logger.Trace("File.StartDownloadSegment: async download started", "segIdx", index)
		data, err := f.doDownloadSegment(f.ctx, index)
		fl.data = data
		fl.err = err
		close(fl.done)

		f.inflightMu.Lock()
		delete(f.inflightDL, index)
		f.inflightMu.Unlock()
		
		if err != nil {
			logger.Debug("File.StartDownloadSegment: async download completed with error", "segIdx", index, "err", err)
			logger.Trace("File.StartDownloadSegment: async download completed with error", "segIdx", index, "err", err)
		} else {
			logger.Debug("File.StartDownloadSegment: async download completed", "segIdx", index, "size", len(data))
			logger.Trace("File.StartDownloadSegment: async download completed", "segIdx", index, "size", len(data))
		}
	}()

	return fl.done
}

func (f *File) DownloadSegment(ctx context.Context, index int) ([]byte, error) {
	logger.Trace("File.DownloadSegment: start", "segIdx", index)
	
	if data, ok := f.GetCachedSegment(index); ok {
		logger.Trace("File.DownloadSegment: cache hit", "segIdx", index)
		return data, nil
	}

	logger.Trace("File.DownloadSegment: cache miss", "segIdx", index)

	// Deduplicate concurrent downloads for the same segment.
	f.inflightMu.Lock()
	if fl, ok := f.inflightDL[index]; ok {
		f.inflightMu.Unlock()
		logger.Trace("File.DownloadSegment: found in-flight download, waiting", "segIdx", index)
		select {
		case <-fl.done:
			logger.Trace("File.DownloadSegment: in-flight download completed", "segIdx", index, "err", fl.err)
			return fl.data, fl.err
		case <-ctx.Done():
			logger.Trace("File.DownloadSegment: context cancelled while waiting", "segIdx", index)
			return nil, ctx.Err()
		}
	}
	fl := &inflight{done: make(chan struct{})}
	f.inflightDL[index] = fl
	f.inflightMu.Unlock()

	logger.Trace("File.DownloadSegment: starting new download", "segIdx", index)

	// Use the file's long-lived context for the actual NNTP download so it
	// survives HTTP client disconnects. The caller's ctx only gates the wait
	// above -- if they cancel, the download still finishes and gets cached
	// for the next request.
	data, err := f.doDownloadSegment(f.ctx, index)
	fl.data = data
	fl.err = err
	close(fl.done)

	f.inflightMu.Lock()
	delete(f.inflightDL, index)
	f.inflightMu.Unlock()

	if err != nil {
		logger.Trace("File.DownloadSegment: download failed", "segIdx", index, "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	} else {
		logger.Trace("File.DownloadSegment: download completed", "segIdx", index, "size", len(data))
	}

	return data, err
}

func (f *File) doDownloadSegment(ctx context.Context, index int) ([]byte, error) {
	downloadCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	seg := f.segments[index]
	tried := make([]bool, len(f.pools))
	var lastErr error

	for attempt := 0; attempt < len(f.pools); attempt++ {
		select {
		case <-downloadCtx.Done():
			return nil, downloadCtx.Err()
		default:
		}

		var client *nntp.Client
		var pool *nntp.ClientPool
		var poolIdx int = -1

		for i, p := range f.pools {
			if !tried[i] {
				if c, ok := p.TryGet(downloadCtx); ok {
					client = c
					pool = p
					poolIdx = i
					break
				}
			}
		}

		if client == nil {
			for i, p := range f.pools {
				if !tried[i] {
					var err error
					client, err = p.Get(downloadCtx)
					if err != nil {
						tried[i] = true
						lastErr = err
						if errors.Is(err, context.Canceled) {
							return nil, err
						}
						continue
					}
					pool = p
					poolIdx = i
					break
				}
			}
		}

		if client == nil {
			break
		}

		if len(f.nzbFile.Groups) > 0 {
			client.Group(f.nzbFile.Groups[0])
		}

		r, err := client.Body(seg.ID)
		if err != nil {
			pool.Put(client)
			tried[poolIdx] = true
			lastErr = err
			continue
		}

		type decodeResult struct {
			frame *decode.Frame
			err   error
		}
		done := make(chan decodeResult, 1)
		go func() {
			frame, err := decode.DecodeToBytes(r)
			done <- decodeResult{frame, err}
		}()

		select {
		case <-downloadCtx.Done():
			pool.Discard(client)
			return nil, downloadCtx.Err()
		case res := <-done:
			if res.err != nil {
				pool.Put(client)
				tried[poolIdx] = true
				lastErr = res.err
				continue
			}
			pool.Put(client)
			f.PutCachedSegment(index, res.frame.Data)
			return res.frame.Data, nil
		}
	}

	f.zeroFillMu.Lock()
	count := f.zeroFillCount
	if count >= MaxZeroFills {
		f.zeroFillMu.Unlock()
		return nil, fmt.Errorf("too many failed segments (%d/%d): %w", count+1, MaxZeroFills, errors.Join(ErrTooManyZeroFills, lastErr))
	}
	f.zeroFillCount++
	f.zeroFillMu.Unlock()

	logger.Debug("Segment failed on all providers, zero-filling", "index", index, "count", count+1, "max", MaxZeroFills, "err", lastErr)
	size := int(seg.EndOffset - seg.StartOffset)
	if size < 0 {
		size = 0
	}
	zeroData := make([]byte, size)
	f.PutCachedSegment(index, zeroData)
	return zeroData, nil
}

// --- Random access (for archive header scanning) ---

func (f *File) ReadAt(p []byte, off int64) (n int, err error) {
	if err := f.EnsureSegmentMap(); err != nil {
		return 0, err
	}
	if off >= f.totalSize {
		return 0, io.EOF
	}

	startIdx := f.FindSegmentIndex(off)
	if startIdx == -1 {
		return 0, io.EOF
	}

	currentOffset := off
	totalRead := 0
	for i := startIdx; i < len(f.segments) && totalRead < len(p); i++ {
		seg := f.segments[i]
		segOff := currentOffset - seg.StartOffset

		data, err := f.DownloadSegment(f.ctx, i)
		if err != nil {
			return totalRead, err
		}
		if segOff >= int64(len(data)) {
			continue
		}

		copied := copy(p[totalRead:], data[segOff:])
		totalRead += copied
		currentOffset += int64(copied)
	}

	if totalRead < len(p) && currentOffset >= f.totalSize {
		return totalRead, io.EOF
	}
	return totalRead, nil
}

// --- Stream creation ---

func (f *File) OpenStream() (io.ReadSeekCloser, error) {
	return f.OpenStreamCtx(f.ctx)
}

func (f *File) OpenStreamCtx(ctx context.Context) (io.ReadSeekCloser, error) {
	if err := f.EnsureSegmentMap(); err != nil {
		return nil, err
	}
	return NewSegmentReader(ctx, f, 0), nil
}

func (f *File) OpenReaderAt(ctx context.Context, offset int64) (io.ReadCloser, error) {
	if err := f.EnsureSegmentMap(); err != nil {
		return nil, err
	}
	return NewSegmentReader(ctx, f, offset), nil
}

// --- Segment size estimator ---

type SegmentSizeEstimator struct {
	entries []sizeEntry
	mu      sync.RWMutex
}

type sizeEntry struct {
	encoded int64
	decoded int64
}

func NewSegmentSizeEstimator() *SegmentSizeEstimator {
	return &SegmentSizeEstimator{entries: make([]sizeEntry, 0, 4)}
}

func (e *SegmentSizeEstimator) Get(encodedSize int64) (int64, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, entry := range e.entries {
		diff := entry.encoded - encodedSize
		if diff < 0 {
			diff = -diff
		}
		if diff < 4096 {
			return entry.decoded, true
		}
	}
	return 0, false
}

func (e *SegmentSizeEstimator) Set(encodedSize, decodedSize int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, entry := range e.entries {
		diff := entry.encoded - encodedSize
		if diff < 0 {
			diff = -diff
		}
		if diff < 4096 {
			return
		}
	}
	e.entries = append(e.entries, sizeEntry{encoded: encodedSize, decoded: decodedSize})
}
