package indexer

import "encoding/xml"

// Indexer defines the interface for interacting with Usenet indexers
type Indexer interface {
	Search(req SearchRequest) (*SearchResponse, error)
	DownloadNZB(nzbURL string) ([]byte, error)
	Ping() error
	Name() string
}

// SearchRequest represents a search query
type SearchRequest struct {
	Query   string // Search query
	IMDbID  string // IMDb ID (tt1234567)
	TMDBID  string // TMDB ID
	Cat     string // Category (movies, tv, etc)
	Limit   int    // Max results
	Season  string // Season number for TV searches
	Episode string // Episode number for TV searches
}

// SearchResponse represents the Newznab XML response
type SearchResponse struct {
	XMLName xml.Name `xml:"rss"`
	Channel Channel  `xml:"channel"`
}

// Channel contains the search results
type Channel struct {
	Items []Item `xml:"item"`
}

// Item represents a single NZB result
type Item struct {
	Title       string      `xml:"title"`
	Link        string      `xml:"link"`
	GUID        string      `xml:"guid"`
	PubDate     string      `xml:"pubDate"`
	Category    string      `xml:"category"`
	Description string      `xml:"description"`
	Size        int64       `xml:"size"`
	Attributes  []Attribute `xml:"attr"`

	// SourceIndexer is the indexer that provided this item
	// This is not part of the XML, but populated by the client
	SourceIndexer Indexer `xml:"-"`
}

// Attribute represents Newznab attributes
type Attribute struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

// GetAttribute retrieves a specific attribute from an item
func (i *Item) GetAttribute(name string) string {
	for _, attr := range i.Attributes {
		if attr.Name == name {
			return attr.Value
		}
	}
	return ""
}
