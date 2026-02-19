package validation

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/media/decode"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/usenet/nntp"

	"github.com/javi11/rardecode/v2"
	"github.com/javi11/sevenzip"
)


// Checker validates article availability across providers
type Checker struct {
	mu            sync.RWMutex
	providers     map[string]*nntp.ClientPool
	providerOrder []string // Provider names in priority order (for single-provider validation)
	sampleSize    int
	maxConcurrent int
}

// NewChecker creates a new article availability checker.
// providerOrder is the list of provider names in priority order (used for cache warming).
// cacheTTL is ignored (validation cache removed to avoid stale results).
func NewChecker(providers map[string]*nntp.ClientPool, providerOrder []string, cacheTTL time.Duration, sampleSize, maxConcurrent int) *Checker {
	return &Checker{
		providers:     providers,
		providerOrder: providerOrder,
		sampleSize:    sampleSize,
		maxConcurrent: maxConcurrent,
	}
}

// ValidationResult represents the result of article validation
type ValidationResult struct {
	Provider        string
	Host            string
	Available       bool
	TotalArticles   int
	CheckedArticles int
	MissingArticles int
	Error           error
}

// GetProviderHosts returns a list of all configured provider hostnames
func (c *Checker) GetProviderHosts() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	hosts := make([]string, 0, len(c.providers))
	for host := range c.providers {
		hosts = append(hosts, host)
	}
	return hosts
}

// GetPrimaryProviderHost returns the highest-priority provider name for single-provider validation (e.g. cache warming).
func (c *Checker) GetPrimaryProviderHost() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.providerOrder) > 0 {
		return c.providerOrder[0]
	}
	for name := range c.providers {
		return name
	}
	return ""
}

// ValidateNZBSingleProvider checks article availability for a single provider only.
// Used for cache warming: check one provider, report good or bad, don't shop around.
func (c *Checker) ValidateNZBSingleProvider(ctx context.Context, nzbData *nzb.NZB, providerName string) *ValidationResult {
	c.mu.RLock()
	pool, ok := c.providers[providerName]
	c.mu.RUnlock()
	if !ok || pool == nil {
		return &ValidationResult{Provider: providerName, Error: fmt.Errorf("provider %q not found", providerName)}
	}
	return c.validateProvider(ctx, nzbData, providerName, pool)
}

// ValidateNZBSingleProviderExtended does a STAT check followed by a BODY+yEnc
// probe on the leading segments of the playback-relevant file. The BODY step
// catches corruption that STAT alone cannot detect (yEnc size mismatches,
// truncated articles, etc.). Intended for background cache warming where
// extra bandwidth is acceptable.
func (c *Checker) ValidateNZBSingleProviderExtended(ctx context.Context, nzbData *nzb.NZB, providerName string) *ValidationResult {
	c.mu.RLock()
	pool, ok := c.providers[providerName]
	c.mu.RUnlock()
	if !ok || pool == nil {
		return &ValidationResult{Provider: providerName, Error: fmt.Errorf("provider %q not found", providerName)}
	}
	return c.validateProviderExtended(ctx, nzbData, providerName, pool)
}

// ValidateNZB checks article availability for an NZB across all providers.
// Each provider runs STAT + BODY/yEnc probe in parallel.
func (c *Checker) ValidateNZB(ctx context.Context, nzbData *nzb.NZB) map[string]*ValidationResult {
	results := make(map[string]*ValidationResult)
	var mu sync.Mutex
	var wg sync.WaitGroup

	logger.Trace("ValidateNZB start", "hash", nzbData.Hash())

	// Validate across all providers in parallel
	c.mu.RLock()
	providers := c.providers
	c.mu.RUnlock()

	for providerName, pool := range providers {
		wg.Add(1)
		go func(name string, p *nntp.ClientPool) {
			defer wg.Done()

			result := c.validateProviderExtended(ctx, nzbData, name, p)

			mu.Lock()
			results[name] = result
			mu.Unlock()
		}(providerName, pool)
	}

	// Wait for all validations with timeout to prevent hanging
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logger.Trace("ValidateNZB: all providers done", "results", len(results))
	case <-ctx.Done():
		// Context cancelled, return partial results
		logger.Debug("Validation cancelled, returning partial results")
		logger.Trace("ValidateNZB: ctx.Done", "partial_results", len(results))
		return results
	case <-time.After(30 * time.Second):
		// Timeout after 30 seconds to prevent hanging
		logger.Warn("Validation timeout, returning partial results", "providers", len(providers))
		logger.Trace("ValidateNZB: 30s timeout", "partial_results", len(results))
		return results
	}

	return results
}

// InvalidateCache is a no-op (validation cache removed)
func (c *Checker) InvalidateCache(hash string) {}

func maxOr(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// validateProvider checks article availability for a single provider
func (c *Checker) validateProvider(ctx context.Context, nzbData *nzb.NZB, providerName string, pool *nntp.ClientPool) *ValidationResult {
	result := &ValidationResult{
		Provider: providerName,
		Host:     pool.Host(),
	}

	articles := c.getSampleArticles(nzbData)
	result.TotalArticles = len(nzbData.Files[0].Segments)
	result.CheckedArticles = len(articles)

	client, ok := pool.TryGet(ctx)
	if !ok {
		var err error
		waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		client, err = pool.Get(waitCtx)
		if err != nil {
			result.Error = fmt.Errorf("pool busy: %w", err)
			return result
		}
	}

	releaseAsOk := false
	defer func() {
		if releaseAsOk {
			pool.Put(client)
		} else {
			pool.Discard(client)
		}
	}()

	// Stat articles in parallel (with concurrency limit)
	type statResult struct {
		exists bool
		err    error
	}
	statChan := make(chan statResult, len(articles))
	sem := make(chan struct{}, maxOr(c.maxConcurrent, 5))
	for _, articleID := range articles {
		articleID := articleID
		select {
		case <-ctx.Done():
			result.Error = ctx.Err()
			return result
		default:
		}
		go func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			exists, err := client.StatArticle(articleID)
			statChan <- statResult{exists, err}
		}()
	}
	missing := 0
	for range articles {
		select {
		case <-ctx.Done():
			result.Error = ctx.Err()
			return result
		case res := <-statChan:
			if res.err != nil {
				result.Error = res.err
				return result
			}
			if !res.exists {
				missing++
			}
		}
	}

	releaseAsOk = true
	result.MissingArticles = missing
	result.Available = missing == 0

	logger.Debug("Provider check", "provider", providerName, "available", result.CheckedArticles-missing, "total", result.CheckedArticles)

	return result
}

// validateProviderExtended runs the regular STAT check, then probes a few
// leading segments with BODY + yEnc decode to verify actual data integrity.
func (c *Checker) validateProviderExtended(ctx context.Context, nzbData *nzb.NZB, providerName string, pool *nntp.ClientPool) *ValidationResult {
	result := c.validateProvider(ctx, nzbData, providerName, pool)
	if result.Error != nil || !result.Available {
		return result
	}

	info := nzbData.GetPlaybackFile()
	if info == nil || info.File == nil || len(info.File.Segments) == 0 {
		return result
	}

	segments := info.File.Segments

	// Pick probe indices: first, last, middle -- deduplicated for small files.
	probeIndices := probeSegmentIndices(len(segments))

	client, ok := pool.TryGet(ctx)
	if !ok {
		waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		var err error
		client, err = pool.Get(waitCtx)
		if err != nil {
			return result
		}
	}

	releaseOk := false
	defer func() {
		if releaseOk {
			pool.Put(client)
		} else {
			pool.Discard(client)
		}
	}()

	if len(info.File.Groups) > 0 {
		_ = client.Group(info.File.Groups[0])
	}

	ct := nzbData.CompressionType()
	var firstSegData []byte
	var lastSegData []byte

	for _, idx := range probeIndices {
		body, err := client.Body(segments[idx].ID)
		if err != nil {
			result.Available = false
			result.Error = fmt.Errorf("body probe segment %d: %w", idx, err)
			logger.Debug("Extended check BODY failed", "provider", providerName, "segment", idx, "err", err)
			return result
		}
		frame, err := decode.DecodeToBytes(body)
		if err != nil {
			_, _ = io.Copy(io.Discard, body)
			result.Available = false
			result.Error = fmt.Errorf("decode probe segment %d: %w", idx, err)
			logger.Debug("Extended check decode failed", "provider", providerName, "segment", idx, "err", err)
			return result
		}
		if len(frame.Data) == 0 {
			result.Available = false
			result.Error = fmt.Errorf("probe segment %d decoded to empty data", idx)
			logger.Debug("Extended check empty segment", "provider", providerName, "segment", idx)
			return result
		}
		if idx == 0 {
			firstSegData = frame.Data
		}
		if idx == len(segments)-1 {
			lastSegData = frame.Data
		}
	}

	if firstSegData == nil {
		firstSegData = lastSegData
	}
	if lastSegData == nil {
		lastSegData = firstSegData
	}

	if ct != "direct" && len(firstSegData) > 0 {
		if err := verifyArchiveHeader(ct, firstSegData, lastSegData, info); err != nil {
			result.Available = false
			result.Error = err
			logger.Debug("Extended check archive header failed", "provider", providerName, "compression", ct, "err", err)
			return result
		}
	}

	releaseOk = true
	logger.Debug("Extended check passed", "provider", providerName, "probed", len(probeIndices), "compression", ct)
	return result
}

// probeSegmentIndices returns deduplicated indices for first, last, and middle
// segments. For files with 1 segment it returns [0]; for 2 segments [0, 1];
// for 3+ segments [0, mid, last].
func probeSegmentIndices(total int) []int {
	if total <= 0 {
		return nil
	}
	if total == 1 {
		return []int{0}
	}
	if total == 2 {
		return []int{0, 1}
	}
	return []int{0, total / 2, total - 1}
}

// getSampleArticles returns a sample of article IDs to check.
// Picks the file most relevant to playback: the first RAR volume for
// RAR releases, or the largest content file for direct/7z.
func (c *Checker) getSampleArticles(nzbData *nzb.NZB) []string {
	if len(nzbData.Files) == 0 {
		return nil
	}

	var file *nzb.File
	if info := nzbData.GetPlaybackFile(); info != nil {
		file = info.File
	} else {
		file = &nzbData.Files[0]
	}
	segments := file.Segments

	if len(segments) == 0 {
		return nil
	}

	// Sample evenly distributed segments
	sampleSize := c.sampleSize
	if sampleSize > len(segments) {
		sampleSize = len(segments)
	}

	articles := make([]string, 0, sampleSize)

	// Prioritize Critical Segments (Start & End)
	// Usually headers are at start, and important footers/recovery at end.

	// 1. Always check First Segment
	articles = append(articles, segments[0].ID)

	// 2. Always check Last Segment (if distinct)
	if len(segments) > 1 {
		articles = append(articles, segments[len(segments)-1].ID)
	}

	// 3. Fill the rest with distributed samples
	remainingSlots := sampleSize - len(articles)
	if remainingSlots > 0 {
		// Calculate internal range to sample from (exclude first and last if needed)
		startIdx := 1
		endIdx := len(segments) - 1
		if startIdx < endIdx {
			totalSpan := endIdx - startIdx
			step := float64(totalSpan) / float64(remainingSlots)

			for i := 0; i < remainingSlots; i++ {
				// Round to nearest integer index
				idx := startIdx + int(float64(i)*step)
				if idx < endIdx {
					articles = append(articles, segments[idx].ID)
				}
			}
		}
	}

	return articles
}

// verifyArchiveHeader parses downloaded segment data to confirm the archive
// is valid and uses STORE mode (required for streaming).
//
// For RAR: feeds the first segment to rardecode and checks the Stored flag.
// For 7z: builds a sparse ReaderAt from first + last segment data and uses
// sevenzip.NewReader to parse the encoded header (which lives at the archive tail).
func verifyArchiveHeader(ct string, firstSeg, lastSeg []byte, info *nzb.FileInfo) error {
	switch ct {
	case "rar":
		r, err := rardecode.NewReader(bytes.NewReader(firstSeg))
		if err != nil {
			return fmt.Errorf("invalid RAR archive: %w", err)
		}
		hdr, err := r.Next()
		if err != nil {
			return fmt.Errorf("cannot read RAR file entry: %w", err)
		}
		if !hdr.Stored {
			return fmt.Errorf("RAR archive uses compression (STORE mode required for streaming)")
		}
	case "7z":
		if err := verify7zHeader(firstSeg, lastSeg, info); err != nil {
			return err
		}
	}
	return nil
}

// verify7zHeader constructs a sparse ReaderAt covering the head and tail of
// the 7z volume and lets the sevenzip library parse the encoded header.
func verify7zHeader(headData, tailData []byte, info *nzb.FileInfo) error {
	if info == nil {
		return nil
	}
	totalSize := info.Size
	if totalSize <= 0 {
		return nil
	}

	// If the file is small enough that head covers everything, use it directly.
	if int64(len(headData)) >= totalSize {
		ra := bytes.NewReader(headData[:totalSize])
		return parse7z(ra, totalSize)
	}

	if len(tailData) == 0 {
		return nil
	}

	tailOffset := totalSize - int64(len(tailData))
	if tailOffset < 0 {
		tailOffset = 0
	}
	ra := &sparse7zReader{
		head:       headData,
		tail:       tailData,
		tailOffset: tailOffset,
		totalSize:  totalSize,
	}
	return parse7z(ra, totalSize)
}

func parse7z(ra io.ReaderAt, size int64) error {
	r, err := sevenzip.NewReader(ra, size)
	if err != nil {
		return fmt.Errorf("invalid 7z archive: %w", err)
	}
	infos, err := r.ListFilesWithOffsets()
	if err != nil {
		return fmt.Errorf("cannot list 7z contents: %w", err)
	}
	for _, fi := range infos {
		if fi.Size > 50*1024*1024 && fi.Compressed {
			return fmt.Errorf("7z archive uses compression (STORE mode required for streaming)")
		}
	}
	return nil
}

// sparse7zReader implements io.ReaderAt backed by a head and tail byte slice
// with a zero-filled gap in between. This lets sevenzip parse the signature
// header (at offset 0) and encoded header (near the end) without downloading
// the entire archive.
type sparse7zReader struct {
	head       []byte
	tail       []byte
	tailOffset int64
	totalSize  int64
}

func (s *sparse7zReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= s.totalSize {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) && off < s.totalSize {
		pos := off
		if pos < int64(len(s.head)) {
			// Read from head region
			copied := copy(p[n:], s.head[pos:])
			n += copied
			off += int64(copied)
		} else if pos >= s.tailOffset {
			// Read from tail region
			idx := pos - s.tailOffset
			if idx >= int64(len(s.tail)) {
				break
			}
			copied := copy(p[n:], s.tail[idx:])
			n += copied
			off += int64(copied)
		} else {
			// Gap between head and tail: fill with zeros
			end := s.tailOffset
			if end > off+int64(len(p)-n) {
				end = off + int64(len(p)-n)
			}
			gap := int(end - off)
			for i := 0; i < gap; i++ {
				p[n+i] = 0
			}
			n += gap
			off += int64(gap)
		}
	}
	if n == 0 {
		return 0, io.EOF
	}
	if off >= s.totalSize && n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// GetBestProvider returns the provider with highest availability
func GetBestProvider(results map[string]*ValidationResult) *ValidationResult {
	var bestResult *ValidationResult
	var bestScore float64

	for _, result := range results {
		if result.Error != nil || !result.Available {
			continue
		}

		// Calculate completion percentage
		var score float64
		if result.CheckedArticles == 0 {
			// Special case for skipped validation (trusted availability)
			// If Available is true (checked above), treat as 100%
			score = 1.0
		} else {
			score = float64(result.CheckedArticles-result.MissingArticles) / float64(result.CheckedArticles)
		}

		if score >= bestScore { // Use >= to pick the first one even if score is 0 or equal
			bestScore = score
			bestResult = result
		}
	}

	return bestResult
}
