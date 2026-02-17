package validation

import (
	"context"
	"fmt"
	"sync"
	"time"

	"streamnzb/pkg/logger"
	"streamnzb/pkg/nntp"
	"streamnzb/pkg/nzb"
)

// Checker validates article availability across providers
type Checker struct {
	mu             sync.RWMutex
	providers      map[string]*nntp.ClientPool
	providerOrder  []string // Provider names in priority order (for single-provider validation)
	cache          *Cache
	sampleSize     int
	maxConcurrent  int
}

// NewChecker creates a new article availability checker.
// providerOrder is the list of provider names in priority order (used for cache warming).
func NewChecker(providers map[string]*nntp.ClientPool, providerOrder []string, cacheTTL time.Duration, sampleSize, maxConcurrent int) *Checker {
	return &Checker{
		providers:     providers,
		providerOrder: providerOrder,
		cache:         NewCache(cacheTTL),
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

// ValidateNZB checks article availability for an NZB across all providers
func (c *Checker) ValidateNZB(ctx context.Context, nzbData *nzb.NZB) map[string]*ValidationResult {
	results := make(map[string]*ValidationResult)
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Check cache first
	cacheKey := nzbData.Hash()
	if cached := c.cache.Get(cacheKey); cached != nil {
		logger.Debug("Using cached validation results for NZB")
		logger.Trace("ValidateNZB: cache hit", "hash", cacheKey)
		return cached
	}
	logger.Trace("ValidateNZB start", "hash", cacheKey)

	// Validate across all providers in parallel
	c.mu.RLock()
	providers := c.providers
	c.mu.RUnlock()

	for providerName, pool := range providers {
		wg.Add(1)
		go func(name string, p *nntp.ClientPool) {
			defer wg.Done()

			result := c.validateProvider(ctx, nzbData, name, p)

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

	// Cache results
	c.cache.Set(cacheKey, results)

	return results
}

// InvalidateCache removes validation results for an NZB from the cache
func (c *Checker) InvalidateCache(hash string) {
	c.cache.Remove(hash)
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

	missing := 0
	for _, articleID := range articles {
		select {
		case <-ctx.Done():
			result.Error = ctx.Err()
			return result
		default:
		}

		exists, err := client.StatArticle(articleID)
		if err != nil {
			result.Error = err
			return result
		}
		if !exists {
			missing++
		}
	}

	releaseAsOk = true
	result.MissingArticles = missing
	result.Available = missing == 0

	logger.Debug("Provider check", "provider", providerName, "available", result.CheckedArticles-missing, "total", result.CheckedArticles)

	return result
}

// getSampleArticles returns a sample of article IDs to check
func (c *Checker) getSampleArticles(nzbData *nzb.NZB) []string {
	if len(nzbData.Files) == 0 {
		return nil
	}

	// Check first file only (usually the media file)
	file := nzbData.Files[0]
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
