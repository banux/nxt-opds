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
//
// Per OPDS 2.0 spec, "navigation" is a Compact Collection: a flat list of
// Link Objects. We therefore use []Link directly rather than a dedicated
// type so the JSON shape matches the reference catalog at
// https://test.opds.io/2.0/home.json — and so strict clients (Cantook,
// Foliate) accept the feed.
type Feed struct {
	Metadata     FeedMetadata  `json:"metadata"`
	Links        []Link        `json:"links"`
	Navigation   []Link        `json:"navigation,omitempty"`
	Publications []Publication `json:"publications,omitempty"`
	// Facets exposes filtering options to OPDS clients per spec §2.6.
	// Cantook renders each FacetGroup as a row of clickable filter chips
	// above the publication list.
	Facets []FacetGroup `json:"facets,omitempty"`
	// Groups exposes multiple sub-collections in a single response per spec §2.5.
	// Each Group is either a navigation group (Group.Navigation) OR an
	// acquisition group (Group.Publications) — never both.  Cantook renders
	// acquisition groups as horizontal carousels of covers on the home screen.
	Groups []Group `json:"groups,omitempty"`
}

// Group is a sub-section of a Feed, either a navigation list or an
// acquisition list (per OPDS 2.0 §2.5 — mutually exclusive).
type Group struct {
	Metadata     GroupMetadata `json:"metadata"`
	Links        []Link        `json:"links,omitempty"`        // self link toward the dedicated feed
	Navigation   []Link        `json:"navigation,omitempty"`   // navigation group
	Publications []Publication `json:"publications,omitempty"` // acquisition group
}

// GroupMetadata holds the title and (optional) total count of a Group.
// NumberOfItems is the TOTAL items reachable via the self link, not
// merely len(Publications) which is bounded by the carousel limit.
type GroupMetadata struct {
	Title         string `json:"title"`
	NumberOfItems int    `json:"numberOfItems,omitempty"`
}

// FacetGroup is a single facet (e.g. "Classification d'âge"), holding the
// list of clickable filter values as Link Objects.
type FacetGroup struct {
	Metadata FacetMetadata `json:"metadata"`
	Links    []Link        `json:"links"`
}

// FacetMetadata holds the display title of a facet group.
type FacetMetadata struct {
	Title string `json:"title"`
}

// LinkProperties carries optional Readium-style metadata on a Link Object
// (notably the facet-count NumberOfItems hint).
type LinkProperties struct {
	NumberOfItems int `json:"numberOfItems,omitempty"`
}

// FeedMetadata holds top-level metadata for a feed.
type FeedMetadata struct {
	Title         string `json:"title"`
	NumberOfItems int    `json:"numberOfItems,omitempty"`
}

// Link represents a link in the feed or in a publication.
type Link struct {
	Rel        interface{}     `json:"rel,omitempty"` // string or []string
	Href       string          `json:"href"`
	Type       string          `json:"type,omitempty"`
	Title      string          `json:"title,omitempty"`
	Templated  bool            `json:"templated,omitempty"`
	Properties *LinkProperties `json:"properties,omitempty"`
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
