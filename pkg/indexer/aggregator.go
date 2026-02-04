package indexer

import (
	"encoding/xml"
	"fmt"
	"sort"
	"sync"
)

// Aggregator combines multiple indexers into one
type Aggregator struct {
	Indexers []Indexer
}

// Name returns the name of the aggregator
func (a *Aggregator) Name() string {
	return "Aggregator"
}

// NewAggregator creates a new indexer aggregator
func NewAggregator(indexers ...Indexer) *Aggregator {
	return &Aggregator{
		Indexers: indexers,
	}
}

// Ping checks if all configured indexers are reachable
// Returns nil if at least one is reachable, otherwise the last error
func (a *Aggregator) Ping() error {
	var lastErr error
	successCount := 0

	for _, idx := range a.Indexers {
		if err := idx.Ping(); err != nil {
			lastErr = err
		} else {
			successCount++
		}
	}

	if successCount == 0 && len(a.Indexers) > 0 {
		return fmt.Errorf("all indexers failed ping, last error: %w", lastErr)
	}
	return nil
}

// DownloadNZB attempts to download using specific logic if needed,
// but usually just passes through to the first capable indexer or generic HTTP
// For now, we'll try the first indexer that can handle it or just a simple GET
// Actually, since interfaces don't have "Download" as common beyond HTTP GET usually,
// we just pick the first indexer to perform the download as they are all HTTP clients.
func (a *Aggregator) DownloadNZB(nzbURL string) ([]byte, error) {
	if len(a.Indexers) == 0 {
		return nil, fmt.Errorf("no indexers configured")
	}
	// Just use the first one, as they all share similar download logic (HTTP GET)
	// In the future, we might route based on domain if needed.
	return a.Indexers[0].DownloadNZB(nzbURL)
}

// Search queries all indexers in parallel and merges results
func (a *Aggregator) Search(req SearchRequest) (*SearchResponse, error) {
	resultsChan := make(chan []Item, len(a.Indexers))
	var wg sync.WaitGroup

	// Launch parallel searches
	for _, idx := range a.Indexers {
		wg.Add(1)
		go func(indexer Indexer) {
			defer wg.Done()
			
			resp, err := indexer.Search(req)
			if err != nil {
				// Log error but don't fail entire search?
				// For now we just return empty result for this indexer
				fmt.Printf("Indexer %s search failed: %v\n", indexer.Name(), err)
				resultsChan <- []Item{}
				return
			}
			
			if resp != nil {
				resultsChan <- resp.Channel.Items
			}
		}(idx)
	}

	// Wait for all searches to complete
	wg.Wait()
	close(resultsChan)

	// Collect results
	var allItems []Item
	for items := range resultsChan {
		allItems = append(allItems, items...)
	}

	// Deduplicate based on GUID (if available) or Link
	// Simple map-based dedup
	seen := make(map[string]bool)
	uniqueItems := []Item{}
	
	for _, item := range allItems {
		key := item.GUID
		if key == "" {
			key = item.Link
		}
		
		if !seen[key] {
			seen[key] = true
			uniqueItems = append(uniqueItems, item)
		}
	}
	
	// Sort by size descending (usually preferred) or published date?
	// Let's keep original order (roughly) but maybe size sort helps
	// Stremio addon usually sorts by quality/size later anyway.
	// Let's just return unique items.
	
	// Sort by size descending as a default heuristic
	sort.Slice(uniqueItems, func(i, j int) bool {
		return uniqueItems[i].Size > uniqueItems[j].Size
	})

	return &SearchResponse{
		XMLName: xml.Name{Local: "rss"},
		Channel: Channel{
			Items: uniqueItems,
		},
	}, nil
}
