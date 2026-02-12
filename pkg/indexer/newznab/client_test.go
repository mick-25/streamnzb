package newznab

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"streamnzb/pkg/config"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/logger"
	"testing"
)

func TestNewznabSearch(t *testing.T) {
	logger.Init("DEBUG")
	// Mock Newznab server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check API key
		if r.URL.Query().Get("apikey") != "test-api-key" {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		// Check search type
		if r.URL.Query().Get("t") != "movie" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Return mock XML with size in attributes but NOT in top-level size tag
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
<channel>
<title>Mock Newznab Search</title>
<newznab:response offset="0" total="1"/>
<item>
	<title>Test Movie 2024</title>
	<link>http://example.com/nzb/1</link>
	<guid isPermaLink="false">123456</guid>
	<pubDate>Mon, 01 Jan 2024 00:00:00 +0000</pubDate>
	<category>Movies &gt; HD</category>
	<description>Test Movie 2024</description>
	<newznab:attr name="size" value="1073741824" />
</item>
</channel>
</rss>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "MockIndexer",
		URL:    server.URL,
		APIKey: "test-api-key",
	}, nil)
	req := indexer.SearchRequest{
		Cat:    "2000",
		Query:  "Test Movie",
		IMDbID: "tt1234567",
	}

	resp, err := client.Search(req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(resp.Channel.Items) != 1 {
		t.Fatalf("Expected 1 item, got %d", len(resp.Channel.Items))
	}

	item := resp.Channel.Items[0]
	if item.Title != "Test Movie 2024" {
		t.Errorf("Expected title 'Test Movie 2024', got '%s'", item.Title)
	}

	// Verify size extraction from attributes
	if item.Size != 1073741824 {
		t.Errorf("Expected size 1073741824, got %d", item.Size)
	}

	if item.SourceIndexer == nil {
		t.Error("SourceIndexer was not populated")
	}
}

func TestNewznabPagination(t *testing.T) {
	logger.Init("DEBUG")
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		limit := r.URL.Query().Get("limit")

		w.Header().Set("Content-Type", "application/xml")
		// Indexer handles pagination internally, returns all requested items in one call
		if limit == "2" {
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
<channel>
<newznab:response offset="0" total="2"/>
<item><title>Item 1</title><newznab:attr name="size" value="100"/></item>
<item><title>Item 2</title><newznab:attr name="size" value="200"/></item>
</channel>
</rss>`)
		} else {
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
		}
		logger.Debug("Mock server call", "count", callCount, "limit", limit)
	}))
	defer server.Close()

	// Request limit = 2, indexer should return all 2 items in one call
	client := NewClient(config.IndexerConfig{
		Name:   "MockIndexer",
		URL:    server.URL,
		APIKey: "test-api-key",
	}, nil)
	req := indexer.SearchRequest{
		Limit: 2,
	}

	resp, err := client.Search(req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(resp.Channel.Items) != 2 {
		t.Fatalf("Expected 2 items, got %d", len(resp.Channel.Items))
	}

	if callCount != 1 {
		t.Errorf("Expected 1 server call (indexer handles pagination), got %d", callCount)
	}
}

func TestNewznabPing(t *testing.T) {
	logger.Init("DEBUG")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") == "caps" {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "MockIndexer",
		URL:    server.URL,
		APIKey: "test-api-key",
	}, nil)
	err := client.Ping()
	if err != nil {
		t.Errorf("Ping failed: %v", err)
	}
}
