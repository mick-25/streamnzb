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
	providers map[string]*nntp.ClientPool
	cache     *Cache
	sampleSize int
	maxConcurrent int
}

// NewChecker creates a new article availability checker
func NewChecker(providers map[string]*nntp.ClientPool, cacheTTL time.Duration, sampleSize, maxConcurrent int) *Checker {
	return &Checker{
		providers:     providers,
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

// ValidateNZB checks article availability for an NZB across all providers
func (c *Checker) ValidateNZB(ctx context.Context, nzbData *nzb.NZB) map[string]*ValidationResult {
	results := make(map[string]*ValidationResult)
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Check cache first
	cacheKey := nzbData.Hash()
	if cached := c.cache.Get(cacheKey); cached != nil {
		logger.Debug("Using cached validation results for NZB")
		return cached
	}

	// Validate across all providers in parallel
	for providerName, pool := range c.providers {
		wg.Add(1)
		go func(name string, p *nntp.ClientPool) {
			defer wg.Done()

			result := c.validateProvider(ctx, nzbData, name, p)

			mu.Lock()
			results[name] = result
			mu.Unlock()
		}(providerName, pool)
	}

	wg.Wait()

	// Cache results
	c.cache.Set(cacheKey, results)

	return results
}

// validateProvider checks article availability for a single provider
func (c *Checker) validateProvider(ctx context.Context, nzbData *nzb.NZB, providerName string, pool *nntp.ClientPool) *ValidationResult {
	result := &ValidationResult{
		Provider: providerName,
		Host:     pool.Host(),
	}

	// Get sample of articles to check
	articles := c.getSampleArticles(nzbData)
	result.TotalArticles = len(nzbData.Files[0].Segments) // Total in first file
	result.CheckedArticles = len(articles)

	// Check articles using STAT command (faster than ARTICLE)
	client, err := pool.Get()
	if err != nil {
		result.Error = fmt.Errorf("failed to get client: %w", err)
		return result
	}
	defer pool.Put(client)

	missing := 0
	for _, articleID := range articles {
		select {
		case <-ctx.Done():
			result.Error = ctx.Err()
			return result
		default:
		}

		// Use STAT to check if article exists (doesn't download it)
		exists, err := client.StatArticle(articleID)
		if err != nil || !exists {
			missing++
		}
	}

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
		score := float64(result.CheckedArticles-result.MissingArticles) / float64(result.CheckedArticles)
		if score > bestScore {
			bestScore = score
			bestResult = result
		}
	}

	return bestResult
}
