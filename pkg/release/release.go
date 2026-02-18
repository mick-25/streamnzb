package release

import (
	"net"
	"net/url"
	"strings"
)

// IsPrivateReleaseURL returns true if the URL host is private/local (localhost).
// We must not report such URLs to AvailNZB — they're from someone's NZBHydra proxy and useless to others.
func IsPrivateReleaseURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return true // treat unparseable as private to be safe
	}
	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		host = u.Hostname()
	}
	ip := net.ParseIP(host)
	if ip != nil {
		return ip.IsPrivate() || ip.IsLoopback()
	}
	lower := strings.ToLower(host)
	return lower == "localhost" || strings.HasSuffix(lower, ".local")
}

// Release is a unified representation of an NZB release from indexers or AvailNZB.
// Used for comparison (by normalized title) and as a common type across the app.
// AvailNZB may return partial data (e.g. no SourceIndexer for download).
type Release struct {
	Title         string // Release name (e.g. "Movie.2024.1080p.BluRay.x264-GROUP")
	Link          string // NZB download URL
	DetailsURL    string // Stable identifier for AvailNZB/reporting
	Size          int64
	Indexer       string      // Actual indexer name (NZBGeek, Drunken Slug, etc.)
	SourceIndexer interface{} // Indexer client for DownloadNZB. Nil when from AvailNZB.

	// Optional fields from indexer search (empty when from AvailNZB)
	PubDate     string // RFC1123/RFC1123Z for age scoring
	GUID        string // For session ID when skipping validation
	QuerySource string // "id" or "text" — ID-based results prioritized
	Grabs       int    // From newznab grabs attribute, for popularity scoring
}

// EqualByTitle returns true if both releases have the same normalized title.
// Use for matching indexer results with AvailNZB releases (e.g. filtering RAR).
func (r *Release) EqualByTitle(other *Release) bool {
	if r == nil || other == nil {
		return r == other
	}
	return NormalizeTitle(r.Title) == NormalizeTitle(other.Title)
}

// NormalizeTitle normalizes a release title for comparison (lowercase, trimmed).
func NormalizeTitle(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
