// Package opds implements OPDS Catalog 1.2 feed types and XML serialization.
// OPDS (Open Publication Distribution System) is an Atom-based catalog format
// for distributing digital publications.
//
// Specification: https://specs.opds.io/opds-1.2
package opds

import (
	"encoding/xml"
	"time"
)

const (
	// Namespaces
	NSAtom       = "http://www.w3.org/2005/Atom"
	NSOPDS       = "http://opds-spec.org/2010/catalog"
	NSDC         = "http://purl.org/dc/terms/"
	NSDCElements = "http://purl.org/dc/elements/1.1/"
	NSCalibre    = "http://calibre.kovidgoyal.net/2009/metadata"

	// OPDS relation types
	RelAcquisition         = "http://opds-spec.org/acquisition"
	RelAcquisitionOpen     = "http://opds-spec.org/acquisition/open-access"
	RelAcquisitionBorrow   = "http://opds-spec.org/acquisition/borrow"
	RelAcquisitionBuy      = "http://opds-spec.org/acquisition/buy"
	RelAcquisitionSample   = "http://opds-spec.org/acquisition/sample"
	RelCover               = "http://opds-spec.org/image"
	RelThumbnail           = "http://opds-spec.org/image/thumbnail"
	RelCatalogNavigation   = "subsection"
	RelCatalogNew          = "http://opds-spec.org/sort/new"
	RelCatalogPopular      = "http://opds-spec.org/sort/popular"
	RelSelf                = "self"
	RelStart               = "start"
	RelSearch              = "search"
	RelFirst               = "first"
	RelLast                = "last"
	RelNext                = "next"
	RelPrevious            = "previous"

	// MIME types
	MIMEAtomFeed         = "application/atom+xml"
	MIMEAtomEntry        = "application/atom+xml;type=entry;profile=opds-catalog"
	MIMENavigationFeed   = "application/atom+xml;profile=opds-catalog;kind=navigation"
	MIMEAcquisitionFeed  = "application/atom+xml;profile=opds-catalog;kind=acquisition"
	MIMEOpenSearchDesc   = "application/opensearchdescription+xml"
	MIMEEPub             = "application/epub+zip"
	MIMEPdf              = "application/pdf"
	MIMEMobiPocket       = "application/x-mobipocket-ebook"
	MIMEAZWThree         = "application/x-mobi8-ebook"
	MIMECBZ              = "application/x-cbz"
	MIMECBR              = "application/x-cbr"
)

// Feed represents an OPDS Atom feed (navigation or acquisition).
type Feed struct {
	XMLName      xml.Name `xml:"feed"`
	Xmlns        string   `xml:"xmlns,attr"`
	XmlnsOS      string   `xml:"xmlns:os,attr,omitempty"`
	XmlnsCalibre string   `xml:"xmlns:calibre,attr,omitempty"`

	ID      string  `xml:"id"`
	Title   Text    `xml:"title"`
	Updated AtomDate `xml:"updated"`
	Author  *Author  `xml:"author,omitempty"`
	Icon    string   `xml:"icon,omitempty"`

	Links   []Link  `xml:"link"`
	Entries []Entry `xml:"entry"`
}

// NewNavigationFeed creates a new navigation feed with standard namespaces.
func NewNavigationFeed(id, title string) *Feed {
	return &Feed{
		Xmlns:   NSAtom,
		ID:      id,
		Title:   Text{Value: title},
		Updated: AtomDate{Time: time.Now()},
	}
}

// NewAcquisitionFeed creates a new acquisition feed with standard namespaces.
// The Calibre namespace is always declared so that series metadata can be included.
func NewAcquisitionFeed(id, title string) *Feed {
	return &Feed{
		Xmlns:        NSAtom,
		XmlnsCalibre: NSCalibre,
		ID:           id,
		Title:        Text{Value: title},
		Updated:      AtomDate{Time: time.Now()},
	}
}

// Text represents an Atom text element with optional type attribute.
type Text struct {
	Type  string `xml:"type,attr,omitempty"`
	Value string `xml:",chardata"`
}

// Author represents the author of a feed or entry.
type Author struct {
	Name  string `xml:"name"`
	URI   string `xml:"uri,omitempty"`
	Email string `xml:"email,omitempty"`
}

// AtomDate wraps time.Time for RFC 3339 XML serialization.
type AtomDate struct {
	Time time.Time
}

func (d AtomDate) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	return e.EncodeElement(d.Time.UTC().Format(time.RFC3339), start)
}

func (d *AtomDate) UnmarshalXML(dec *xml.Decoder, start xml.StartElement) error {
	var s string
	if err := dec.DecodeElement(&s, &start); err != nil {
		return err
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return err
	}
	d.Time = t
	return nil
}

// Link represents an Atom link element.
type Link struct {
	Rel      string `xml:"rel,attr,omitempty"`
	Href     string `xml:"href,attr"`
	Type     string `xml:"type,attr,omitempty"`
	Title    string `xml:"title,attr,omitempty"`
	Count    int    `xml:"count,attr,omitempty"`
}

// Entry represents a single entry in an OPDS feed.
// It can be a navigation entry (pointing to another feed)
// or an acquisition entry (pointing to a publication).
type Entry struct {
	ID      string   `xml:"id"`
	Title   Text     `xml:"title"`
	Updated AtomDate `xml:"updated"`
	Summary *Text    `xml:"summary,omitempty"`
	Content *Content `xml:"content,omitempty"`
	Authors []Author `xml:"author,omitempty"`

	// Dublin Core metadata
	Language  string `xml:"language,omitempty"`
	Publisher string `xml:"publisher,omitempty"`
	Published string `xml:"published,omitempty"`

	// Calibre series extensions (widely supported by OPDS clients)
	CalSeries      string `xml:"http://calibre.kovidgoyal.net/2009/metadata series,omitempty"`
	CalSeriesIndex string `xml:"http://calibre.kovidgoyal.net/2009/metadata series_index,omitempty"`

	Links []Link `xml:"link"`
}

// Content represents an Atom content element.
type Content struct {
	Type  string `xml:"type,attr,omitempty"`
	Value string `xml:",chardata"`
}

// AddLink appends a link to the feed.
func (f *Feed) AddLink(rel, href, mimeType string) {
	f.Links = append(f.Links, Link{Rel: rel, Href: href, Type: mimeType})
}

// AddEntry appends an entry to the feed.
func (f *Feed) AddEntry(e Entry) {
	f.Entries = append(f.Entries, e)
}

// MarshalXML serializes the feed to XML bytes with a proper XML declaration.
func (f *Feed) MarshalToXML() ([]byte, error) {
	data, err := xml.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), data...), nil
}
