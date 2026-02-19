// Package opds2 implements OPDS Catalog 2.0 feed types and JSON serialization.
// OPDS 2.0 is based on the Readium Web Publication Manifest format and uses JSON
// instead of the Atom/XML format used by OPDS 1.2.
//
// Specification: https://drafts.opds.io/opds-2.0
package opds2

// MIME types for OPDS 2.0.
const (
	MIMEFeed    = "application/opds+json"
	MIMENavFeed = "application/opds+json" // same type, navigation vs acquisition is inferred from content
)

// Feed is the root object for an OPDS 2.0 feed.
// It may be a navigation feed, an acquisition feed, or a combined feed.
type Feed struct {
	Metadata     FeedMetadata  `json:"metadata"`
	Links        []Link        `json:"links"`
	Navigation   []NavItem     `json:"navigation,omitempty"`
	Publications []Publication `json:"publications,omitempty"`
}

// FeedMetadata holds top-level metadata for a feed.
type FeedMetadata struct {
	Title         string `json:"title"`
	NumberOfItems int    `json:"numberOfItems,omitempty"`
}

// Link represents a link in the feed or in a publication.
type Link struct {
	Rel       interface{} `json:"rel,omitempty"` // string or []string
	Href      string      `json:"href"`
	Type      string      `json:"type,omitempty"`
	Title     string      `json:"title,omitempty"`
	Templated bool        `json:"templated,omitempty"`
}

// NavItem is a navigation entry in a navigation feed.
type NavItem struct {
	Title string `json:"title"`
	Href  string `json:"href"`
	Type  string `json:"type,omitempty"`
	Rel   string `json:"rel,omitempty"`
}

// Publication represents a book in an acquisition feed.
type Publication struct {
	Metadata PubMetadata `json:"metadata"`
	Links    []Link      `json:"links"`
	Images   []Link      `json:"images,omitempty"`
}

// PubMetadata holds structured metadata for a publication.
type PubMetadata struct {
	Type        string        `json:"@type,omitempty"`
	Title       string        `json:"title"`
	Author      interface{}   `json:"author,omitempty"` // Contributor or []Contributor
	Language    interface{}   `json:"language,omitempty"` // string or []string
	Publisher   string        `json:"publisher,omitempty"`
	Description string        `json:"description,omitempty"`
	Subject     []Subject     `json:"subject,omitempty"`
	Identifier  string        `json:"identifier,omitempty"`
	Modified    string        `json:"modified,omitempty"`
	Published   string        `json:"published,omitempty"`
	BelongsTo   *BelongsTo    `json:"belongsTo,omitempty"`
}

// Contributor represents an author or other contributor.
type Contributor struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

// Subject represents a subject/tag/genre with optional scheme.
type Subject struct {
	Name string `json:"name"`
	Code string `json:"code,omitempty"`
}

// BelongsTo groups series memberships for a publication.
type BelongsTo struct {
	Series []Series `json:"series,omitempty"`
}

// Series represents a series a book belongs to.
type Series struct {
	Name     string  `json:"name"`
	Position float64 `json:"position,omitempty"`
}
