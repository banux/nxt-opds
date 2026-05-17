package server

import (
	"archive/zip"
	"crypto/subtle"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/skip2/go-qrcode"

	"github.com/banux/nxt-opds/internal/catalog"
	"github.com/banux/nxt-opds/internal/opds"
	"github.com/banux/nxt-opds/internal/opds2"
	"github.com/banux/nxt-opds/internal/updater"
)

const (
	defaultPageSize = 50
	maxPageSize     = 200
)

// writeOPDS writes an OPDS XML feed response.
func writeOPDS(w http.ResponseWriter, status int, feed *opds.Feed) {
	data, err := feed.MarshalToXML()
	if err != nil {
		http.Error(w, "feed serialization error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", opds.MIMENavigationFeed+"; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// parsePagination extracts offset and limit from query parameters.
func parsePagination(r *http.Request) (offset, limit int) {
	q := r.URL.Query()
	offset, _ = strconv.Atoi(q.Get("offset"))
	limit, _ = strconv.Atoi(q.Get("limit"))
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > maxPageSize {
		limit = defaultPageSize
	}
	return
}

// paginationLink builds a URL for the given page by replacing the offset and
// limit query parameters while preserving all other query parameters (e.g. q=).
func paginationLink(r *http.Request, offset, limit int) string {
	q := r.URL.Query()
	q.Set("offset", strconv.Itoa(offset))
	q.Set("limit", strconv.Itoa(limit))
	return r.URL.Path + "?" + q.Encode()
}

// addPaginationLinks appends OPDS-standard first/previous/next/last link elements
// to feed when the result set spans more than one page.
func addPaginationLinks(feed *opds.Feed, r *http.Request, offset, limit, total int, mimeType string) {
	if total <= 0 || limit <= 0 {
		return
	}
	lastOffset := ((total - 1) / limit) * limit
	feed.AddLink(opds.RelFirst, paginationLink(r, 0, limit), mimeType)
	if offset > 0 {
		prevOffset := offset - limit
		if prevOffset < 0 {
			prevOffset = 0
		}
		feed.AddLink(opds.RelPrevious, paginationLink(r, prevOffset, limit), mimeType)
	}
	if offset+limit < total {
		feed.AddLink(opds.RelNext, paginationLink(r, offset+limit, limit), mimeType)
	}
	feed.AddLink(opds.RelLast, paginationLink(r, lastOffset, limit), mimeType)
}

// bookToEntry converts a catalog.Book to an opds.Entry for an acquisition feed.
// tok is the OPDS authentication token to append to all URLs (may be empty).
func bookToEntry(b catalog.Book, tok string) opds.Entry {
	entry := opds.Entry{
		ID:      "urn:nxt-opds:book:" + b.ID,
		Title:   opds.Text{Value: b.Title},
		Updated: opds.AtomDate{Time: b.UpdatedAt},
	}

	if b.Summary != "" {
		entry.Summary = &opds.Text{Value: b.Summary}
	}

	if !b.PublishedAt.IsZero() {
		entry.Published = b.PublishedAt.UTC().Format(time.RFC3339)
	}

	for _, a := range b.Authors {
		entry.Authors = append(entry.Authors, opds.Author{Name: a.Name, URI: a.URI})
	}

	if b.Series != "" {
		entry.CalSeries = b.Series
		entry.CalSeriesIndex = b.SeriesIndex
	}

	// Acquisition links for each available file
	for _, f := range b.Files {
		entry.Links = append(entry.Links, opds.Link{
			Rel:  opds.RelAcquisition,
			Href: withToken("/opds/books/"+b.ID+"/download?path="+url.QueryEscape(f.Path), tok),
			Type: f.MIMEType,
		})
	}

	if b.CoverURL != "" {
		entry.Links = append(entry.Links, opds.Link{
			Rel:  opds.RelCover,
			Href: withToken(b.CoverURL, tok),
			Type: "image/jpeg",
		})
	}
	if b.ThumbnailURL != "" {
		entry.Links = append(entry.Links, opds.Link{
			Rel:  opds.RelThumbnail,
			Href: withToken(b.ThumbnailURL, tok),
			Type: "image/jpeg",
		})
	}

	return entry
}

// handleRoot serves the root OPDS navigation feed.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")

	feed := opds.NewNavigationFeed(
		"urn:nxt-opds:root",
		"nxt-opds Catalog",
	)
	feed.Author = &opds.Author{Name: "nxt-opds"}

	// Self link
	feed.AddLink(opds.RelSelf, withToken("/opds", tok), opds.MIMENavigationFeed)
	// Start link (root)
	feed.AddLink(opds.RelStart, withToken("/opds", tok), opds.MIMENavigationFeed)
	// Search link
	feed.AddLink(opds.RelSearch, withToken("/opds/opensearch.xml", tok), opds.MIMEOpenSearchDesc)

	now := time.Now()

	// Navigation entries
	feed.AddEntry(opds.Entry{
		ID:      "urn:nxt-opds:all-books",
		Title:   opds.Text{Value: "All Books"},
		Updated: opds.AtomDate{Time: now},
		Content: &opds.Content{Type: "text", Value: "Browse all books in the catalog"},
		Links: []opds.Link{
			{Rel: opds.RelCatalogNavigation, Href: withToken("/opds/books", tok), Type: opds.MIMEAcquisitionFeed},
		},
	})

	feed.AddEntry(opds.Entry{
		ID:      "urn:nxt-opds:by-author",
		Title:   opds.Text{Value: "By Author"},
		Updated: opds.AtomDate{Time: now},
		Content: &opds.Content{Type: "text", Value: "Browse books by author"},
		Links: []opds.Link{
			{Rel: opds.RelCatalogNavigation, Href: withToken("/opds/authors", tok), Type: opds.MIMENavigationFeed},
		},
	})

	feed.AddEntry(opds.Entry{
		ID:      "urn:nxt-opds:by-tag",
		Title:   opds.Text{Value: "By Genre"},
		Updated: opds.AtomDate{Time: now},
		Content: &opds.Content{Type: "text", Value: "Browse books by genre/tag"},
		Links: []opds.Link{
			{Rel: opds.RelCatalogNavigation, Href: withToken("/opds/tags", tok), Type: opds.MIMENavigationFeed},
		},
	})

	feed.AddEntry(opds.Entry{
		ID:      "urn:nxt-opds:unread",
		Title:   opds.Text{Value: "Unread Books"},
		Updated: opds.AtomDate{Time: now},
		Content: &opds.Content{Type: "text", Value: "Browse books not yet read"},
		Links: []opds.Link{
			{Rel: opds.RelCatalogNavigation, Href: withToken("/opds/unread", tok), Type: opds.MIMEAcquisitionFeed},
		},
	})

	feed.AddEntry(opds.Entry{
		ID:      "urn:nxt-opds:by-publisher",
		Title:   opds.Text{Value: "By Publisher"},
		Updated: opds.AtomDate{Time: now},
		Content: &opds.Content{Type: "text", Value: "Browse books by publisher"},
		Links: []opds.Link{
			{Rel: opds.RelCatalogNavigation, Href: withToken("/opds/publishers", tok), Type: opds.MIMENavigationFeed},
		},
	})

	// "Niveaux de piment" — hide entirely for child profiles, the upstream
	// MaxAgeRating filter already prevents them from accessing 16+/18+ titles.
	if s.maxAgeRatingForUser(currentUserID(r)) == 0 {
		feed.AddEntry(opds.Entry{
			ID:      "urn:nxt-opds:spice",
			Title:   opds.Text{Value: "Niveaux de piment"},
			Updated: opds.AtomDate{Time: now},
			Content: &opds.Content{Type: "text", Value: "Filtrer par intensité (🌶 0 à 4)"},
			Links: []opds.Link{
				{Rel: opds.RelCatalogNavigation, Href: withToken("/opds/spice", tok), Type: opds.MIMENavigationFeed},
			},
		})
	}

	if s.wishlistManager != nil {
		feed.AddEntry(opds.Entry{
			ID:      "urn:nxt-opds:wishlist",
			Title:   opds.Text{Value: "Liste de souhaits"},
			Updated: opds.AtomDate{Time: now},
			Content: &opds.Content{Type: "text", Value: "Livres recherchés"},
			Links: []opds.Link{
				{Rel: opds.RelCatalogNavigation, Href: withToken("/opds/wishlist", tok), Type: opds.MIMENavigationFeed},
			},
		})
	}

	if s.recommender != nil && s.userManager != nil {
		feed.AddEntry(opds.Entry{
			ID:      "urn:nxt-opds:recommendations",
			Title:   opds.Text{Value: "Recommandations"},
			Updated: opds.AtomDate{Time: now},
			Content: &opds.Content{Type: "text", Value: "Livres recommandés"},
			Links: []opds.Link{
				{Rel: opds.RelCatalogNavigation, Href: withToken("/opds/recommendations", tok), Type: opds.MIMEAcquisitionFeed},
			},
		})
	}

	if s.toReadManager != nil {
		s.appendToReadV1Entries(feed, r, tok, now)
	}

	writeOPDS(w, http.StatusOK, feed)
}

// appendToReadV1Entries emits one or more "Pile à lire" entries on the OPDS v1
// root navigation feed.  When the request has a session-cookie userID the
// entry points to /opds/to-read (the handler reads the userID from the
// session).  When there is no session userID and multi-user mode is active
// (typical for OPDS reader clients that only have the shared OPDS token), one
// entry per user is emitted with ?user=<id> so the reader can pick a pile.
// In single-user mode (no users) a single generic entry is emitted.
func (s *Server) appendToReadV1Entries(feed *opds.Feed, r *http.Request, tok string, now time.Time) {
	if currentUserID(r) != "" || !s.hasMultipleUsers() {
		feed.AddEntry(opds.Entry{
			ID:      "urn:nxt-opds:to-read",
			Title:   opds.Text{Value: "Pile à lire"},
			Updated: opds.AtomDate{Time: now},
			Content: &opds.Content{Type: "text", Value: "Livres à lire prochainement"},
			Links: []opds.Link{
				{Rel: opds.RelCatalogNavigation, Href: withToken("/opds/to-read", tok), Type: opds.MIMEAcquisitionFeed},
			},
		})
		return
	}
	users, err := s.userManager.Users()
	if err != nil {
		return
	}
	for _, u := range users {
		href := "/opds/to-read?user=" + url.QueryEscape(u.ID)
		feed.AddEntry(opds.Entry{
			ID:      "urn:nxt-opds:to-read:" + u.ID,
			Title:   opds.Text{Value: "Pile à lire de " + u.Name},
			Updated: opds.AtomDate{Time: now},
			Content: &opds.Content{Type: "text", Value: "Livres à lire pour " + u.Name},
			Links: []opds.Link{
				{Rel: opds.RelCatalogNavigation, Href: withToken(href, tok), Type: opds.MIMEAcquisitionFeed},
			},
		})
	}
}

// handleUnreadBooks serves the OPDS 1.x acquisition feed filtered to unread books.
// In multi-user mode, when the request is authenticated as a specific user
// (session cookie or per-user token), the feed is filtered to that user's
// unread books only; otherwise it falls back to the global is_read flag.
func (s *Server) handleUnreadBooks(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	offset, limit := parsePagination(r)
	userID := currentUserID(r)

	books, total, err := s.catalog.Search(catalog.SearchQuery{
		UnreadOnly:   true,
		UserID:       userID,
		Offset:       offset,
		Limit:        limit,
		SortBy:       "added",
		SortOrder:    "desc",
		MaxAgeRating: s.maxAgeRatingForUser(userID),
		SpiceExact:   parseSpiceExactQuery(r),
	})
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := opds.NewAcquisitionFeed(
		"urn:nxt-opds:unread",
		fmt.Sprintf("Unread Books (%d)", total),
	)
	feed.AddLink(opds.RelSelf, withToken("/opds/unread", tok), opds.MIMEAcquisitionFeed)
	feed.AddLink(opds.RelStart, withToken("/opds", tok), opds.MIMENavigationFeed)
	addPaginationLinks(feed, r, offset, limit, total, opds.MIMEAcquisitionFeed)

	for _, bk := range books {
		feed.AddEntry(bookToEntry(bk, tok))
	}

	writeOPDS(w, http.StatusOK, feed)
}

// handleAllBooks serves the acquisition feed with all books.
// Honours ?spice=N (exact) and the child profile MaxAgeRating filter so
// external OPDS clients see the same content the web UI filters down to.
func (s *Server) handleAllBooks(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	offset, limit := parsePagination(r)
	userID := currentUserID(r)

	books, total, err := s.catalog.Search(catalog.SearchQuery{
		Offset:       offset,
		Limit:        limit,
		UserID:       userID,
		SortBy:       "added",
		SortOrder:    "desc",
		MaxAgeRating: s.maxAgeRatingForUser(userID),
		SpiceExact:   parseSpiceExactQuery(r),
	})
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := opds.NewAcquisitionFeed(
		"urn:nxt-opds:all-books",
		fmt.Sprintf("All Books (%d)", total),
	)
	feed.AddLink(opds.RelSelf, withToken("/opds/books", tok), opds.MIMEAcquisitionFeed)
	feed.AddLink(opds.RelStart, withToken("/opds", tok), opds.MIMENavigationFeed)
	addPaginationLinks(feed, r, offset, limit, total, opds.MIMEAcquisitionFeed)

	for _, bk := range books {
		feed.AddEntry(bookToEntry(bk, tok))
	}

	writeOPDS(w, http.StatusOK, feed)
}

// handleBook serves a single book entry.
func (s *Server) handleBook(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	vars := mux.Vars(r)
	id := vars["id"]

	bk, err := s.catalog.BookByID(id)
	if err != nil {
		http.Error(w, "book not found", http.StatusNotFound)
		return
	}

	feed := opds.NewAcquisitionFeed(
		"urn:nxt-opds:book:"+id,
		bk.Title,
	)
	feed.AddLink(opds.RelSelf, withToken("/opds/books/"+id, tok), opds.MIMEAcquisitionFeed)
	feed.AddLink(opds.RelStart, withToken("/opds", tok), opds.MIMENavigationFeed)
	feed.AddEntry(bookToEntry(*bk, tok))

	writeOPDS(w, http.StatusOK, feed)
}

// handleSearch performs a catalog search.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "missing search query parameter 'q'", http.StatusBadRequest)
		return
	}

	offset, limit := parsePagination(r)
	userID := currentUserID(r)

	books, total, err := s.catalog.Search(catalog.SearchQuery{
		Query:        q,
		Offset:       offset,
		Limit:        limit,
		UserID:       userID,
		MaxAgeRating: s.maxAgeRatingForUser(userID),
		SpiceExact:   parseSpiceExactQuery(r),
	})
	if err != nil {
		http.Error(w, "search error", http.StatusInternalServerError)
		return
	}

	feed := opds.NewAcquisitionFeed(
		"urn:nxt-opds:search",
		fmt.Sprintf("Search: %s (%d results)", q, total),
	)
	feed.AddLink(opds.RelSelf, r.URL.RequestURI(), opds.MIMEAcquisitionFeed)
	feed.AddLink(opds.RelStart, withToken("/opds", tok), opds.MIMENavigationFeed)
	addPaginationLinks(feed, r, offset, limit, total, opds.MIMEAcquisitionFeed)

	for _, bk := range books {
		feed.AddEntry(bookToEntry(bk, tok))
	}

	writeOPDS(w, http.StatusOK, feed)
}

// handleAuthors serves the author navigation feed.
func (s *Server) handleAuthors(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	offset, limit := parsePagination(r)

	authors, total, err := s.catalog.Authors(offset, limit)
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := opds.NewNavigationFeed(
		"urn:nxt-opds:authors",
		fmt.Sprintf("Authors (%d)", total),
	)
	feed.AddLink(opds.RelSelf, withToken("/opds/authors", tok), opds.MIMENavigationFeed)
	feed.AddLink(opds.RelStart, withToken("/opds", tok), opds.MIMENavigationFeed)
	addPaginationLinks(feed, r, offset, limit, total, opds.MIMENavigationFeed)

	now := time.Now()
	for _, name := range authors {
		feed.AddEntry(opds.Entry{
			ID:      "urn:nxt-opds:author:" + name,
			Title:   opds.Text{Value: name},
			Updated: opds.AtomDate{Time: now},
			Links: []opds.Link{
				{
					Rel:  opds.RelCatalogNavigation,
					Href: withToken("/opds/authors/"+url.PathEscape(name), tok),
					Type: opds.MIMEAcquisitionFeed,
				},
			},
		})
	}

	writeOPDS(w, http.StatusOK, feed)
}

// handleAuthorBooks serves books filtered by a specific author.
func (s *Server) handleAuthorBooks(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	vars := mux.Vars(r)
	author, _ := url.PathUnescape(vars["author"])
	offset, limit := parsePagination(r)
	userID := currentUserID(r)
	spiceExact := parseSpiceExactQuery(r)
	maxAge := s.maxAgeRatingForUser(userID)

	var (
		books []catalog.Book
		total int
		err   error
	)
	if spiceExact != nil || maxAge > 0 {
		books, total, err = s.catalog.Search(catalog.SearchQuery{
			Author:       author,
			Offset:       offset,
			Limit:        limit,
			UserID:       userID,
			MaxAgeRating: maxAge,
			SpiceExact:   spiceExact,
		})
	} else {
		books, total, err = s.catalog.BooksByAuthor(author, offset, limit)
	}
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := opds.NewAcquisitionFeed(
		"urn:nxt-opds:author:"+author,
		fmt.Sprintf("Books by %s (%d)", author, total),
	)
	feed.AddLink(opds.RelSelf, r.URL.RequestURI(), opds.MIMEAcquisitionFeed)
	feed.AddLink(opds.RelStart, withToken("/opds", tok), opds.MIMENavigationFeed)
	addPaginationLinks(feed, r, offset, limit, total, opds.MIMEAcquisitionFeed)

	for _, bk := range books {
		feed.AddEntry(bookToEntry(bk, tok))
	}

	writeOPDS(w, http.StatusOK, feed)
}

// handleTags serves the tag/genre navigation feed.
func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	offset, limit := parsePagination(r)

	tags, total, err := s.catalog.Tags(offset, limit)
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := opds.NewNavigationFeed(
		"urn:nxt-opds:tags",
		fmt.Sprintf("Genres (%d)", total),
	)
	feed.AddLink(opds.RelSelf, withToken("/opds/tags", tok), opds.MIMENavigationFeed)
	feed.AddLink(opds.RelStart, withToken("/opds", tok), opds.MIMENavigationFeed)
	addPaginationLinks(feed, r, offset, limit, total, opds.MIMENavigationFeed)

	now := time.Now()
	for _, tag := range tags {
		feed.AddEntry(opds.Entry{
			ID:      "urn:nxt-opds:tag:" + tag,
			Title:   opds.Text{Value: tag},
			Updated: opds.AtomDate{Time: now},
			Links: []opds.Link{
				{
					Rel:  opds.RelCatalogNavigation,
					Href: withToken("/opds/tags/"+url.PathEscape(tag), tok),
					Type: opds.MIMEAcquisitionFeed,
				},
			},
		})
	}

	writeOPDS(w, http.StatusOK, feed)
}

// handleTagBooks serves books filtered by a specific tag/genre.
func (s *Server) handleTagBooks(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	vars := mux.Vars(r)
	tag, _ := url.PathUnescape(vars["tag"])
	offset, limit := parsePagination(r)
	userID := currentUserID(r)
	spiceExact := parseSpiceExactQuery(r)
	maxAge := s.maxAgeRatingForUser(userID)

	var (
		books []catalog.Book
		total int
		err   error
	)
	if spiceExact != nil || maxAge > 0 {
		books, total, err = s.catalog.Search(catalog.SearchQuery{
			Tag:          tag,
			Offset:       offset,
			Limit:        limit,
			UserID:       userID,
			MaxAgeRating: maxAge,
			SpiceExact:   spiceExact,
		})
	} else {
		books, total, err = s.catalog.BooksByTag(tag, offset, limit)
	}
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := opds.NewAcquisitionFeed(
		"urn:nxt-opds:tag:"+tag,
		fmt.Sprintf("Genre: %s (%d)", tag, total),
	)
	feed.AddLink(opds.RelSelf, r.URL.RequestURI(), opds.MIMEAcquisitionFeed)
	feed.AddLink(opds.RelStart, withToken("/opds", tok), opds.MIMENavigationFeed)
	addPaginationLinks(feed, r, offset, limit, total, opds.MIMEAcquisitionFeed)

	for _, bk := range books {
		feed.AddEntry(bookToEntry(bk, tok))
	}

	writeOPDS(w, http.StatusOK, feed)
}

// spiceLevels defines the entries shown in the "Niveaux de piment" OPDS
// navigation feeds.  Each entry points to /opds/books?spice=N (or its OPDS v2
// equivalent) so the backend filter applies an EXACT match scoped to 16+/18+
// books — the older "≤ N" semantic was confusing for end users.
var spiceLevels = []struct {
	N        int
	Title    string
	Subtitle string
}{
	{0, "🌶 0 — Sans", "Livres 16+/18+ explicitement notés sans contenu sexuel"},
	{1, "🌶 1 — Suggestif", "Livres 16+/18+ avec contenu suggéré ou allusif"},
	{2, "🌶 2 — Sensuel", "Romance sensuelle, peu de scènes explicites"},
	{3, "🌶 3 — Explicite", "Scènes explicites occasionnelles"},
	{4, "🌶 4 — Récurrent", "Contenu explicite récurrent"},
	{5, "🌶 5 — Érotique", "Très explicite / érotique"},
}

// handleSpiceLevels serves the OPDS 1.x navigation feed listing the spice
// intensity buckets.  Each entry links to /opds/books?spice=N (exact match).
// For child profiles (maxAgeRatingForUser > 0), the feed returns an empty
// list — the spice levels are irrelevant since their age filter already
// strips 16+/18+.
func (s *Server) handleSpiceLevels(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")

	feed := opds.NewNavigationFeed(
		"urn:nxt-opds:spice",
		"Niveaux de piment",
	)
	feed.AddLink(opds.RelSelf, withToken("/opds/spice", tok), opds.MIMENavigationFeed)
	feed.AddLink(opds.RelStart, withToken("/opds", tok), opds.MIMENavigationFeed)

	if s.maxAgeRatingForUser(currentUserID(r)) > 0 {
		writeOPDS(w, http.StatusOK, feed)
		return
	}

	now := time.Now()
	for _, lvl := range spiceLevels {
		href := fmt.Sprintf("/opds/books?spice=%d", lvl.N)
		feed.AddEntry(opds.Entry{
			ID:      fmt.Sprintf("urn:nxt-opds:spice:%d", lvl.N),
			Title:   opds.Text{Value: lvl.Title},
			Updated: opds.AtomDate{Time: now},
			Content: &opds.Content{Type: "text", Value: lvl.Subtitle},
			Links: []opds.Link{
				{Rel: opds.RelCatalogNavigation, Href: withToken(href, tok), Type: opds.MIMEAcquisitionFeed},
			},
		})
	}

	writeOPDS(w, http.StatusOK, feed)
}

// handleOPDS2SpiceLevels is the OPDS 2.0 counterpart of handleSpiceLevels.
func (s *Server) handleOPDS2SpiceLevels(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")

	feed := &opds2.Feed{
		Metadata: opds2.FeedMetadata{Title: "Niveaux de piment"},
		Links: []opds2.Link{
			{Rel: "self", Href: withToken("/opds/v2/spice", tok), Type: opds2.MIMEFeed},
			{Rel: "start", Href: withToken("/opds/v2", tok), Type: opds2.MIMEFeed},
		},
	}

	if s.maxAgeRatingForUser(currentUserID(r)) > 0 {
		writeOPDS2(w, http.StatusOK, feed)
		return
	}

	for _, lvl := range spiceLevels {
		href := fmt.Sprintf("/opds/v2/publications?spice=%d", lvl.N)
		feed.Navigation = append(feed.Navigation, opds2.Link{
			Title: lvl.Title,
			Href:  withToken(href, tok),
			Type:  opds2.MIMEFeed,
		})
	}

	writeOPDS2(w, http.StatusOK, feed)
}

// handlePublishers serves the publisher navigation feed (OPDS 1.x).
func (s *Server) handlePublishers(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	offset, limit := parsePagination(r)

	publishers, total, err := s.catalog.Publishers(offset, limit)
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := opds.NewNavigationFeed(
		"urn:nxt-opds:publishers",
		fmt.Sprintf("Publishers (%d)", total),
	)
	feed.AddLink(opds.RelSelf, withToken("/opds/publishers", tok), opds.MIMENavigationFeed)
	feed.AddLink(opds.RelStart, withToken("/opds", tok), opds.MIMENavigationFeed)
	addPaginationLinks(feed, r, offset, limit, total, opds.MIMENavigationFeed)

	now := time.Now()
	for _, pub := range publishers {
		feed.AddEntry(opds.Entry{
			ID:      "urn:nxt-opds:publisher:" + pub,
			Title:   opds.Text{Value: pub},
			Updated: opds.AtomDate{Time: now},
			Links: []opds.Link{
				{
					Rel:  opds.RelCatalogNavigation,
					Href: withToken("/opds/publishers/"+url.PathEscape(pub), tok),
					Type: opds.MIMEAcquisitionFeed,
				},
			},
		})
	}

	writeOPDS(w, http.StatusOK, feed)
}

// handlePublisherBooks serves books filtered by a specific publisher (OPDS 1.x).
func (s *Server) handlePublisherBooks(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	vars := mux.Vars(r)
	publisher, _ := url.PathUnescape(vars["publisher"])
	offset, limit := parsePagination(r)
	userID := currentUserID(r)
	spiceExact := parseSpiceExactQuery(r)
	maxAge := s.maxAgeRatingForUser(userID)

	var (
		books []catalog.Book
		total int
		err   error
	)
	if spiceExact != nil || maxAge > 0 {
		books, total, err = s.catalog.Search(catalog.SearchQuery{
			Publisher:    publisher,
			Offset:       offset,
			Limit:        limit,
			UserID:       userID,
			MaxAgeRating: maxAge,
			SpiceExact:   spiceExact,
		})
	} else {
		books, total, err = s.catalog.BooksByPublisher(publisher, offset, limit)
	}
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := opds.NewAcquisitionFeed(
		"urn:nxt-opds:publisher:"+publisher,
		fmt.Sprintf("Publisher: %s (%d)", publisher, total),
	)
	feed.AddLink(opds.RelSelf, r.URL.RequestURI(), opds.MIMEAcquisitionFeed)
	feed.AddLink(opds.RelStart, withToken("/opds", tok), opds.MIMENavigationFeed)
	addPaginationLinks(feed, r, offset, limit, total, opds.MIMEAcquisitionFeed)

	for _, bk := range books {
		feed.AddEntry(bookToEntry(bk, tok))
	}

	writeOPDS(w, http.StatusOK, feed)
}

// handleOpenSearch serves the OpenSearch description document.
func (s *Server) handleOpenSearch(w http.ResponseWriter, r *http.Request) {
	type OpenSearchDescription struct {
		XMLName     xml.Name `xml:"OpenSearchDescription"`
		Xmlns       string   `xml:"xmlns,attr"`
		ShortName   string   `xml:"ShortName"`
		Description string   `xml:"Description"`
		URL         struct {
			Type     string `xml:"type,attr"`
			Template string `xml:"template,attr"`
		} `xml:"Url"`
	}

	desc := OpenSearchDescription{
		Xmlns:       "http://a9.com/-/spec/opensearch/1.1/",
		ShortName:   "nxt-opds",
		Description: "Search the nxt-opds catalog",
	}
	desc.URL.Type = opds.MIMEAcquisitionFeed
	desc.URL.Template = "/opds/search?q={searchTerms}"

	data, err := xml.MarshalIndent(desc, "", "  ")
	if err != nil {
		http.Error(w, "opensearch serialization error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", opds.MIMEOpenSearchDesc+"; charset=utf-8")
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(data)
}

// handleHealth serves a simple health-check endpoint.
// handleEPUBFile serves a file from inside an EPUB (ZIP) archive.
// Route: GET /opds/books/{id}/{filepath:.*}
// Some OPDS readers follow links to internal EPUB resources (e.g. META-INF/container.xml).
// This handler opens the EPUB as a ZIP archive and streams the requested inner file.
func (s *Server) handleEPUBFile(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]
	innerPath := vars["filepath"]

	// Prevent path traversal
	if strings.Contains(innerPath, "..") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	bk, err := s.catalog.BookByID(id)
	if err != nil || len(bk.Files) == 0 {
		s.handleOPDSNotFound(w, r)
		return
	}

	// Find the first EPUB file
	var epubPath string
	for _, f := range bk.Files {
		if f.MIMEType == "application/epub+zip" || strings.HasSuffix(strings.ToLower(f.Path), ".epub") {
			epubPath = f.Path
			break
		}
	}
	if epubPath == "" {
		s.handleOPDSNotFound(w, r)
		return
	}

	zr, err := zip.OpenReader(epubPath)
	if err != nil {
		http.Error(w, "cannot open epub", http.StatusInternalServerError)
		return
	}
	defer zr.Close()

	for _, zf := range zr.File {
		if zf.Name == innerPath {
			rc, err := zf.Open()
			if err != nil {
				http.Error(w, "cannot read file", http.StatusInternalServerError)
				return
			}
			defer rc.Close()

			ct := mime.TypeByExtension(filepath.Ext(zf.Name))
			if ct == "" {
				ct = "application/octet-stream"
			}
			w.Header().Set("Content-Type", ct)
			w.WriteHeader(http.StatusOK)
			_, _ = io.Copy(w, rc)
			return
		}
	}

	s.handleOPDSNotFound(w, r)
}

// handleOPDSNotFound handles any unmatched /opds/** paths by returning a
// well-formed XML 404 response so that OPDS clients receive parseable XML
// instead of an HTML error page.
func (s *Server) handleOPDSNotFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(xml.Header + `<error xmlns="http://opds-spec.org/2010/catalog"><message>Not found</message></error>`))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handleSW serves sw.js with the current version injected into CACHE_NAME so
// that each release gets its own cache namespace and old caches are evicted.
func (s *Server) handleSW(w http.ResponseWriter, r *http.Request) {
	if s.opts.StaticFS == nil {
		http.NotFound(w, r)
		return
	}
	f, err := s.opts.StaticFS.Open("sw.js")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	version := s.opts.Version
	if version == "" {
		version = "dev"
	}
	content := strings.ReplaceAll(string(data), "__APP_VERSION__", version)
	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	_, _ = w.Write([]byte(content))
}

// handleAPIPing returns the server startup timestamp in Unix milliseconds.
// Used by the frontend to detect when the server has been restarted.
func (s *Server) handleAPIPing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":        true,
		"startedAt": s.startedAt.UnixMilli(),
	})
}

// handleMCPInfo answers GET /mcp with a small JSON document describing how to
// reach the MCP endpoint.  Without this, a `curl /mcp` would fall through to
// the SPA catch-all and return an HTML 404, leaving operators with no clue
// that the endpoint is alive but only accepts POST.
func (s *Server) handleMCPInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"name":            "nxt-opds MCP",
		"version":         s.opts.Version,
		"protocolVersion": "2024-11-05",
		"transport":       "Streamable HTTP",
		"method":          "POST",
		"endpoint":        "/mcp",
		"auth":            "Authorization: Bearer <opds_token | per-user token>  ou  ?token=<…>",
		"hint":            "Cette URL n'accepte que POST avec une enveloppe JSON-RPC 2.0. Voir la doc README.md.",
	})
}

// bookJSON is the JSON representation of a book for the frontend API.
type bookJSON struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Authors     []string `json:"authors"`
	CoverURL    string   `json:"coverUrl,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Language    string   `json:"language,omitempty"`
	Publisher   string   `json:"publisher,omitempty"`
	Summary     string   `json:"summary,omitempty"`
	Series      string   `json:"series,omitempty"`
	SeriesIndex string   `json:"seriesIndex,omitempty"`
	SeriesTotal string   `json:"seriesTotal,omitempty"`
	Collection      string   `json:"collection,omitempty"`
	CollectionIndex string   `json:"collectionIndex,omitempty"`
	IsRead          bool     `json:"isRead"`       // current user's read status
	ReadColors      []string `json:"readColors"`   // hex colors of all users who have read it
	ReadAt          int64    `json:"readAt,omitempty"` // Unix ms, when the current user marked it as read (0 = unknown / not read)
	Rating          int      `json:"rating"`
	AgeRating       int      `json:"ageRating"`
	SpiceRating     int      `json:"spiceRating"`
	DownloadURL       string `json:"downloadUrl"`
	FileType          string `json:"fileType,omitempty"` // MIME type of the primary file (e.g. "application/epub+zip")
	LastMaintenanceAt int64  `json:"lastMaintenanceAt,omitempty"` // Unix ms, when this book was last indexed
}

// currentUserID returns the user ID stored in the request context (set by authMiddleware
// after validating the session cookie).  Returns empty string when no user is in session.
func currentUserID(r *http.Request) string {
	uid, _ := r.Context().Value(ctxUserID).(string)
	return uid
}

// hasMultipleUsers reports whether the catalog is running in multi-user mode
// (UserManager is wired up AND at least one user is registered).  Single-user
// mode (no UserManager, or zero users) keeps per-user data under userID="".
func (s *Server) hasMultipleUsers() bool {
	if s.userManager == nil {
		return false
	}
	users, err := s.userManager.Users()
	if err != nil {
		return false
	}
	return len(users) > 0
}

// resolveUserForRequest returns the user ID to use for per-user data.
//  1. Session cookie userID (set by authMiddleware).
//  2. ?user= query parameter (for OPDS-token / Basic-Auth clients that have
//     no session cookie).  Validated against the catalog when multi-user.
//  3. Empty string when running in single-user mode.
//
// ok=false means multi-user mode + no userID could be determined, so the
// caller should respond with 401.
func (s *Server) resolveUserForRequest(r *http.Request) (userID string, ok bool) {
	if uid := currentUserID(r); uid != "" {
		return uid, true
	}
	if uid := r.URL.Query().Get("user"); uid != "" {
		if s.userManager != nil {
			if _, err := s.userManager.UserByID(uid); err != nil {
				return "", false
			}
		}
		return uid, true
	}
	if !s.hasMultipleUsers() {
		return "", true
	}
	return "", false
}

// maxAgeRatingForUser returns the MaxAgeRating to apply for the current user.
// Returns 0 (no filter) for non-child users or when multi-user is not available.
// For child users, uses u.MaxAge (falls back to 10 if zero).
func (s *Server) maxAgeRatingForUser(userID string) int {
	if s.userManager == nil || userID == "" {
		return 0
	}
	u, err := s.userManager.UserByID(userID)
	if err != nil || !u.IsChild {
		return 0
	}
	if u.MaxAge > 0 {
		return u.MaxAge
	}
	return 10 // default child restriction
}

// parseSortParam maps the ?sort= query parameter to SortBy and SortOrder values.
// Valid values: "added_desc" (default), "added_asc", "title_asc", "title_desc", "series_index".
func parseSortParam(r *http.Request) (sortBy, sortOrder string) {
	switch r.URL.Query().Get("sort") {
	case "title_asc":
		return "title", "asc"
	case "title_desc":
		return "title", "desc"
	case "added_asc":
		return "added", "asc"
	case "series_index":
		return "series_index", "asc"
	default: // "added_desc" or empty → newest first
		return "added", "desc"
	}
}

// parseSpiceExactQuery reads ?spice=N (exact spice rating match, 0..5) and
// returns a clamped pointer or nil when absent / unparseable.  Shared by
// /api/books and every OPDS feed that builds a SearchQuery so external
// clients can restrict spicy content the same way the web UI does.
func parseSpiceExactQuery(r *http.Request) *int {
	raw := r.URL.Query().Get("spice")
	if raw == "" {
		return nil
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return nil
	}
	if v < 0 {
		v = 0
	}
	if v > 5 {
		v = 5
	}
	return &v
}

// handleAPIBooks serves the full book list as JSON for the web frontend.
// Supports optional ?q= search query, ?series= series filter, ?author= author filter,
// ?tag= tag filter, ?publisher= publisher filter, ?collection= collection filter,
// ?unread=1 filter, ?age_rating= age classification filter,
// ?series_size= (standalone|short|medium|long), ?sort= sort order,
// and standard ?offset=&limit= pagination.
// When ?ids_only=1 is set, returns {"ids":["id1","id2",...]} for all matching books
// (no pagination limit) — used by the frontend to build the full prev/next context.
func (s *Server) handleAPIBooks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	seriesFilter := r.URL.Query().Get("series")
	authorFilter := r.URL.Query().Get("author")
	tagFilter := r.URL.Query().Get("tag")
	publisherFilter := r.URL.Query().Get("publisher")
	collectionFilter := r.URL.Query().Get("collection")
	unreadOnly := r.URL.Query().Get("unread") == "1"
	notIndexed := r.URL.Query().Get("not_indexed") == "1"
	idsOnly := r.URL.Query().Get("ids_only") == "1"
	seriesSize := r.URL.Query().Get("series_size")
	var ageRatingFilters []int
	if raw := r.URL.Query().Get("age_rating"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
				ageRatingFilters = append(ageRatingFilters, v)
			}
		}
	}
	spiceExact := parseSpiceExactQuery(r)
	sortBy, sortOrder := parseSortParam(r)
	userID := currentUserID(r)

	maxAge := s.maxAgeRatingForUser(userID)

	// ids_only mode: return just book IDs for all matching books (no page limit).
	if idsOnly {
		books, _, err := s.catalog.Search(catalog.SearchQuery{
			Query:        q,
			Series:       seriesFilter,
			SeriesSize:   seriesSize,
			Author:       authorFilter,
			Tag:          tagFilter,
			Publisher:    publisherFilter,
			Collection:   collectionFilter,
			Offset:       0,
			Limit:        99999,
			UnreadOnly:   unreadOnly,
			NotIndexed:   notIndexed,
			UserID:       userID,
			SortBy:       sortBy,
			SortOrder:    sortOrder,
			MaxAgeRating: maxAge,
			AgeRatings:   ageRatingFilters,
			SpiceExact:   spiceExact,
		})
		if err != nil {
			http.Error(w, "catalog error", http.StatusInternalServerError)
			return
		}
		ids := make([]string, len(books))
		for i, bk := range books {
			ids[i] = bk.ID
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ids": ids})
		return
	}

	offset, limit := parsePagination(r)

	books, total, err := s.catalog.Search(catalog.SearchQuery{
		Query:        q,
		Series:       seriesFilter,
		SeriesSize:   seriesSize,
		Author:       authorFilter,
		Tag:          tagFilter,
		Publisher:    publisherFilter,
		Collection:   collectionFilter,
		Offset:       offset,
		Limit:        limit,
		UnreadOnly:   unreadOnly,
		NotIndexed:   notIndexed,
		UserID:       userID,
		SortBy:       sortBy,
		SortOrder:    sortOrder,
		MaxAgeRating: maxAge,
		AgeRatings:   ageRatingFilters,
		SpiceExact:   spiceExact,
	})
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	// Build per-user read status and read-color maps (one DB round-trip each).
	bookIDs := make([]string, len(books))
	for i, bk := range books {
		bookIDs[i] = bk.ID
	}
	userReadMap := map[string]bool{}
	colorMap := map[string][]string{}
	if s.userReadManager != nil && len(bookIDs) > 0 {
		userReadMap, _ = s.userReadManager.UserReadStatuses(userID, bookIDs)
		colorMap, _ = s.userReadManager.BookReadColors(bookIDs)
	}

	result := make([]bookJSON, 0, len(books))
	for _, bk := range books {
		isRead := bk.IsRead
		if s.userReadManager != nil {
			isRead = userReadMap[bk.ID]
		}
		colors := colorMap[bk.ID]
		if colors == nil {
			colors = []string{}
		}
		fileType := ""
		if len(bk.Files) > 0 {
			fileType = bk.Files[0].MIMEType
		}
		j := bookJSON{
			ID:              bk.ID,
			Title:           bk.Title,
			CoverURL:        withToken(bk.CoverURL, s.opdsToken),
			Tags:            bk.Tags,
			Language:        bk.Language,
			Publisher:       bk.Publisher,
			Summary:         bk.Summary,
			Series:          bk.Series,
			SeriesIndex:     bk.SeriesIndex,
			SeriesTotal:     bk.SeriesTotal,
			Collection:      bk.Collection,
			CollectionIndex: bk.CollectionIndex,
			IsRead:          isRead,
			ReadColors:      colors,
			Rating:          bk.Rating,
			AgeRating:       bk.AgeRating,
			SpiceRating:     bk.SpiceRating,
			DownloadURL:     "/opds/books/" + bk.ID + "/download",
			FileType:        fileType,
			LastMaintenanceAt: func() int64 {
				if !bk.LastMaintenanceAt.IsZero() {
					return bk.LastMaintenanceAt.UnixMilli()
				}
				return 0
			}(),
		}
		for _, a := range bk.Authors {
			j.Authors = append(j.Authors, a.Name)
		}
		result = append(result, j)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"books": result,
		"total": total,
	})
}

// bookUpdateRequest is the JSON body accepted by PATCH /api/books/{id}.
// All fields are optional; only non-nil fields are applied.
type bookUpdateRequest struct {
	Title       *string  `json:"title"`
	Authors     []string `json:"authors"`
	Tags        []string `json:"tags"`
	Summary     *string  `json:"summary"`
	Publisher   *string  `json:"publisher"`
	Language    *string  `json:"language"`
	Series      *string  `json:"series"`
	SeriesIndex *string  `json:"seriesIndex"`
	SeriesTotal *string  `json:"seriesTotal"`
	Collection      *string `json:"collection"`
	CollectionIndex *string `json:"collectionIndex"`
	IsRead          *bool   `json:"isRead"`
	Rating          *int    `json:"rating"`
	AgeRating       *int    `json:"ageRating"`
	SpiceRating     *int    `json:"spiceRating"`
	// LastMaintenanceAt: Unix ms timestamp; -1 means "now".
	LastMaintenanceAt *int64  `json:"lastMaintenanceAt"`
}

// handleAPIBook handles GET /api/books/{id} to fetch a single book as JSON.
func (s *Server) handleAPIBook(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	bk, err := s.catalog.BookByID(id)
	if err != nil {
		http.Error(w, "book not found", http.StatusNotFound)
		return
	}

	userID := currentUserID(r)
	isRead := bk.IsRead
	colors := []string{}
	var readAtMs int64
	if s.userReadManager != nil {
		rm, _ := s.userReadManager.UserReadStatuses(userID, []string{bk.ID})
		isRead = rm[bk.ID]
		cm, _ := s.userReadManager.BookReadColors([]string{bk.ID})
		if c := cm[bk.ID]; c != nil {
			colors = c
		}
		if isRead && userID != "" {
			if rp, ok := s.userReadManager.(catalog.UserReadAtProvider); ok {
				if t, err := rp.UserReadAt(userID, bk.ID); err == nil && !t.IsZero() {
					readAtMs = t.UnixMilli()
				}
			}
		}
	}

	j := bookJSON{
		ID:              bk.ID,
		Title:           bk.Title,
		CoverURL:        withToken(bk.CoverURL, s.opdsToken),
		Tags:            bk.Tags,
		Language:        bk.Language,
		Publisher:       bk.Publisher,
		Summary:         bk.Summary,
		Series:          bk.Series,
		SeriesIndex:     bk.SeriesIndex,
		SeriesTotal:     bk.SeriesTotal,
		Collection:      bk.Collection,
		CollectionIndex: bk.CollectionIndex,
		IsRead:          isRead,
		ReadColors:      colors,
		ReadAt:          readAtMs,
		Rating:          bk.Rating,
		AgeRating:       bk.AgeRating,
		SpiceRating:     bk.SpiceRating,
		DownloadURL:     "/opds/books/" + bk.ID + "/download",
		FileType: func() string {
			if len(bk.Files) > 0 {
				return bk.Files[0].MIMEType
			}
			return ""
		}(),
		LastMaintenanceAt: func() int64 {
			if !bk.LastMaintenanceAt.IsZero() {
				return bk.LastMaintenanceAt.UnixMilli()
			}
			return 0
		}(),
	}
	for _, a := range bk.Authors {
		j.Authors = append(j.Authors, a.Name)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(j)
}

// handleAPIUpdateBook handles PATCH /api/books/{id} to update book metadata.
func (s *Server) handleAPIUpdateBook(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		http.Error(w, "metadata editing not supported by this backend", http.StatusNotImplemented)
		return
	}

	vars := mux.Vars(r)
	id := vars["id"]

	var req bookUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	// When per-user read management is available, route IsRead through it.
	// Otherwise fall back to the legacy global is_read column.
	updateIsRead := req.IsRead
	userID := currentUserID(r)
	if s.userReadManager != nil && req.IsRead != nil {
		if err := s.userReadManager.SetUserRead(userID, id, *req.IsRead); err != nil {
			http.Error(w, "set read status failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		updateIsRead = nil // don't also write to global is_read
	}

	var maintenanceAt *time.Time
	if req.LastMaintenanceAt != nil {
		var t time.Time
		if *req.LastMaintenanceAt == -1 {
			t = time.Now()
		} else if *req.LastMaintenanceAt > 0 {
			t = time.UnixMilli(*req.LastMaintenanceAt)
		}
		maintenanceAt = &t
	}

	if req.SpiceRating != nil {
		v := *req.SpiceRating
		if v < 0 || v > 5 {
			http.Error(w, "spiceRating must be between 0 and 5", http.StatusBadRequest)
			return
		}
	}

	update := catalog.BookUpdate{
		Title:             req.Title,
		Authors:           req.Authors,
		Tags:              req.Tags,
		Summary:           req.Summary,
		Publisher:         req.Publisher,
		Language:          req.Language,
		Series:            req.Series,
		SeriesIndex:       req.SeriesIndex,
		SeriesTotal:       req.SeriesTotal,
		Collection:        req.Collection,
		CollectionIndex:   req.CollectionIndex,
		IsRead:            updateIsRead,
		Rating:            req.Rating,
		AgeRating:         req.AgeRating,
		SpiceRating:       req.SpiceRating,
		LastMaintenanceAt: maintenanceAt,
	}

	bk, err := s.updater.UpdateBook(id, update)
	if err != nil {
		http.Error(w, "update failed: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}

	s.webhooks.Fire(catalog.WebhookEventBookUpdated, bookEventPayload(bk))

	// Enrich response with per-user read status.
	isRead := bk.IsRead
	colors := []string{}
	var readAtMs int64
	if s.userReadManager != nil {
		rm, _ := s.userReadManager.UserReadStatuses(userID, []string{bk.ID})
		isRead = rm[bk.ID]
		cm, _ := s.userReadManager.BookReadColors([]string{bk.ID})
		if c := cm[bk.ID]; c != nil {
			colors = c
		}
		if isRead && userID != "" {
			if rp, ok := s.userReadManager.(catalog.UserReadAtProvider); ok {
				if t, err := rp.UserReadAt(userID, bk.ID); err == nil && !t.IsZero() {
					readAtMs = t.UnixMilli()
				}
			}
		}
	}

	j := bookJSON{
		ID:              bk.ID,
		Title:           bk.Title,
		CoverURL:        withToken(bk.CoverURL, s.opdsToken),
		Tags:            bk.Tags,
		Language:        bk.Language,
		Publisher:       bk.Publisher,
		Summary:         bk.Summary,
		Series:          bk.Series,
		SeriesIndex:     bk.SeriesIndex,
		SeriesTotal:     bk.SeriesTotal,
		Collection:      bk.Collection,
		CollectionIndex: bk.CollectionIndex,
		IsRead:          isRead,
		ReadColors:      colors,
		ReadAt:          readAtMs,
		Rating:          bk.Rating,
		AgeRating:       bk.AgeRating,
		SpiceRating:     bk.SpiceRating,
		DownloadURL:     "/opds/books/" + bk.ID + "/download",
		FileType:        func() string {
			if len(bk.Files) > 0 {
				return bk.Files[0].MIMEType
			}
			return ""
		}(),
		LastMaintenanceAt: func() int64 {
			if !bk.LastMaintenanceAt.IsZero() {
				return bk.LastMaintenanceAt.UnixMilli()
			}
			return 0
		}(),
	}
	for _, a := range bk.Authors {
		j.Authors = append(j.Authors, a.Name)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(j)
}

// handleAPIDeleteBook handles DELETE /api/books/{id} to remove a book from the catalog.
func (s *Server) handleAPIDeleteBook(w http.ResponseWriter, r *http.Request) {
	if s.deleter == nil {
		http.Error(w, "deletion not supported by this backend", http.StatusNotImplemented)
		return
	}

	vars := mux.Vars(r)
	id := vars["id"]

	// Fetch the book before deleting so the webhook payload can describe it.
	bk, _ := s.catalog.BookByID(id)

	if err := s.deleter.DeleteBook(id); err != nil {
		http.Error(w, "delete failed: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}

	if bk != nil {
		s.webhooks.Fire(catalog.WebhookEventBookDeleted, bookEventPayload(bk))
	} else {
		s.webhooks.Fire(catalog.WebhookEventBookDeleted, map[string]string{"id": id})
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// handleAPIAuthors returns all distinct author names as a JSON array of strings.
func (s *Server) handleAPIAuthors(w http.ResponseWriter, r *http.Request) {
	authors, _, err := s.catalog.Authors(0, 10000)
	if err != nil {
		http.Error(w, "authors query error", http.StatusInternalServerError)
		return
	}
	if authors == nil {
		authors = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(authors)
}

// handleAPITags returns all distinct tag names as a JSON array of strings.
func (s *Server) handleAPITags(w http.ResponseWriter, r *http.Request) {
	tags, _, err := s.catalog.Tags(0, 10000)
	if err != nil {
		http.Error(w, "tags query error", http.StatusInternalServerError)
		return
	}
	if tags == nil {
		tags = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(tags)
}

// handleAPIDeleteTag removes a tag from all books in the catalog.
func (s *Server) handleAPIDeleteTag(w http.ResponseWriter, r *http.Request) {
	tag, _ := url.PathUnescape(mux.Vars(r)["tag"])
	td, ok := s.catalog.(catalog.TagDeleter)
	if !ok {
		http.Error(w, "not supported", http.StatusNotImplemented)
		return
	}
	if err := td.DeleteTag(tag); err != nil {
		http.Error(w, "delete tag error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAPIPublishers returns all distinct publisher names as a JSON array of strings.
func (s *Server) handleAPIPublishers(w http.ResponseWriter, r *http.Request) {
	publishers, _, err := s.catalog.Publishers(0, 10000)
	if err != nil {
		http.Error(w, "publishers query error", http.StatusInternalServerError)
		return
	}
	if publishers == nil {
		publishers = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(publishers)
}

// handleAPISeries returns all distinct series as a JSON array of {name, count} objects.
// Returns 501 if the backend does not support series listing.
func (s *Server) handleAPISeries(w http.ResponseWriter, r *http.Request) {
	if s.seriesLister == nil {
		http.Error(w, "series listing not supported by this backend", http.StatusNotImplemented)
		return
	}
	entries, err := s.seriesLister.Series()
	if err != nil {
		http.Error(w, "series query error", http.StatusInternalServerError)
		return
	}

	type seriesJSON struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	result := make([]seriesJSON, 0, len(entries))
	for _, e := range entries {
		result = append(result, seriesJSON{Name: e.Name, Count: e.Count})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// handleAPICollections returns all distinct editorial collection names as a JSON array of strings.
// Returns 501 if the backend does not support collection listing.
func (s *Server) handleAPICollections(w http.ResponseWriter, r *http.Request) {
	if s.collectionLister == nil {
		http.Error(w, "collection listing not supported by this backend", http.StatusNotImplemented)
		return
	}
	names, err := s.collectionLister.Collections()
	if err != nil {
		http.Error(w, "collections query error", http.StatusInternalServerError)
		return
	}
	if names == nil {
		names = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(names)
}

// handleCover serves the cached cover image for a book by its ID.
// Returns 501 if the backend does not support cover serving.
// Returns 404 if no cover image exists for the given ID.
func (s *Server) handleCover(w http.ResponseWriter, r *http.Request) {
	if s.coverProvider == nil {
		http.Error(w, "cover serving not supported by this backend", http.StatusNotImplemented)
		return
	}

	vars := mux.Vars(r)
	id := vars["id"]

	coverPath, err := s.coverProvider.CoverPath(id)
	if err != nil {
		http.Error(w, "cover not found", http.StatusNotFound)
		return
	}

	f, err := os.Open(coverPath)
	if err != nil {
		http.Error(w, "cover unavailable", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	contentType := mime.TypeByExtension(filepath.Ext(coverPath))
	if contentType == "" {
		contentType = "image/jpeg"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")

	// Use the file's actual mod-time so browsers honour If-Modified-Since
	// after the cover has been replaced by the user.
	stat, _ := f.Stat()
	var modTime time.Time
	if stat != nil {
		modTime = stat.ModTime()
	}
	http.ServeContent(w, r, filepath.Base(coverPath), modTime, f)
}

// maxUploadSize is the maximum file size accepted for upload (100 MiB).
const maxUploadSize = 100 << 20

// handleUpload accepts a multipart/form-data POST with a single file field named "file".
// It stores the file in the catalog and returns the resulting Book as JSON.
// Returns 501 if the backend does not support upload.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if s.uploader == nil {
		http.Error(w, "upload not supported by this backend", http.StatusNotImplemented)
		return
	}

	// Limit request body to prevent memory exhaustion
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "request too large or malformed: "+err.Error(), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing 'file' field in form: "+err.Error(), http.StatusBadRequest)
		return
	}
	// file is an io.ReadCloser; StoreBook will close it
	book, err := s.uploader.StoreBook(header.Filename, file)
	if err != nil {
		http.Error(w, "upload failed: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}

	s.webhooks.Fire(catalog.WebhookEventBookCreated, bookEventPayload(book))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(book)
}

// handleAPIConfig returns public server configuration for the web frontend.
// The response includes the OPDS token (if configured) so that the UI can
// display the OPDS reader URL with the token for easy copy-paste.
// Returns 200 with a JSON object.
func (s *Server) handleAPIConfig(w http.ResponseWriter, r *http.Request) {
	type userJSON struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Color   string `json:"color"`
		IsAdmin bool   `json:"isAdmin"`
		IsChild bool   `json:"isChild"`
		MaxAge  int    `json:"maxAge"`
		Token   string `json:"token,omitempty"`
	}
	type configJSON struct {
		OPDSToken        string    `json:"opdsToken"`
		LibrarianEnabled bool      `json:"librarianEnabled"`
		MultiUser        bool      `json:"multiUser"`
		CurrentUser      *userJSON `json:"currentUser,omitempty"`
		Version          string    `json:"version"`
	}
	// librarianEnabled is true only when an association row actually exists
	// in the catalog backend — the SPA hides the chat box otherwise.
	librarianPaired := false
	if s.librarianAssoc != nil {
		if assoc, err := s.librarianAssoc.Get(); err == nil &&
			assoc != nil && assoc.LibrarianURL != "" {
			librarianPaired = true
		}
	}
	cfg := configJSON{
		OPDSToken:        s.opdsToken,
		LibrarianEnabled: librarianPaired,
		MultiUser:        s.userManager != nil,
		Version:          s.opts.Version,
	}
	if s.userManager != nil {
		uid := currentUserID(r)
		if uid != "" {
			if u, err := s.userManager.UserByID(uid); err == nil {
				cfg.CurrentUser = &userJSON{ID: u.ID, Name: u.Name, Color: u.Color, IsAdmin: u.IsAdmin, IsChild: u.IsChild, MaxAge: u.MaxAge, Token: u.Token}
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(cfg)
}

// handleAPIQR returns a PNG QR code that encodes the OPDS feed URL for the
// requesting user, embedding the appropriate token so a phone or e-reader
// scanning it can pair without typing a password.
//
// Query parameters:
//   - type: "opds" (default) or "mcp" — selects which endpoint to encode.
//   - size: pixel side length, clamped to [128, 1024], default 320.
//   - user_url: when set (admin UI use), encodes that exact URL instead of
//     deriving one from the session.  Must point at the same host as the
//     request, otherwise we ignore it — prevents the endpoint from being
//     used as an arbitrary-string-to-PNG generator for off-host URLs.
//
// Token resolution for the auto-derived URL: if multi-user mode is on and
// the request belongs to a known user, the per-user token is used so
// scanning grants a personalised view (recommendations, to-read pile, unread
// filter).  Otherwise the shared OPDS token is used.  When no token is
// configured AND no user_url is supplied, a 503 is returned.
func (s *Server) handleAPIQR(w http.ResponseWriter, r *http.Request) {
	size := 320
	if v := r.URL.Query().Get("size"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			size = n
		}
	}
	if size < 128 {
		size = 128
	}
	if size > 1024 {
		size = 1024
	}

	var feedURL string
	if explicit := r.URL.Query().Get("user_url"); explicit != "" {
		// Admin-supplied URL must target the same host as the current request
		// to prevent abusing /api/qr as a generic QR encoder for arbitrary
		// strings (an admin can already do many things, but constraining the
		// endpoint to its stated purpose keeps audit logs tractable).
		parsed, err := url.Parse(explicit)
		if err != nil || parsed.Host != "" && parsed.Host != r.Host {
			http.Error(w, "user_url must target the same host as the request", http.StatusBadRequest)
			return
		}
		feedURL = explicit
	} else {
		target := r.URL.Query().Get("type")
		if target == "" {
			target = "opds"
		}
		tok := s.opdsToken
		if s.userManager != nil {
			if uid := currentUserID(r); uid != "" {
				if u, err := s.userManager.UserByID(uid); err == nil && u.Token != "" {
					tok = u.Token
				}
			}
		}
		if tok == "" {
			http.Error(w, "no token configured", http.StatusServiceUnavailable)
			return
		}
		scheme := "http"
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		path := "/opds"
		if target == "mcp" {
			path = "/mcp"
		}
		feedURL = fmt.Sprintf("%s://%s%s?token=%s", scheme, r.Host, path, tok)
	}

	png, err := qrcode.Encode(feedURL, qrcode.Medium, size)
	if err != nil {
		http.Error(w, "qr generation error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store") // tokens rotate; never cache
	_, _ = w.Write(png)
}

// handleAPIMe returns the currently logged-in user's info, including the
// per-user OPDS / MCP token so the frontend can build personalised feed URLs.
func (s *Server) handleAPIMe(w http.ResponseWriter, r *http.Request) {
	if s.userManager == nil {
		http.Error(w, "multi-user not supported", http.StatusNotImplemented)
		return
	}
	uid := currentUserID(r)
	if uid == "" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`null`))
		return
	}
	u, err := s.userManager.UserByID(uid)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"id":      u.ID,
		"name":    u.Name,
		"color":   u.Color,
		"isAdmin": u.IsAdmin,
		"isChild": u.IsChild,
		"maxAge":  u.MaxAge,
		"token":   u.Token,
	})
}

// handleAPIUsers returns all registered users as JSON.
// The per-user token is only included when the requester is an administrator
// (or in single-user / dev mode where currentUserID is empty), so non-admin
// users cannot see other users' tokens.
func (s *Server) handleAPIUsers(w http.ResponseWriter, r *http.Request) {
	if s.userManager == nil {
		http.Error(w, "multi-user not supported", http.StatusNotImplemented)
		return
	}
	users, err := s.userManager.Users()
	if err != nil {
		http.Error(w, "users query error", http.StatusInternalServerError)
		return
	}

	includeTokens := true
	if uid := currentUserID(r); uid != "" {
		if me, err := s.userManager.UserByID(uid); err == nil {
			includeTokens = me.IsAdmin
		} else {
			includeTokens = false
		}
	}

	type userJSON struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Color   string `json:"color"`
		IsAdmin bool   `json:"isAdmin"`
		IsChild bool   `json:"isChild"`
		MaxAge  int    `json:"maxAge"`
		Token   string `json:"token,omitempty"`
	}
	result := make([]userJSON, 0, len(users))
	for _, u := range users {
		row := userJSON{ID: u.ID, Name: u.Name, Color: u.Color, IsAdmin: u.IsAdmin, IsChild: u.IsChild, MaxAge: u.MaxAge}
		if includeTokens {
			row.Token = u.Token
		}
		result = append(result, row)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// handleAPICreateUser creates a new user.
func (s *Server) handleAPICreateUser(w http.ResponseWriter, r *http.Request) {
	if s.userManager == nil {
		http.Error(w, "multi-user not supported", http.StatusNotImplemented)
		return
	}
	var req struct {
		Name    string `json:"name"`
		Color   string `json:"color"`
		IsAdmin bool   `json:"isAdmin"`
		IsChild bool   `json:"isChild"`
		MaxAge  int    `json:"maxAge"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if req.Color == "" {
		req.Color = "#3B82F6"
	}
	u, err := s.userManager.CreateUser(req.Name, req.Color, req.IsAdmin, req.IsChild, req.MaxAge)
	if err != nil {
		http.Error(w, "create user failed: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"id":      u.ID,
		"name":    u.Name,
		"color":   u.Color,
		"isAdmin": u.IsAdmin,
		"isChild": u.IsChild,
		"maxAge":  u.MaxAge,
		"token":   u.Token,
	})
}

// handleAPIUpdateUser updates an existing user's name, color, admin, child status and max age.
func (s *Server) handleAPIUpdateUser(w http.ResponseWriter, r *http.Request) {
	if s.userManager == nil {
		http.Error(w, "multi-user not supported", http.StatusNotImplemented)
		return
	}
	id := mux.Vars(r)["id"]
	var req struct {
		Name    string `json:"name"`
		Color   string `json:"color"`
		IsAdmin bool   `json:"isAdmin"`
		IsChild bool   `json:"isChild"`
		MaxAge  int    `json:"maxAge"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	u, err := s.userManager.UpdateUser(id, req.Name, req.Color, req.IsAdmin, req.IsChild, req.MaxAge)
	if err != nil {
		http.Error(w, "update user failed: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"id":      u.ID,
		"name":    u.Name,
		"color":   u.Color,
		"isAdmin": u.IsAdmin,
		"isChild": u.IsChild,
		"maxAge":  u.MaxAge,
		"token":   u.Token,
	})
}

// handleAPIRegenerateUserToken assigns a fresh per-user token to a user, invalidating
// the previous one.  Admin-only (enforced by the requireAdmin route wrapper).
// POST /api/users/{id}/token
func (s *Server) handleAPIRegenerateUserToken(w http.ResponseWriter, r *http.Request) {
	if s.userManager == nil {
		http.Error(w, "multi-user not supported", http.StatusNotImplemented)
		return
	}
	id := mux.Vars(r)["id"]
	u, err := s.userManager.RegenerateUserToken(id)
	if err != nil {
		http.Error(w, "regenerate token failed: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"id":      u.ID,
		"name":    u.Name,
		"color":   u.Color,
		"isAdmin": u.IsAdmin,
		"isChild": u.IsChild,
		"maxAge":  u.MaxAge,
		"token":   u.Token,
	})
}

// handleAPIDeleteUser removes a user.
func (s *Server) handleAPIDeleteUser(w http.ResponseWriter, r *http.Request) {
	if s.userManager == nil {
		http.Error(w, "multi-user not supported", http.StatusNotImplemented)
		return
	}
	id := mux.Vars(r)["id"]
	if err := s.userManager.DeleteUser(id); err != nil {
		http.Error(w, "delete user failed: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAPIToggleRead sets or clears the read status for the current user via
// PUT /api/books/{id}/read with body {"isRead": true|false}.
func (s *Server) handleAPIToggleRead(w http.ResponseWriter, r *http.Request) {
	if s.userReadManager == nil {
		// Fall back to legacy global is_read via UpdateBook
		if s.updater == nil {
			http.Error(w, "read status not supported by this backend", http.StatusNotImplemented)
			return
		}
		id := mux.Vars(r)["id"]
		var req struct{ IsRead bool `json:"isRead"` }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		bk, err := s.updater.UpdateBook(id, catalog.BookUpdate{IsRead: &req.IsRead})
		if err != nil {
			http.Error(w, "update failed: "+err.Error(), http.StatusUnprocessableEntity)
			return
		}
		s.webhooks.Fire(catalog.WebhookEventBookRead, bookReadEventPayload(bk, nil, bk.IsRead))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":     bk.ID,
			"isRead": bk.IsRead,
		})
		return
	}

	id := mux.Vars(r)["id"]
	userID := currentUserID(r)
	var req struct{ IsRead bool `json:"isRead"` }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := s.userReadManager.SetUserRead(userID, id, req.IsRead); err != nil {
		http.Error(w, "set read status failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Fire book.read webhook with the user's identity attached for routing.
	if bk, _ := s.catalog.BookByID(id); bk != nil {
		var u *catalog.User
		if s.userManager != nil && userID != "" {
			u, _ = s.userManager.UserByID(userID)
		}
		s.webhooks.Fire(catalog.WebhookEventBookRead, bookReadEventPayload(bk, u, req.IsRead))
	}
	colors := []string{}
	if cm, _ := s.userReadManager.BookReadColors([]string{id}); cm != nil {
		if c := cm[id]; c != nil {
			colors = c
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"id":         id,
		"isRead":     req.IsRead,
		"readColors": colors,
	})
}

// handleAPIRecommend creates or replaces a book recommendation from the current user
// to one or more target users.
// POST /api/books/{id}/recommend
// Body: {"toUserID": "uid", "message": "optional message"}
func (s *Server) handleAPIRecommend(w http.ResponseWriter, r *http.Request) {
	if s.recommender == nil {
		http.Error(w, "recommendations not supported", http.StatusNotImplemented)
		return
	}
	bookID := mux.Vars(r)["id"]
	fromUserID := currentUserID(r)
	if fromUserID == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	var req struct {
		ToUserID string `json:"toUserID"`
		Message  string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ToUserID == "" {
		http.Error(w, "invalid request: toUserID required", http.StatusBadRequest)
		return
	}
	if err := s.recommender.RecommendBook(fromUserID, req.ToUserID, bookID, req.Message); err != nil {
		http.Error(w, "recommend failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAPIRemoveRecommend removes a recommendation.
// DELETE /api/books/{id}/recommend/{toUserID}
func (s *Server) handleAPIRemoveRecommend(w http.ResponseWriter, r *http.Request) {
	if s.recommender == nil {
		http.Error(w, "recommendations not supported", http.StatusNotImplemented)
		return
	}
	bookID := mux.Vars(r)["id"]
	toUserID := mux.Vars(r)["toUserID"]
	fromUserID := currentUserID(r)
	if fromUserID == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if err := s.recommender.RemoveRecommendation(fromUserID, toUserID, bookID); err != nil {
		http.Error(w, "remove failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAPIBookRecipients returns the list of users to whom the current user has
// recommended a book.
// GET /api/books/{id}/recipients
func (s *Server) handleAPIBookRecipients(w http.ResponseWriter, r *http.Request) {
	if s.recommender == nil {
		http.Error(w, "recommendations not supported", http.StatusNotImplemented)
		return
	}
	bookID := mux.Vars(r)["id"]
	fromUserID := currentUserID(r)
	if fromUserID == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	ids, err := s.recommender.BookRecipients(fromUserID, bookID)
	if err != nil {
		http.Error(w, "query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if ids == nil {
		ids = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ids)
}

// handleAPIRecommendations returns all recommendations addressed to the current user.
// GET /api/recommendations
func (s *Server) handleAPIRecommendations(w http.ResponseWriter, r *http.Request) {
	if s.recommender == nil {
		http.Error(w, "recommendations not supported", http.StatusNotImplemented)
		return
	}
	userID := currentUserID(r)
	if userID == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	recs, err := s.recommender.RecommendationsForUser(userID)
	if err != nil {
		http.Error(w, "query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	type recJSON struct {
		BookID    string `json:"bookId"`
		BookTitle string `json:"bookTitle"`
		CoverURL  string `json:"coverUrl,omitempty"`
		FromName  string `json:"fromName"`
		FromColor string `json:"fromColor"`
		Message   string `json:"message,omitempty"`
		CreatedAt int64  `json:"createdAt"`
	}
	result := make([]recJSON, 0, len(recs))
	for _, rec := range recs {
		result = append(result, recJSON{
			BookID:    rec.Book.ID,
			BookTitle: rec.Book.Title,
			CoverURL:  withToken(rec.Book.CoverURL, s.opdsToken),
			FromName:  rec.FromUser.Name,
			FromColor: rec.FromUser.Color,
			Message:   rec.Message,
			CreatedAt: rec.CreatedAt.Unix(),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// ─── Wishlist ─────────────────────────────────────────────────────────────────

// wishlistItemJSON is the JSON representation of a WishlistItem.
type wishlistItemJSON struct {
	ID          string `json:"id"`
	UserID      string `json:"userId,omitempty"`
	UserName    string `json:"userName,omitempty"`
	UserColor   string `json:"userColor,omitempty"`
	Title       string `json:"title"`
	Author      string `json:"author,omitempty"`
	ReleaseDate string `json:"releaseDate,omitempty"`
	Notes       string `json:"notes,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
}

func toWishlistItemJSON(it catalog.WishlistItem) wishlistItemJSON {
	return wishlistItemJSON{
		ID:          it.ID,
		UserID:      it.UserID,
		UserName:    it.UserName,
		UserColor:   it.UserColor,
		Title:       it.Title,
		Author:      it.Author,
		ReleaseDate: it.ReleaseDate,
		Notes:       it.Notes,
		CreatedAt:   it.CreatedAt.Unix(),
	}
}

// handleAPIWishlist returns the wishlist for the current user (or all items in single-user mode).
// GET /api/wishlist
// Query params: ?all=1 to retrieve all users' items (default: current user only when multi-user).
func (s *Server) handleAPIWishlist(w http.ResponseWriter, r *http.Request) {
	if s.wishlistManager == nil {
		http.Error(w, "wishlist not supported", http.StatusNotImplemented)
		return
	}
	// Determine which userID to filter on.
	userID := ""
	if r.URL.Query().Get("all") != "1" && s.userManager != nil {
		userID = currentUserID(r)
	}
	items, err := s.wishlistManager.WishlistItems(userID)
	if err != nil {
		http.Error(w, "query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	result := make([]wishlistItemJSON, 0, len(items))
	for _, it := range items {
		result = append(result, toWishlistItemJSON(it))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// handleAPIAddWishlistItem adds a new item to the current user's wishlist.
// POST /api/wishlist
// Body: {"title":"…","author":"…","releaseDate":"…","notes":"…"}
func (s *Server) handleAPIAddWishlistItem(w http.ResponseWriter, r *http.Request) {
	if s.wishlistManager == nil {
		http.Error(w, "wishlist not supported", http.StatusNotImplemented)
		return
	}
	var req struct {
		Title       string `json:"title"`
		Author      string `json:"author"`
		ReleaseDate string `json:"releaseDate"`
		Notes       string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}
	userID := currentUserID(r)
	it, err := s.wishlistManager.AddWishlistItem(userID, req.Title, req.Author, req.ReleaseDate, req.Notes)
	if err != nil {
		http.Error(w, "add failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(toWishlistItemJSON(*it))
}

// handleAPIUpdateWishlistItem updates an existing wishlist item.
// PATCH /api/wishlist/{id}
// Body: {"title":"…","author":"…","releaseDate":"…","notes":"…"}
func (s *Server) handleAPIUpdateWishlistItem(w http.ResponseWriter, r *http.Request) {
	if s.wishlistManager == nil {
		http.Error(w, "wishlist not supported", http.StatusNotImplemented)
		return
	}
	id := mux.Vars(r)["id"]
	var req struct {
		Title       string `json:"title"`
		Author      string `json:"author"`
		ReleaseDate string `json:"releaseDate"`
		Notes       string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	it, err := s.wishlistManager.UpdateWishlistItem(id, req.Title, req.Author, req.ReleaseDate, req.Notes)
	if err != nil {
		http.Error(w, "update failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(toWishlistItemJSON(*it))
}

// handleAPIDeleteWishlistItem removes a wishlist item.
// DELETE /api/wishlist/{id}
func (s *Server) handleAPIDeleteWishlistItem(w http.ResponseWriter, r *http.Request) {
	if s.wishlistManager == nil {
		http.Error(w, "wishlist not supported", http.StatusNotImplemented)
		return
	}
	id := mux.Vars(r)["id"]
	if err := s.wishlistManager.DeleteWishlistItem(id); err != nil {
		http.Error(w, "delete failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── To-Read List ────────────────────────────────────────────────────────────

// toReadItemJSON is the JSON representation of a ToReadItem for the frontend.
type toReadItemJSON struct {
	BookID      string   `json:"bookId"`
	Title       string   `json:"title"`
	Authors     []string `json:"authors,omitempty"`
	CoverURL    string   `json:"coverUrl,omitempty"`
	Series      string   `json:"series,omitempty"`
	SeriesIndex string   `json:"seriesIndex,omitempty"`
	Position    int      `json:"position"`
	AddedAt     int64    `json:"addedAt"`
}

// handleAPIToRead returns the current user's ordered to-read list.
// GET /api/to-read
func (s *Server) handleAPIToRead(w http.ResponseWriter, r *http.Request) {
	if s.toReadManager == nil {
		http.Error(w, "to-read list not supported", http.StatusNotImplemented)
		return
	}
	userID, ok := s.resolveUserForRequest(r)
	if !ok {
		http.Error(w, "user not specified; pass ?user=<id>", http.StatusUnauthorized)
		return
	}
	items, err := s.toReadManager.ToReadList(userID)
	if err != nil {
		http.Error(w, "query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	result := make([]toReadItemJSON, 0, len(items))
	for _, it := range items {
		j := toReadItemJSON{
			BookID:      it.Book.ID,
			Title:       it.Book.Title,
			CoverURL:    withToken(it.Book.CoverURL, s.opdsToken),
			Series:      it.Book.Series,
			SeriesIndex: it.Book.SeriesIndex,
			Position:    it.Position,
			AddedAt:     it.AddedAt.Unix(),
		}
		for _, a := range it.Book.Authors {
			j.Authors = append(j.Authors, a.Name)
		}
		result = append(result, j)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// handleAPIAddToRead adds a book to the current user's to-read list.
// POST /api/to-read   Body: {"bookId":"…"}
func (s *Server) handleAPIAddToRead(w http.ResponseWriter, r *http.Request) {
	if s.toReadManager == nil {
		http.Error(w, "to-read list not supported", http.StatusNotImplemented)
		return
	}
	userID, ok := s.resolveUserForRequest(r)
	if !ok {
		http.Error(w, "user not specified; pass ?user=<id>", http.StatusUnauthorized)
		return
	}
	var req struct {
		BookID string `json:"bookId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.BookID == "" {
		http.Error(w, "invalid request: bookId required", http.StatusBadRequest)
		return
	}
	if err := s.toReadManager.AddToReadList(userID, req.BookID); err != nil {
		http.Error(w, "add failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAPIRemoveToRead removes a book from the current user's to-read list.
// DELETE /api/to-read/{bookId}
func (s *Server) handleAPIRemoveToRead(w http.ResponseWriter, r *http.Request) {
	if s.toReadManager == nil {
		http.Error(w, "to-read list not supported", http.StatusNotImplemented)
		return
	}
	userID, ok := s.resolveUserForRequest(r)
	if !ok {
		http.Error(w, "user not specified; pass ?user=<id>", http.StatusUnauthorized)
		return
	}
	bookID := mux.Vars(r)["bookId"]
	if err := s.toReadManager.RemoveFromToReadList(userID, bookID); err != nil {
		http.Error(w, "remove failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAPIReorderToRead replaces the user's to-read list ordering.
// PUT /api/to-read/reorder   Body: {"bookIds":["id1","id2",…]}
func (s *Server) handleAPIReorderToRead(w http.ResponseWriter, r *http.Request) {
	if s.toReadManager == nil {
		http.Error(w, "to-read list not supported", http.StatusNotImplemented)
		return
	}
	userID, ok := s.resolveUserForRequest(r)
	if !ok {
		http.Error(w, "user not specified; pass ?user=<id>", http.StatusUnauthorized)
		return
	}
	var req struct {
		BookIDs []string `json:"bookIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := s.toReadManager.ReorderToReadList(userID, req.BookIDs); err != nil {
		http.Error(w, "reorder failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleOPDSToRead serves the OPDS 1.x acquisition feed of the current user's
// to-read list, in user-defined order.
// GET /opds/to-read
//
// In multi-user mode, the user must be identified either by session cookie or
// by a ?user=<id> query parameter (used by OPDS reader clients that
// authenticate via the shared OPDS token).
func (s *Server) handleOPDSToRead(w http.ResponseWriter, r *http.Request) {
	if s.toReadManager == nil {
		http.Error(w, "to-read list not supported", http.StatusNotImplemented)
		return
	}
	userID, ok := s.resolveUserForRequest(r)
	if !ok {
		http.Error(w, "user not specified; pass ?user=<id>", http.StatusUnauthorized)
		return
	}
	tok := r.URL.Query().Get("token")
	selfHref := "/opds/to-read"
	if userID != "" && currentUserID(r) == "" {
		selfHref = "/opds/to-read?user=" + url.QueryEscape(userID)
	}
	items, err := s.toReadManager.ToReadList(userID)
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}
	feed := opds.NewAcquisitionFeed(
		"urn:nxt-opds:to-read",
		fmt.Sprintf("Pile à lire (%d)", len(items)),
	)
	feed.Author = &opds.Author{Name: "nxt-opds"}
	feed.AddLink(opds.RelSelf, withToken(selfHref, tok), opds.MIMEAcquisitionFeed)
	feed.AddLink(opds.RelStart, withToken("/opds", tok), opds.MIMENavigationFeed)
	for _, it := range items {
		feed.AddEntry(bookToEntry(it.Book, tok))
	}
	writeOPDS(w, http.StatusOK, feed)
}

// handleOPDS2ToRead serves the OPDS 2.0 acquisition feed of the current user's
// to-read list.
// GET /opds/v2/to-read
//
// In multi-user mode, the user must be identified either by session cookie or
// by a ?user=<id> query parameter (used by OPDS reader clients that
// authenticate via the shared OPDS token).
func (s *Server) handleOPDS2ToRead(w http.ResponseWriter, r *http.Request) {
	if s.toReadManager == nil {
		http.Error(w, "to-read list not supported", http.StatusNotImplemented)
		return
	}
	userID, ok := s.resolveUserForRequest(r)
	if !ok {
		http.Error(w, "user not specified; pass ?user=<id>", http.StatusUnauthorized)
		return
	}
	tok := r.URL.Query().Get("token")
	selfHref := "/opds/v2/to-read"
	if userID != "" && currentUserID(r) == "" {
		selfHref = "/opds/v2/to-read?user=" + url.QueryEscape(userID)
	}
	items, err := s.toReadManager.ToReadList(userID)
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}
	feed := &opds2.Feed{
		Metadata: opds2.FeedMetadata{
			Title:         fmt.Sprintf("Pile à lire (%d)", len(items)),
			NumberOfItems: len(items),
		},
		Links: []opds2.Link{
			{Rel: "self", Href: withToken(selfHref, tok), Type: opds2.MIMEFeed},
			{Rel: "start", Href: withToken("/opds/v2", tok), Type: opds2.MIMEFeed},
		},
	}
	for _, it := range items {
		feed.Publications = append(feed.Publications, bookToPublication(it.Book, tok))
	}
	writeOPDS2(w, http.StatusOK, feed)
}

// ─── OPDS Wishlist / Recommendations ─────────────────────────────────────────

// handleOPDSWishlist serves an OPDS 1.x navigation feed of wishlist items.
// GET /opds/wishlist
// Wishlist items are not real catalog books, so they are exposed as navigation
// entries with the title, author and notes in the content field.
func (s *Server) handleOPDSWishlist(w http.ResponseWriter, r *http.Request) {
	if s.wishlistManager == nil {
		http.Error(w, "wishlist not supported", http.StatusNotImplemented)
		return
	}
	tok := r.URL.Query().Get("token")
	items, err := s.wishlistManager.WishlistItems("") // all users
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := opds.NewNavigationFeed(
		"urn:nxt-opds:wishlist",
		fmt.Sprintf("Liste de souhaits (%d)", len(items)),
	)
	feed.Author = &opds.Author{Name: "nxt-opds"}
	feed.AddLink(opds.RelSelf, withToken("/opds/wishlist", tok), opds.MIMENavigationFeed)
	feed.AddLink(opds.RelStart, withToken("/opds", tok), opds.MIMENavigationFeed)

	for _, it := range items {
		content := it.Author
		if it.ReleaseDate != "" {
			if content != "" {
				content += " – "
			}
			content += it.ReleaseDate
		}
		if it.Notes != "" {
			if content != "" {
				content += " – "
			}
			content += it.Notes
		}
		if it.UserName != "" {
			content += " (souhaité par " + it.UserName + ")"
		}
		feed.AddEntry(opds.Entry{
			ID:      "urn:nxt-opds:wishlist:" + it.ID,
			Title:   opds.Text{Value: it.Title},
			Updated: opds.AtomDate{Time: it.CreatedAt},
			Content: &opds.Content{Type: "text", Value: content},
			Links:   []opds.Link{{Rel: opds.RelCatalogNavigation, Href: withToken("/opds/wishlist", tok), Type: opds.MIMENavigationFeed}},
		})
	}
	writeOPDS(w, http.StatusOK, feed)
}

// handleOPDSRecommendations serves an OPDS 1.x acquisition feed of recommended
// books.  When the request is authenticated as a specific user (session
// cookie or per-user token), only that user's incoming recommendations are
// returned; otherwise (single-user mode or shared token) all recommendations
// across users are returned, deduplicated by book ID.
// GET /opds/recommendations
func (s *Server) handleOPDSRecommendations(w http.ResponseWriter, r *http.Request) {
	if s.recommender == nil {
		http.Error(w, "recommendations not supported", http.StatusNotImplemented)
		return
	}
	if s.userManager == nil {
		http.Error(w, "multi-user not supported", http.StatusNotImplemented)
		return
	}
	tok := r.URL.Query().Get("token")
	uid := currentUserID(r)

	books, err := s.recommendedBooks(uid)
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := opds.NewAcquisitionFeed(
		"urn:nxt-opds:recommendations",
		fmt.Sprintf("Recommandations (%d)", len(books)),
	)
	feed.Author = &opds.Author{Name: "nxt-opds"}
	feed.AddLink(opds.RelSelf, withToken("/opds/recommendations", tok), opds.MIMEAcquisitionFeed)
	feed.AddLink(opds.RelStart, withToken("/opds", tok), opds.MIMENavigationFeed)
	for _, bk := range books {
		feed.AddEntry(bookToEntry(bk, tok))
	}
	writeOPDS(w, http.StatusOK, feed)
}

// recommendedBooks returns the deduplicated list of recommended books visible
// to the given user.  When uid is non-empty, only recommendations addressed
// to that user are returned; when empty, every user's recommendations are
// merged via a single AllRecommendations() call when the backend supports
// catalog.AllRecommendationsLister, falling back to a per-user loop otherwise.
// Order follows the underlying query's "newest first".
func (s *Server) recommendedBooks(uid string) ([]catalog.Book, error) {
	seen := map[string]bool{}
	var books []catalog.Book
	if uid != "" {
		recs, err := s.recommender.RecommendationsForUser(uid)
		if err != nil {
			return nil, err
		}
		for _, rec := range recs {
			if seen[rec.Book.ID] {
				continue
			}
			seen[rec.Book.ID] = true
			books = append(books, rec.Book)
		}
		return books, nil
	}
	if all, ok := s.recommender.(catalog.AllRecommendationsLister); ok {
		recs, err := all.AllRecommendations()
		if err != nil {
			return nil, err
		}
		for _, rec := range recs {
			if seen[rec.Book.ID] {
				continue
			}
			seen[rec.Book.ID] = true
			books = append(books, rec.Book)
		}
		return books, nil
	}
	// Fallback: per-user fan-out for backends that don't yet expose the
	// AllRecommendationsLister optimisation.
	users, err := s.userManager.Users()
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		recs, err := s.recommender.RecommendationsForUser(u.ID)
		if err != nil {
			continue
		}
		for _, rec := range recs {
			if seen[rec.Book.ID] {
				continue
			}
			seen[rec.Book.ID] = true
			books = append(books, rec.Book)
		}
	}
	return books, nil
}

// handleOPDS2Wishlist serves an OPDS 2.0 navigation feed of wishlist items.
// GET /opds/v2/wishlist
func (s *Server) handleOPDS2Wishlist(w http.ResponseWriter, r *http.Request) {
	if s.wishlistManager == nil {
		http.Error(w, "wishlist not supported", http.StatusNotImplemented)
		return
	}
	tok := r.URL.Query().Get("token")
	items, err := s.wishlistManager.WishlistItems("") // all users
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := &opds2.Feed{
		Metadata: opds2.FeedMetadata{
			Title:         fmt.Sprintf("Liste de souhaits (%d)", len(items)),
			NumberOfItems: len(items),
		},
		Links: []opds2.Link{
			{Rel: "self", Href: withToken("/opds/v2/wishlist", tok), Type: opds2.MIMEFeed},
			{Rel: "start", Href: withToken("/opds/v2", tok), Type: opds2.MIMEFeed},
		},
	}

	for _, it := range items {
		feed.Navigation = append(feed.Navigation, opds2.Link{
			Title: it.Title,
			Href:  withToken("/opds/v2/wishlist", tok),
			Type:  opds2.MIMEFeed,
		})
	}
	writeOPDS2(w, http.StatusOK, feed)
}

// handleOPDS2Recommendations serves an OPDS 2.0 acquisition feed of recommended
// books.  When the request is authenticated as a specific user (session
// cookie or per-user token), only that user's incoming recommendations are
// returned; otherwise (single-user mode or shared token) all users'
// recommendations are merged, deduplicated by book ID.
// GET /opds/v2/recommendations
func (s *Server) handleOPDS2Recommendations(w http.ResponseWriter, r *http.Request) {
	if s.recommender == nil {
		http.Error(w, "recommendations not supported", http.StatusNotImplemented)
		return
	}
	if s.userManager == nil {
		http.Error(w, "multi-user not supported", http.StatusNotImplemented)
		return
	}
	tok := r.URL.Query().Get("token")
	uid := currentUserID(r)

	books, err := s.recommendedBooks(uid)
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := &opds2.Feed{
		Metadata: opds2.FeedMetadata{
			Title:         fmt.Sprintf("Recommandations (%d)", len(books)),
			NumberOfItems: len(books),
		},
		Links: []opds2.Link{
			{Rel: "self", Href: withToken("/opds/v2/recommendations", tok), Type: opds2.MIMEFeed},
			{Rel: "start", Href: withToken("/opds/v2", tok), Type: opds2.MIMEFeed},
		},
	}
	for _, bk := range books {
		feed.Publications = append(feed.Publications, bookToPublication(bk, tok))
	}
	writeOPDS2(w, http.StatusOK, feed)
}

// handleAPIRefresh triggers an on-demand catalog refresh.
// Returns 501 if the backend does not support refresh.
// Returns 200 {"ok":true} on success, 500 on backend error.
func (s *Server) handleAPIRefresh(w http.ResponseWriter, r *http.Request) {
	if s.refresher == nil {
		http.Error(w, "refresh not supported by this backend", http.StatusNotImplemented)
		return
	}
	if err := s.refresher.Refresh(); err != nil {
		http.Error(w, "refresh failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// handleAPIUpdateCover replaces the cover image for a book with the uploaded file.
// Accepts a multipart/form-data POST with a field named "cover".
// Returns 501 if the backend does not support cover updates.
// Returns 200 {"ok":true} on success.
func (s *Server) handleAPIUpdateCover(w http.ResponseWriter, r *http.Request) {
	if s.coverUpdater == nil {
		http.Error(w, "cover update not supported by this backend", http.StatusNotImplemented)
		return
	}

	id := mux.Vars(r)["id"]

	// Limit to 20 MB for cover images.
	if err := r.ParseMultipartForm(20 << 20); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("cover")
	if err != nil {
		http.Error(w, "missing cover field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Determine extension from Content-Type header, then fall back to filename.
	ext := imageExtFromMIME(header.Header.Get("Content-Type"))
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(header.Filename))
	}
	if ext == "" {
		ext = ".jpg"
	}

	if err := s.coverUpdater.UpdateCover(id, io.NopCloser(file), ext); err != nil {
		http.Error(w, "update cover: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// withToken appends the OPDS authentication token to a feed URL so that
// OPDS reader clients can follow sub-feed links without getting 401 errors.
// If tok is empty, href is returned unchanged.
func withToken(href, tok string) string {
	if tok == "" {
		return href
	}
	if strings.Contains(href, "?") {
		return href + "&token=" + url.QueryEscape(tok)
	}
	return href + "?token=" + url.QueryEscape(tok)
}

// imageExtFromMIME returns the file extension for common image MIME types.
func imageExtFromMIME(mimeType string) string {
	switch strings.ToLower(strings.SplitN(mimeType, ";", 2)[0]) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ""
	}
}

// handleDownload serves the raw file for a book's acquisition link.
// Query param "path" is the filesystem path stored in the catalog File entry.
// Only files inside the catalog root are served (path traversal prevention).
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	bk, err := s.catalog.BookByID(id)
	if err != nil {
		http.Error(w, "book not found", http.StatusNotFound)
		return
	}

	reqPath, _ := url.QueryUnescape(r.URL.Query().Get("path"))
	if reqPath == "" {
		// Default to the first file
		if len(bk.Files) == 0 {
			http.Error(w, "no files available for this book", http.StatusNotFound)
			return
		}
		reqPath = bk.Files[0].Path
	}

	// Verify the requested path belongs to one of the book's known files
	var matched *catalog.File
	for i := range bk.Files {
		if bk.Files[i].Path == reqPath {
			matched = &bk.Files[i]
			break
		}
	}
	if matched == nil {
		http.Error(w, "file not found for this book", http.StatusNotFound)
		return
	}

	f, err := os.Open(matched.Path)
	if err != nil {
		http.Error(w, "file unavailable", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	contentType := matched.MIMEType
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(matched.Path))
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+filepath.Base(matched.Path)+`"`)

	http.ServeContent(w, r, filepath.Base(matched.Path), time.Time{}, f)
}

// writeOPDS2 serializes an OPDS 2.0 feed to JSON and writes it to the response.
func writeOPDS2(w http.ResponseWriter, status int, feed *opds2.Feed) {
	w.Header().Set("Content-Type", opds2.MIMEFeed+"; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(feed)
}

// bookToPublication converts a catalog.Book to an opds2.Publication.
// tok is the OPDS authentication token to append to all URLs (may be empty).
func bookToPublication(b catalog.Book, tok string) opds2.Publication {
	pub := opds2.Publication{
		Metadata: opds2.PubMetadata{
			Type:        "http://schema.org/Book",
			Title:       b.Title,
			Identifier:  "urn:nxt-opds:book:" + b.ID,
			Publisher:   b.Publisher,
			Description: b.Summary,
		},
	}

	if b.Language != "" {
		pub.Metadata.Language = b.Language
	}

	if !b.PublishedAt.IsZero() {
		pub.Metadata.Published = b.PublishedAt.UTC().Format(time.RFC3339)
	}
	if !b.UpdatedAt.IsZero() {
		pub.Metadata.Modified = b.UpdatedAt.UTC().Format(time.RFC3339)
	}

	// Authors
	switch len(b.Authors) {
	case 0:
		// no author
	case 1:
		pub.Metadata.Author = opds2.Contributor{Name: b.Authors[0].Name, URL: b.Authors[0].URI}
	default:
		contributors := make([]opds2.Contributor, len(b.Authors))
		for i, a := range b.Authors {
			contributors[i] = opds2.Contributor{Name: a.Name, URL: a.URI}
		}
		pub.Metadata.Author = contributors
	}

	// Tags/subjects
	for _, tag := range b.Tags {
		pub.Metadata.Subject = append(pub.Metadata.Subject, opds2.Subject{Name: tag})
	}

	// Series
	if b.Series != "" {
		pos := 0.0
		if b.SeriesIndex != "" {
			if f, err := strconv.ParseFloat(b.SeriesIndex, 64); err == nil {
				pos = f
			}
		}
		pub.Metadata.BelongsTo = &opds2.BelongsTo{
			Series: []opds2.Series{{Name: b.Series, Position: pos}},
		}
	}

	// Acquisition links
	for _, f := range b.Files {
		pub.Links = append(pub.Links, opds2.Link{
			Rel:  "http://opds-spec.org/acquisition",
			Href: withToken("/opds/books/"+b.ID+"/download?path="+url.QueryEscape(f.Path), tok),
			Type: f.MIMEType,
		})
	}

	// Cover / thumbnail
	if b.CoverURL != "" {
		pub.Images = append(pub.Images, opds2.Link{
			Rel:  "http://opds-spec.org/image",
			Href: withToken(b.CoverURL, tok),
			Type: "image/jpeg",
		})
	}
	if b.ThumbnailURL != "" {
		pub.Images = append(pub.Images, opds2.Link{
			Rel:  "http://opds-spec.org/image/thumbnail",
			Href: withToken(b.ThumbnailURL, tok),
			Type: "image/jpeg",
		})
	}

	return pub
}

// ageFacetLabel returns the human-readable label for an age_rating bucket.
func ageFacetLabel(v int) string {
	if v == 0 {
		return "Non classifié"
	}
	return fmt.Sprintf("%d+", v)
}

// spiceFacetLabel returns the human-readable label for a spice bucket.
func spiceFacetLabel(v int) string {
	for _, lvl := range spiceLevels {
		if lvl.N == v {
			return lvl.Title
		}
	}
	return ""
}

// buildFacetURL composes a facet link URL: takes the base path and current
// query parameters, replaces (or sets) the named facet param, and ensures the
// token survives.  Pagination params (offset/limit) are stripped so the
// caller restarts at page 1 after picking a facet.
func buildFacetURL(basePath string, cur url.Values, param, value, tok string) string {
	out := url.Values{}
	for k, vs := range cur {
		if k == "offset" || k == "limit" || k == "token" || k == param {
			continue
		}
		for _, v := range vs {
			out.Add(k, v)
		}
	}
	out.Set(param, value)
	if tok != "" {
		out.Set("token", tok)
	}
	return basePath + "?" + out.Encode()
}

// faceter is implemented by catalog backends that can compute facet counts.
type faceter interface {
	Facets(q catalog.SearchQuery) (catalog.FacetCounts, error)
}

// buildFacetGroups produces the OPDS 2.0 "facets" section for an acquisition
// feed.  When the catalog backend does not implement Facets, returns nil so
// the caller's Feed.Facets stays empty (and the field is omitted from JSON).
//
// Age facet: shown when at least one bucket has results.  Buckets above the
// child profile's MaxAgeRating are skipped automatically.
// Spice facet: hidden when MaxAgeRating > 0 (child profile) or when no
// matching book is 16+/18+.  Each bucket count is computed by the catalog
// already scoped to age >= 16.
func (s *Server) buildFacetGroups(r *http.Request, q catalog.SearchQuery, basePath, tok string) []opds2.FacetGroup {
	f, ok := s.catalog.(faceter)
	if !ok {
		return nil
	}
	counts, err := f.Facets(q)
	if err != nil {
		return nil
	}

	var groups []opds2.FacetGroup
	cur := r.URL.Query()

	if len(counts.AgeRating) > 0 {
		var links []opds2.Link
		for _, v := range []int{0, 3, 6, 10, 12, 16, 18} {
			n := counts.AgeRating[v]
			if n == 0 {
				continue
			}
			if q.MaxAgeRating > 0 && v > q.MaxAgeRating {
				continue
			}
			links = append(links, opds2.Link{
				Title:      ageFacetLabel(v),
				Href:       buildFacetURL(basePath, cur, "age_rating", strconv.Itoa(v), tok),
				Type:       opds2.MIMEFeed,
				Properties: &opds2.LinkProperties{NumberOfItems: n},
			})
		}
		if len(links) > 0 {
			groups = append(groups, opds2.FacetGroup{
				Metadata: opds2.FacetMetadata{Title: "Classification d'âge"},
				Links:    links,
			})
		}
	}

	if q.MaxAgeRating == 0 && len(counts.Spice) > 0 {
		var links []opds2.Link
		for v := 0; v <= 5; v++ {
			n := counts.Spice[v]
			if n == 0 {
				continue
			}
			links = append(links, opds2.Link{
				Title:      spiceFacetLabel(v),
				Href:       buildFacetURL(basePath, cur, "spice", strconv.Itoa(v), tok),
				Type:       opds2.MIMEFeed,
				Properties: &opds2.LinkProperties{NumberOfItems: n},
			})
		}
		if len(links) > 0 {
			groups = append(groups, opds2.FacetGroup{
				Metadata: opds2.FacetMetadata{Title: "Piment"},
				Links:    links,
			})
		}
	}

	return groups
}

// addPaginationLinks2 appends OPDS 2.0 pagination links to a feed.
func addPaginationLinks2(feed *opds2.Feed, r *http.Request, offset, limit, total int) {
	if total <= 0 || limit <= 0 {
		return
	}
	lastOffset := ((total - 1) / limit) * limit
	feed.Links = append(feed.Links, opds2.Link{Rel: "first", Href: paginationLink(r, 0, limit), Type: opds2.MIMEFeed})
	if offset > 0 {
		prevOffset := offset - limit
		if prevOffset < 0 {
			prevOffset = 0
		}
		feed.Links = append(feed.Links, opds2.Link{Rel: "previous", Href: paginationLink(r, prevOffset, limit), Type: opds2.MIMEFeed})
	}
	if offset+limit < total {
		feed.Links = append(feed.Links, opds2.Link{Rel: "next", Href: paginationLink(r, offset+limit, limit), Type: opds2.MIMEFeed})
	}
	feed.Links = append(feed.Links, opds2.Link{Rel: "last", Href: paginationLink(r, lastOffset, limit), Type: opds2.MIMEFeed})
}

// handleOPDS2Root serves the OPDS 2.0 root navigation feed.
func (s *Server) handleOPDS2Root(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	feed := &opds2.Feed{
		Metadata: opds2.FeedMetadata{Title: "nxt-opds Catalog"},
		Links: []opds2.Link{
			{Rel: "self", Href: withToken("/opds/v2", tok), Type: opds2.MIMEFeed},
			{Rel: "start", Href: withToken("/opds/v2", tok), Type: opds2.MIMEFeed},
			{Rel: "search", Href: "/opds/v2/search{?q}", Type: opds2.MIMEFeed, Templated: true},
		},
		Navigation: []opds2.Link{
			{Title: "Tous les livres", Href: withToken("/opds/v2/publications", tok), Type: opds2.MIMEFeed},
			{Title: "Par auteur", Href: withToken("/opds/v2/authors", tok), Type: opds2.MIMEFeed},
			{Title: "Par genre", Href: withToken("/opds/v2/tags", tok), Type: opds2.MIMEFeed},
			{Title: "Par éditeur", Href: withToken("/opds/v2/publishers", tok), Type: opds2.MIMEFeed},
			{Title: "Non lus", Href: withToken("/opds/v2/unread", tok), Type: opds2.MIMEFeed},
		},
	}
	if s.maxAgeRatingForUser(currentUserID(r)) == 0 {
		feed.Navigation = append(feed.Navigation, opds2.Link{
			Title: "Niveaux de piment",
			Href:  withToken("/opds/v2/spice", tok),
			Type:  opds2.MIMEFeed,
		})
	}
	if s.wishlistManager != nil {
		feed.Navigation = append(feed.Navigation, opds2.Link{
			Title: "Liste de souhaits",
			Href:  withToken("/opds/v2/wishlist", tok),
			Type:  opds2.MIMEFeed,
		})
	}
	if s.recommender != nil && s.userManager != nil {
		feed.Navigation = append(feed.Navigation, opds2.Link{
			Title: "Recommandations",
			Href:  withToken("/opds/v2/recommendations", tok),
			Type:  opds2.MIMEFeed,
		})
	}
	if s.toReadManager != nil {
		s.appendToReadV2NavItems(feed, r, tok)
	}
	writeOPDS2(w, http.StatusOK, feed)
}

// appendToReadV2NavItems is the OPDS v2 counterpart to appendToReadV1Entries.
func (s *Server) appendToReadV2NavItems(feed *opds2.Feed, r *http.Request, tok string) {
	if currentUserID(r) != "" || !s.hasMultipleUsers() {
		feed.Navigation = append(feed.Navigation, opds2.Link{
			Title: "Pile à lire",
			Href:  withToken("/opds/v2/to-read", tok),
			Type:  opds2.MIMEFeed,
		})
		return
	}
	users, err := s.userManager.Users()
	if err != nil {
		return
	}
	for _, u := range users {
		href := "/opds/v2/to-read?user=" + url.QueryEscape(u.ID)
		feed.Navigation = append(feed.Navigation, opds2.Link{
			Title: "Pile à lire de " + u.Name,
			Href:  withToken(href, tok),
			Type:  opds2.MIMEFeed,
		})
	}
}

// handleOPDS2Unread serves the OPDS 2.0 acquisition feed filtered to unread books.
// In multi-user mode, when the request is authenticated as a specific user
// (session cookie or per-user token), the feed is filtered to that user's
// unread books only; otherwise it falls back to the global is_read flag.
func (s *Server) handleOPDS2Unread(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	offset, limit := parsePagination(r)
	userID := currentUserID(r)

	q := catalog.SearchQuery{
		UnreadOnly:   true,
		UserID:       userID,
		Offset:       offset,
		Limit:        limit,
		SortBy:       "added",
		SortOrder:    "desc",
		MaxAgeRating: s.maxAgeRatingForUser(userID),
		SpiceExact:   parseSpiceExactQuery(r),
	}
	books, total, err := s.catalog.Search(q)
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := &opds2.Feed{
		Metadata: opds2.FeedMetadata{
			Title:         fmt.Sprintf("Non lus (%d)", total),
			NumberOfItems: total,
		},
		Links: []opds2.Link{
			{Rel: "self", Href: withToken("/opds/v2/unread", tok), Type: opds2.MIMEFeed},
			{Rel: "start", Href: withToken("/opds/v2", tok), Type: opds2.MIMEFeed},
		},
	}
	addPaginationLinks2(feed, r, offset, limit, total)
	feed.Facets = s.buildFacetGroups(r, q, "/opds/v2/unread", tok)

	for _, bk := range books {
		feed.Publications = append(feed.Publications, bookToPublication(bk, tok))
	}

	writeOPDS2(w, http.StatusOK, feed)
}

// handleOPDS2Publications serves the OPDS 2.0 acquisition feed with all books.
// Honours ?spice=N (exact) and the child profile MaxAgeRating filter.
func (s *Server) handleOPDS2Publications(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	offset, limit := parsePagination(r)
	userID := currentUserID(r)

	q := catalog.SearchQuery{
		Offset:       offset,
		Limit:        limit,
		UserID:       userID,
		SortBy:       "added",
		SortOrder:    "desc",
		MaxAgeRating: s.maxAgeRatingForUser(userID),
		SpiceExact:   parseSpiceExactQuery(r),
	}
	// Honour ?age_rating=N (single value) on the publications feed.
	if raw := r.URL.Query().Get("age_rating"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
				q.AgeRatings = append(q.AgeRatings, v)
			}
		}
	}
	books, total, err := s.catalog.Search(q)
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := &opds2.Feed{
		Metadata: opds2.FeedMetadata{
			Title:         fmt.Sprintf("Tous les livres (%d)", total),
			NumberOfItems: total,
		},
		Links: []opds2.Link{
			{Rel: "self", Href: withToken("/opds/v2/publications", tok), Type: opds2.MIMEFeed},
			{Rel: "start", Href: withToken("/opds/v2", tok), Type: opds2.MIMEFeed},
		},
	}
	addPaginationLinks2(feed, r, offset, limit, total)
	feed.Facets = s.buildFacetGroups(r, q, "/opds/v2/publications", tok)

	for _, bk := range books {
		feed.Publications = append(feed.Publications, bookToPublication(bk, tok))
	}

	writeOPDS2(w, http.StatusOK, feed)
}

// handleOPDS2Search performs a catalog search and returns an OPDS 2.0 feed.
func (s *Server) handleOPDS2Search(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "missing search query parameter 'q'", http.StatusBadRequest)
		return
	}

	offset, limit := parsePagination(r)
	userID := currentUserID(r)

	books, total, err := s.catalog.Search(catalog.SearchQuery{
		Query:        q,
		Offset:       offset,
		Limit:        limit,
		UserID:       userID,
		MaxAgeRating: s.maxAgeRatingForUser(userID),
		SpiceExact:   parseSpiceExactQuery(r),
	})
	if err != nil {
		http.Error(w, "search error", http.StatusInternalServerError)
		return
	}

	feed := &opds2.Feed{
		Metadata: opds2.FeedMetadata{
			Title:         fmt.Sprintf("Recherche : %s (%d résultats)", q, total),
			NumberOfItems: total,
		},
		Links: []opds2.Link{
			{Rel: "self", Href: r.URL.RequestURI(), Type: opds2.MIMEFeed},
			{Rel: "start", Href: withToken("/opds/v2", tok), Type: opds2.MIMEFeed},
		},
	}
	addPaginationLinks2(feed, r, offset, limit, total)

	for _, bk := range books {
		feed.Publications = append(feed.Publications, bookToPublication(bk, tok))
	}

	writeOPDS2(w, http.StatusOK, feed)
}

// handleOPDS2Authors serves the OPDS 2.0 author navigation feed.
func (s *Server) handleOPDS2Authors(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	offset, limit := parsePagination(r)

	authors, total, err := s.catalog.Authors(offset, limit)
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := &opds2.Feed{
		Metadata: opds2.FeedMetadata{
			Title:         fmt.Sprintf("Auteurs (%d)", total),
			NumberOfItems: total,
		},
		Links: []opds2.Link{
			{Rel: "self", Href: withToken("/opds/v2/authors", tok), Type: opds2.MIMEFeed},
			{Rel: "start", Href: withToken("/opds/v2", tok), Type: opds2.MIMEFeed},
		},
	}
	addPaginationLinks2(feed, r, offset, limit, total)

	for _, name := range authors {
		feed.Navigation = append(feed.Navigation, opds2.Link{
			Title: name,
			Href:  withToken("/opds/v2/authors/"+url.PathEscape(name), tok),
			Type:  opds2.MIMEFeed,
		})
	}

	writeOPDS2(w, http.StatusOK, feed)
}

// handleOPDS2AuthorBooks serves an OPDS 2.0 acquisition feed for a specific author.
func (s *Server) handleOPDS2AuthorBooks(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	vars := mux.Vars(r)
	author, _ := url.PathUnescape(vars["author"])
	offset, limit := parsePagination(r)
	userID := currentUserID(r)
	spiceExact := parseSpiceExactQuery(r)
	maxAge := s.maxAgeRatingForUser(userID)

	q := catalog.SearchQuery{
		Author:       author,
		Offset:       offset,
		Limit:        limit,
		UserID:       userID,
		MaxAgeRating: maxAge,
		SpiceExact:   spiceExact,
	}
	books, total, err := s.catalog.Search(q)
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := &opds2.Feed{
		Metadata: opds2.FeedMetadata{
			Title:         fmt.Sprintf("Livres de %s (%d)", author, total),
			NumberOfItems: total,
		},
		Links: []opds2.Link{
			{Rel: "self", Href: r.URL.RequestURI(), Type: opds2.MIMEFeed},
			{Rel: "start", Href: withToken("/opds/v2", tok), Type: opds2.MIMEFeed},
		},
	}
	addPaginationLinks2(feed, r, offset, limit, total)
	feed.Facets = s.buildFacetGroups(r, q, "/opds/v2/authors/"+url.PathEscape(author), tok)

	for _, bk := range books {
		feed.Publications = append(feed.Publications, bookToPublication(bk, tok))
	}

	writeOPDS2(w, http.StatusOK, feed)
}

// handleOPDS2Tags serves the OPDS 2.0 tag/genre navigation feed.
func (s *Server) handleOPDS2Tags(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	offset, limit := parsePagination(r)

	tags, total, err := s.catalog.Tags(offset, limit)
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := &opds2.Feed{
		Metadata: opds2.FeedMetadata{
			Title:         fmt.Sprintf("Genres (%d)", total),
			NumberOfItems: total,
		},
		Links: []opds2.Link{
			{Rel: "self", Href: withToken("/opds/v2/tags", tok), Type: opds2.MIMEFeed},
			{Rel: "start", Href: withToken("/opds/v2", tok), Type: opds2.MIMEFeed},
		},
	}
	addPaginationLinks2(feed, r, offset, limit, total)

	for _, tag := range tags {
		feed.Navigation = append(feed.Navigation, opds2.Link{
			Title: tag,
			Href:  withToken("/opds/v2/tags/"+url.PathEscape(tag), tok),
			Type:  opds2.MIMEFeed,
		})
	}

	writeOPDS2(w, http.StatusOK, feed)
}

// handleOPDS2TagBooks serves an OPDS 2.0 acquisition feed for a specific tag/genre.
func (s *Server) handleOPDS2TagBooks(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	vars := mux.Vars(r)
	tag, _ := url.PathUnescape(vars["tag"])
	offset, limit := parsePagination(r)
	userID := currentUserID(r)
	spiceExact := parseSpiceExactQuery(r)
	maxAge := s.maxAgeRatingForUser(userID)

	q := catalog.SearchQuery{
		Tag:          tag,
		Offset:       offset,
		Limit:        limit,
		UserID:       userID,
		MaxAgeRating: maxAge,
		SpiceExact:   spiceExact,
	}
	books, total, err := s.catalog.Search(q)
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := &opds2.Feed{
		Metadata: opds2.FeedMetadata{
			Title:         fmt.Sprintf("Genre : %s (%d)", tag, total),
			NumberOfItems: total,
		},
		Links: []opds2.Link{
			{Rel: "self", Href: r.URL.RequestURI(), Type: opds2.MIMEFeed},
			{Rel: "start", Href: withToken("/opds/v2", tok), Type: opds2.MIMEFeed},
		},
	}
	addPaginationLinks2(feed, r, offset, limit, total)
	feed.Facets = s.buildFacetGroups(r, q, "/opds/v2/tags/"+url.PathEscape(tag), tok)

	for _, bk := range books {
		feed.Publications = append(feed.Publications, bookToPublication(bk, tok))
	}

	writeOPDS2(w, http.StatusOK, feed)
}

// handleOPDS2Publishers serves the OPDS 2.0 publisher navigation feed.
func (s *Server) handleOPDS2Publishers(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	offset, limit := parsePagination(r)

	publishers, total, err := s.catalog.Publishers(offset, limit)
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := &opds2.Feed{
		Metadata: opds2.FeedMetadata{
			Title:         fmt.Sprintf("Éditeurs (%d)", total),
			NumberOfItems: total,
		},
		Links: []opds2.Link{
			{Rel: "self", Href: withToken("/opds/v2/publishers", tok), Type: opds2.MIMEFeed},
			{Rel: "start", Href: withToken("/opds/v2", tok), Type: opds2.MIMEFeed},
		},
	}
	addPaginationLinks2(feed, r, offset, limit, total)

	for _, pub := range publishers {
		feed.Navigation = append(feed.Navigation, opds2.Link{
			Title: pub,
			Href:  withToken("/opds/v2/publishers/"+url.PathEscape(pub), tok),
			Type:  opds2.MIMEFeed,
		})
	}

	writeOPDS2(w, http.StatusOK, feed)
}

// handleOPDS2PublisherBooks serves an OPDS 2.0 acquisition feed for a specific publisher.
func (s *Server) handleOPDS2PublisherBooks(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	vars := mux.Vars(r)
	publisher, _ := url.PathUnescape(vars["publisher"])
	offset, limit := parsePagination(r)
	userID := currentUserID(r)
	spiceExact := parseSpiceExactQuery(r)
	maxAge := s.maxAgeRatingForUser(userID)

	q := catalog.SearchQuery{
		Publisher:    publisher,
		Offset:       offset,
		Limit:        limit,
		UserID:       userID,
		MaxAgeRating: maxAge,
		SpiceExact:   spiceExact,
	}
	books, total, err := s.catalog.Search(q)
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := &opds2.Feed{
		Metadata: opds2.FeedMetadata{
			Title:         fmt.Sprintf("Éditeur : %s (%d)", publisher, total),
			NumberOfItems: total,
		},
		Links: []opds2.Link{
			{Rel: "self", Href: r.URL.RequestURI(), Type: opds2.MIMEFeed},
			{Rel: "start", Href: withToken("/opds/v2", tok), Type: opds2.MIMEFeed},
		},
	}
	addPaginationLinks2(feed, r, offset, limit, total)
	feed.Facets = s.buildFacetGroups(r, q, "/opds/v2/publishers/"+url.PathEscape(publisher), tok)

	for _, bk := range books {
		feed.Publications = append(feed.Publications, bookToPublication(bk, tok))
	}

	writeOPDS2(w, http.StatusOK, feed)
}

// loginPageHTML is the standalone login form served at GET /login.
// It is self-contained (Tailwind CDN) so it works even when the main
// app SPA cannot be served (not authenticated yet).
// The template supports multi-user mode when .Users is non-empty.
const loginPageHTML = `<!DOCTYPE html>
<html lang="fr">
<head>
  <meta charset="UTF-8"/>
  <meta name="viewport" content="width=device-width,initial-scale=1.0"/>
  <title>Connexion – nxt-opds</title>
  <script src="https://cdn.tailwindcss.com"></script>
</head>
<body class="min-h-screen bg-gray-100 flex items-center justify-center">
  <div class="bg-white rounded-2xl shadow-lg p-8 w-full max-w-sm">
    <div class="flex flex-col items-center mb-6">
      <svg class="w-10 h-10 text-blue-600 mb-2" fill="none" stroke="currentColor" viewBox="0 0 24 24">
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
          d="M12 6.253v13m0-13C10.832 5.477 9.246 5 7.5 5S4.168 5.477 3 6.253v13C4.168 18.477 5.754 18 7.5 18s3.332.477 4.5 1.253m0-13C13.168 5.477 14.754 5 16.5 5c1.746 0 3.332.477 4.5 1.253v13C19.832 18.477 18.246 18 16.5 18c-1.746 0-3.332.477-4.5 1.253"/>
      </svg>
      <h1 class="text-xl font-bold text-gray-900">nxt-opds Bibliothèque</h1>
      <p class="text-sm text-gray-500 mt-1">Entrez votre mot de passe pour continuer</p>
    </div>
    {{if .Error}}
    <div class="mb-4 px-3 py-2 bg-red-50 border border-red-200 rounded-lg text-sm text-red-700">
      {{.Error}}
    </div>
    {{end}}
    <form method="POST" action="/login">
      <input type="hidden" name="redirect" value="{{.Redirect}}"/>
      {{if .Users}}
      <div class="mb-4">
        <label class="block text-sm font-medium text-gray-700 mb-2">Qui êtes-vous ?</label>
        <div class="grid grid-cols-2 gap-2">
          {{range .Users}}
          <label class="flex items-center gap-2 p-2 border-2 rounded-lg cursor-pointer hover:border-blue-500 transition-colors has-[:checked]:border-blue-500 has-[:checked]:bg-blue-50">
            <input type="radio" name="user_id" value="{{.ID}}" required class="sr-only"/>
            <span class="w-4 h-4 rounded-full shrink-0" style="background-color:{{.Color}}"></span>
            <span class="text-sm font-medium text-gray-800 truncate">{{.Name}}</span>
          </label>
          {{end}}
        </div>
      </div>
      {{end}}
      <div class="mb-4">
        <label class="block text-sm font-medium text-gray-700 mb-1" for="password">Mot de passe</label>
        <input
          id="password" name="password" type="password" autocomplete="current-password"
          {{if not .Users}}autofocus{{end}} required
          class="w-full px-3 py-2 border border-gray-300 rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent text-sm"
          placeholder="••••••••"
        />
      </div>
      <button type="submit"
        class="w-full py-2 px-4 bg-blue-600 hover:bg-blue-700 text-white font-medium rounded-lg text-sm transition-colors">
        Se connecter
      </button>
    </form>
  </div>
  <script>
    // Highlight selected user card
    document.querySelectorAll('input[name="user_id"]').forEach(function(radio) {
      radio.addEventListener('change', function() {
        document.querySelectorAll('label:has([name="user_id"])').forEach(function(l) {
          l.classList.remove('border-blue-500', 'bg-blue-50');
        });
        radio.closest('label').classList.add('border-blue-500', 'bg-blue-50');
        // Focus password field after user selection
        document.getElementById('password').focus();
      });
    });
  </script>
</body>
</html>`

// handleLoginPage serves the GET /login HTML form.
func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	// If auth is disabled, redirect straight to home.
	if s.opts.Password == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	// If already logged in, redirect to home.
	if c, err := r.Cookie(sessionCookieName); err == nil && s.sessions.valid(c.Value) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	redirect := r.URL.Query().Get("redirect")
	if redirect == "" {
		redirect = "/"
	}
	s.renderLoginPage(w, redirect, "")
}

// handleLoginPost processes the POST /login form submission.
func (s *Server) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	password := r.FormValue("password")
	redirect := r.FormValue("redirect")
	userID := r.FormValue("user_id")
	if redirect == "" || redirect[0] != '/' {
		redirect = "/"
	}

	// Constant-time password comparison to prevent timing attacks.
	passwordOK := s.opts.Password == "" ||
		(subtle.ConstantTimeCompare([]byte(password), []byte(s.opts.Password)) == 1)

	if !passwordOK {
		// Wrong password – re-render the form with an error.
		s.renderLoginPage(w, redirect, "Mot de passe incorrect. Veuillez réessayer.")
		return
	}

	// Validate the selected user ID when user management is configured.
	if s.userManager != nil && userID != "" {
		if _, err := s.userManager.UserByID(userID); err != nil {
			s.renderLoginPage(w, redirect, "Utilisateur invalide. Veuillez réessayer.")
			return
		}
	}

	token, err := s.sessions.create(userID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionDuration.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// handleLogout clears the session cookie and redirects to /login.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		s.sessions.delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:    sessionCookieName,
		Value:   "",
		Path:    "/",
		MaxAge:  -1,
		Expires: time.Unix(0, 0),
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// renderLoginPage writes the login HTML page with the given error message.
func (s *Server) renderLoginPage(w http.ResponseWriter, redirect, errMsg string) {
	type userTmpl struct {
		ID    string
		Name  string
		Color string
	}
	type data struct {
		Error    string
		Redirect string
		Users    []userTmpl
	}

	d := data{Error: errMsg, Redirect: redirect}
	if s.userManager != nil {
		if users, err := s.userManager.Users(); err == nil {
			for _, u := range users {
				d.Users = append(d.Users, userTmpl{ID: u.ID, Name: u.Name, Color: u.Color})
			}
		}
	}

	// Add a custom "not" function to the template FuncMap.
	funcMap := template.FuncMap{
		"not": func(v interface{}) bool {
			if v == nil {
				return true
			}
			if s, ok := v.([]userTmpl); ok {
				return len(s) == 0
			}
			return false
		},
	}
	tmpl, err := template.New("login").Funcs(funcMap).Parse(loginPageHTML)
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if errMsg != "" {
		w.WriteHeader(http.StatusUnauthorized)
	}
	_ = tmpl.Execute(w, d)
}

// handleAPIUpdateCheck checks GitHub for the latest release and returns
// version information as JSON.
func (s *Server) handleAPIUpdateCheck(w http.ResponseWriter, r *http.Request) {
	current := s.opts.Version
	release, err := updater.CheckLatest()
	if err != nil {
		http.Error(w, fmt.Sprintf("update check failed: %v", err), http.StatusBadGateway)
		return
	}
	available := release.TagName != current && current != "dev"
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"currentVersion":  current,
		"latestVersion":   release.TagName,
		"updateAvailable": available,
		"releaseURL":      release.HTMLURL,
		"releaseNotes":    release.Body,
		"assetName":       updater.AssetName(release.TagName),
	})
}

// handleAPIUpdateApply downloads the latest release binary and atomically
// replaces the running executable. The server process must be restarted by
// the operator (or init system) after this returns 200.
func (s *Server) handleAPIUpdateApply(w http.ResponseWriter, r *http.Request) {
	current := s.opts.Version
	release, err := updater.CheckLatest()
	if err != nil {
		http.Error(w, fmt.Sprintf("update check failed: %v", err), http.StatusBadGateway)
		return
	}
	if release.TagName == current {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "up-to-date",
			"version": current,
		})
		return
	}
	if err := updater.Apply(release); err != nil {
		http.Error(w, fmt.Sprintf("update apply failed: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":     "updated",
		"newVersion": release.TagName,
		"message":    "Mise à jour appliquée. Redémarrez le serveur pour activer la nouvelle version.",
	})
}

// handleAPIRestart restarts the server process by re-executing the current
// binary with the same arguments and environment (syscall.Exec). This is
// intended to be called after a binary update has been applied.
func (s *Server) handleAPIRestart(w http.ResponseWriter, r *http.Request) {
	exePath, err := os.Executable()
	if err != nil {
		http.Error(w, fmt.Sprintf("restart: get executable: %v", err), http.StatusInternalServerError)
		return
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		http.Error(w, fmt.Sprintf("restart: resolve symlink: %v", err), http.StatusInternalServerError)
		return
	}
	// Send response before replacing the process.
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "restarting"})
	// Flush the response so the client receives it before exec replaces us.
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	// Replace this process with a fresh copy of the binary.
	_ = syscall.Exec(exePath, os.Args, os.Environ())
}

// statsLabelJSON is the JSON shape for a (label,count) aggregate row.
type statsLabelJSON struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

// statsMonthJSON is the JSON shape for a (month,count) aggregate row.
type statsMonthJSON struct {
	Month string `json:"month"`
	Count int    `json:"count"`
}

// statsUserJSON groups a user's per-user stats; nil fields mean "not available".
type statsUserJSON struct {
	UserID             string           `json:"userId"`
	UserName           string           `json:"userName,omitempty"`
	UserColor          string           `json:"userColor,omitempty"`
	TotalBooks         int              `json:"totalBooks"`
	BooksRead          int              `json:"booksRead"`
	BooksReadThisYear  int              `json:"booksReadThisYear"`
	AverageRating      float64          `json:"averageRating"`
	RatedBooks         int              `json:"ratedBooks"`
	RatingDistribution []int            `json:"ratingDistribution"`
	TopAuthors         []statsLabelJSON `json:"topAuthors"`
	TopTags            []statsLabelJSON `json:"topTags"`
	TopSeries          []statsLabelJSON `json:"topSeries"`
	ByLanguage         []statsLabelJSON `json:"byLanguage"`
	ByMonth            []statsMonthJSON `json:"byMonth"`
}

// statsResponseJSON is the payload returned by GET /api/stats.
type statsResponseJSON struct {
	MultiUser bool            `json:"multiUser"`
	Users     []statsUserJSON `json:"users"`
}

// handleAPIStats returns aggregated reading statistics for the current user.
// Admin users in multi-user mode get stats for every user plus a global "whole library" row.
// GET /api/stats
func (s *Server) handleAPIStats(w http.ResponseWriter, r *http.Request) {
	if s.readStats == nil {
		http.Error(w, "stats not supported", http.StatusNotImplemented)
		return
	}

	resp := statsResponseJSON{MultiUser: s.userManager != nil}

	// Single-user mode: return one global stats row.
	if s.userManager == nil {
		rs, err := s.readStats.ReadStats("")
		if err != nil {
			http.Error(w, "stats query failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		resp.Users = []statsUserJSON{toStatsUserJSON(rs, "Bibliothèque", "")}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	// Multi-user: determine the caller's privileges.
	uid := currentUserID(r)
	if uid == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	me, err := s.userManager.UserByID(uid)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	// Non-admins only see their own stats.
	if !me.IsAdmin {
		rs, err := s.readStats.ReadStats(me.ID)
		if err != nil {
			http.Error(w, "stats query failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		j := toStatsUserJSON(rs, me.Name, me.Color)
		j.UserID = me.ID
		resp.Users = []statsUserJSON{j}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	// Admins get a row per user, followed by a global row.
	users, err := s.userManager.Users()
	if err != nil {
		http.Error(w, "users query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for _, u := range users {
		rs, err := s.readStats.ReadStats(u.ID)
		if err != nil {
			http.Error(w, "stats query failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		j := toStatsUserJSON(rs, u.Name, u.Color)
		j.UserID = u.ID
		resp.Users = append(resp.Users, j)
	}
	if global, err := s.readStats.ReadStats(""); err == nil {
		resp.Users = append(resp.Users, toStatsUserJSON(global, "Tous utilisateurs", ""))
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// toStatsUserJSON converts a catalog.ReadStats to the API-level statsUserJSON.
func toStatsUserJSON(rs *catalog.ReadStats, name, color string) statsUserJSON {
	if rs == nil {
		return statsUserJSON{UserName: name, UserColor: color}
	}
	j := statsUserJSON{
		UserName:          name,
		UserColor:         color,
		TotalBooks:        rs.TotalBooks,
		BooksRead:         rs.BooksRead,
		BooksReadThisYear: rs.BooksReadThisYear,
		AverageRating:     rs.AverageRating,
		RatedBooks:        rs.RatedBooks,
	}
	j.RatingDistribution = make([]int, 5)
	for i, n := range rs.RatingDistribution {
		j.RatingDistribution[i] = n
	}
	j.TopAuthors = toLabelJSON(rs.TopAuthors)
	j.TopTags = toLabelJSON(rs.TopTags)
	j.TopSeries = toLabelJSON(rs.TopSeries)
	j.ByLanguage = toLabelJSON(rs.ByLanguage)
	j.ByMonth = make([]statsMonthJSON, 0, len(rs.ByMonth))
	for _, m := range rs.ByMonth {
		j.ByMonth = append(j.ByMonth, statsMonthJSON{Month: m.Month, Count: m.Count})
	}
	return j
}

func toLabelJSON(rows []catalog.LabelCount) []statsLabelJSON {
	out := make([]statsLabelJSON, 0, len(rows))
	for _, r := range rows {
		out = append(out, statsLabelJSON{Label: r.Label, Count: r.Count})
	}
	return out
}

