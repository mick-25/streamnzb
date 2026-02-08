package validation

import (
	"sync"
	"time"
)

// Cache stores validation results with TTL
type Cache struct {
	data map[string]*cacheEntry
	mu   sync.RWMutex
	ttl  time.Duration
}

type cacheEntry struct {
	results   map[string]*ValidationResult
	expiresAt time.Time
}

// NewCache creates a new validation cache
func NewCache(ttl time.Duration) *Cache {
	c := &Cache{
		data: make(map[string]*cacheEntry),
		ttl:  ttl,
	}

	// Start cleanup goroutine
	go c.cleanup()

	return c
}

// Get retrieves cached validation results
func (c *Cache) Get(key string) map[string]*ValidationResult {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, exists := c.data[key]
	if !exists {
		return nil
	}

	if time.Now().After(entry.expiresAt) {
		return nil
	}

	return entry.results
}

// Set stores validation results in cache
func (c *Cache) Set(key string, results map[string]*ValidationResult) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.data[key] = &cacheEntry{
		results:   results,
		expiresAt: time.Now().Add(c.ttl),
	}
}

// Remove deletes an entry from the cache
func (c *Cache) Remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.data, key)
}

// cleanup removes expired entries periodically
func (c *Cache) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		c.mu.Lock()
		now := time.Now()
		for key, entry := range c.data {
			if now.After(entry.expiresAt) {
				delete(c.data, key)
			}
		}
		c.mu.Unlock()
	}
}
