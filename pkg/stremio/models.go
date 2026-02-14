package stremio

// StreamResponse represents the response to a stream request
type StreamResponse struct {
	Streams []Stream `json:"streams"`
}

// Stream represents a single stream option
type Stream struct {
	// URL for direct streaming (HTTP video file)
	URL string `json:"url,omitempty"`

	// ExternalUrl for external player (alternative to URL)
	ExternalUrl string `json:"externalUrl,omitempty"`

	// Display name in Stremio
	Name string `json:"name,omitempty"`

	// Optional metadata (shown in Stremio UI)
	Title         string         `json:"title,omitempty"`
	Description   string         `json:"description,omitempty"`
	BehaviorHints *BehaviorHints `json:"behaviorHints,omitempty"`
}

// BehaviorHints provides hints to Stremio about stream behavior
type BehaviorHints struct {
	NotWebReady      bool     `json:"notWebReady,omitempty"`
	BingeGroup       string   `json:"bingeGroup,omitempty"`
	CountryWhitelist []string `json:"countryWhitelist,omitempty"`
	VideoSize        int64    `json:"videoSize,omitempty"`
	Filename         string   `json:"filename,omitempty"`
}
