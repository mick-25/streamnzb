package indexer

import (
	"encoding/xml"
	"strings"
)

// Indexer defines the interface for interacting with Usenet indexers
type Indexer interface {
	Search(req SearchRequest) (*SearchResponse, error)
	DownloadNZB(nzbURL string) ([]byte, error)
	Ping() error
	Name() string
	GetUsage() Usage
}

// Usage represents the current API and download usage for an indexer
type Usage struct {
	APIHitsLimit       int
	APIHitsUsed        int
	APIHitsRemaining   int
	DownloadsLimit     int
	DownloadsUsed      int
	DownloadsRemaining int
}

// SearchRequest represents a search query
type SearchRequest struct {
	Query   string // Search query
	IMDbID  string // IMDb ID (tt1234567)
	TMDBID  string // TMDB ID
	TVDBID  string // TVDB ID (New)
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

// NewznabResponse contains metadata about the results
type NewznabResponse struct {
	Offset int `xml:"offset,attr"`
	Total  int `xml:"total,attr"`
}

// Channel contains the search results
type Channel struct {
	Response NewznabResponse `xml:"http://www.newznab.com/DTD/2010/feeds/attributes/ response"`
	Items    []Item          `xml:"item"`
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
	Enclosure   Enclosure   `xml:"enclosure"`
	Attributes  []Attribute `xml:"attr"`

	// SourceIndexer is the indexer that provided this item
	// This is not part of the XML, but populated by the client
	SourceIndexer Indexer `xml:"-"`

	// ActualIndexer is the real indexer name when using meta-indexers like NZBHydra2
	// This is populated from Newznab attributes and not part of the XML
	ActualIndexer string `xml:"-"`
	
	// ActualGUID is the real indexer GUID when using meta-indexers like NZBHydra2
	// This is extracted from the link field and not part of the XML
	ActualGUID string `xml:"-"`
}

// Attribute represents Newznab attributes
type Attribute struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

// Enclosure represents the enclosure tag (often contains size)
type Enclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
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

// ReleaseDetailsURL returns the stable indexer details URL for this release (for AvailNZB and reporting).
// Most indexers use GUID or details_link for the details page; Link is typically the NZB download URL.
func (i *Item) ReleaseDetailsURL() string {
	if i.ActualGUID != "" && strings.Contains(i.ActualGUID, "://") {
		return i.ActualGUID
	}
	if i.GUID != "" && strings.Contains(i.GUID, "://") {
		return i.GUID
	}
	return i.Link
}
