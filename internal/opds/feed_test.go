package opds_test

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"github.com/banux/nxt-opds/internal/opds"
)

func TestNewNavigationFeed_Structure(t *testing.T) {
	feed := opds.NewNavigationFeed("urn:test:root", "Test Catalog")
	if feed.ID != "urn:test:root" {
		t.Errorf("expected ID urn:test:root, got %s", feed.ID)
	}
	if feed.Title.Value != "Test Catalog" {
		t.Errorf("expected title 'Test Catalog', got %s", feed.Title.Value)
	}
	if feed.Xmlns != opds.NSAtom {
		t.Errorf("expected xmlns %s, got %s", opds.NSAtom, feed.Xmlns)
	}
}

func TestFeed_AddLink(t *testing.T) {
	feed := opds.NewNavigationFeed("urn:test:root", "Test")
	feed.AddLink(opds.RelSelf, "/opds", opds.MIMENavigationFeed)

	if len(feed.Links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(feed.Links))
	}
	l := feed.Links[0]
	if l.Rel != opds.RelSelf {
		t.Errorf("expected rel %s, got %s", opds.RelSelf, l.Rel)
	}
	if l.Href != "/opds" {
		t.Errorf("expected href /opds, got %s", l.Href)
	}
}

func TestFeed_MarshalToXML_ValidXML(t *testing.T) {
	feed := opds.NewNavigationFeed("urn:test:root", "Test Catalog")
	feed.AddLink(opds.RelSelf, "/opds", opds.MIMENavigationFeed)
	feed.AddEntry(opds.Entry{
		ID:      "urn:test:entry:1",
		Title:   opds.Text{Value: "All Books"},
		Updated: opds.AtomDate{Time: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
		Links: []opds.Link{
			{Rel: opds.RelCatalogNavigation, Href: "/opds/books", Type: opds.MIMEAcquisitionFeed},
		},
	})

	data, err := feed.MarshalToXML()
	if err != nil {
		t.Fatalf("MarshalToXML failed: %v", err)
	}

	// Must start with XML declaration
	s := string(data)
	if !strings.HasPrefix(s, "<?xml") {
		t.Error("expected XML declaration at start")
	}

	// Must be parseable XML
	var out opds.Feed
	if err := xml.Unmarshal(data[len(xml.Header):], &out); err != nil {
		t.Fatalf("output is not valid XML: %v", err)
	}

	if out.ID != "urn:test:root" {
		t.Errorf("round-trip ID mismatch: got %s", out.ID)
	}
	if len(out.Entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(out.Entries))
	}
}

func TestAtomDate_MarshalXML_RFC3339(t *testing.T) {
	ref := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	feed := opds.NewNavigationFeed("urn:test", "T")
	feed.Updated = opds.AtomDate{Time: ref}

	data, err := feed.MarshalToXML()
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	// The RFC3339 date should appear in the output
	if !strings.Contains(string(data), "2024-06-15T12:00:00Z") {
		t.Errorf("expected RFC3339 date in output, got: %s", string(data))
	}
}

func TestFeed_MultipleEntries(t *testing.T) {
	feed := opds.NewAcquisitionFeed("urn:test:books", "All Books")
	for i := 0; i < 5; i++ {
		feed.AddEntry(opds.Entry{
			ID:    "urn:test:book:" + string(rune('0'+i)),
			Title: opds.Text{Value: "Book"},
		})
	}
	if len(feed.Entries) != 5 {
		t.Errorf("expected 5 entries, got %d", len(feed.Entries))
	}
}
