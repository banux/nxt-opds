package server

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	fsbackend "github.com/banux/nxt-opds/internal/backend/fs"
	"github.com/banux/nxt-opds/internal/catalog"
	"github.com/banux/nxt-opds/internal/opds"
	"github.com/banux/nxt-opds/internal/opds2"
)

// ---- mock types for refresh tests ----

// noRefreshCatalog implements catalog.Catalog but NOT catalog.Refresher.
// Used to verify that POST /api/refresh returns 501 when backend lacks support.
type noRefreshCatalog struct{}

func (noRefreshCatalog) Root() ([]catalog.NavEntry, error)                                  { return nil, nil }
func (noRefreshCatalog) AllBooks(_, _ int) ([]catalog.Book, int, error)                     { return nil, 0, nil }
func (noRefreshCatalog) BookByID(_ string) (*catalog.Book, error)                           { return nil, fmt.Errorf("not found") }
func (noRefreshCatalog) Search(_ catalog.SearchQuery) ([]catalog.Book, int, error)          { return nil, 0, nil }
func (noRefreshCatalog) BooksByAuthor(_ string, _, _ int) ([]catalog.Book, int, error)      { return nil, 0, nil }
func (noRefreshCatalog) BooksByTag(_ string, _, _ int) ([]catalog.Book, int, error)         { return nil, 0, nil }
func (noRefreshCatalog) BooksByPublisher(_ string, _, _ int) ([]catalog.Book, int, error)   { return nil, 0, nil }
func (noRefreshCatalog) Authors(_, _ int) ([]string, int, error)                            { return nil, 0, nil }
func (noRefreshCatalog) Tags(_, _ int) ([]string, int, error)                               { return nil, 0, nil }
func (noRefreshCatalog) Publishers(_, _ int) ([]string, int, error)                         { return nil, 0, nil }

// failRefreshBackend wraps an fs.Backend and overrides Refresh() to return an error.
// Used to verify that POST /api/refresh propagates backend errors as 500.
type failRefreshBackend struct {
	*fsbackend.Backend
}

func (f *failRefreshBackend) Refresh() error {
	return fmt.Errorf("simulated refresh failure")
}

// uploadBook is a test helper that uploads a minimal EPUB and returns the resulting Book.
func uploadBook(t *testing.T, srv *Server, filename, title, author string) catalog.Book {
	t.Helper()
	epubData := buildEPUBBytes(title, author)
	body, ct := buildMultipartBody(t, "file", filename, epubData)
	req := httptest.NewRequest(http.MethodPost, "/api/upload", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("upload %q: expected 201, got %d: %s", filename, rr.Code, rr.Body.String())
	}
	var book catalog.Book
	if err := json.NewDecoder(rr.Body).Decode(&book); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	return book
}

// ---- OPDS root ----

func TestHandleRoot_ReturnsNavigationFeed(t *testing.T) {
	srv := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/opds", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/atom+xml") {
		t.Errorf("unexpected Content-Type: %q", ct)
	}
	// Must be valid XML
	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("response is not valid XML: %v", err)
	}
	if feed.ID != "urn:nxt-opds:root" {
		t.Errorf("feed ID: got %q, want urn:nxt-opds:root", feed.ID)
	}
	// Should have navigation entries (All Books, By Author, By Genre)
	if len(feed.Entries) < 3 {
		t.Errorf("expected at least 3 navigation entries, got %d", len(feed.Entries))
	}
}

func TestHandleRoot_TrailingSlash(t *testing.T) {
	srv := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/opds/", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for /opds/, got %d", rr.Code)
	}
}

// ---- OPDS all books ----

func TestHandleAllBooks_EmptyCatalog(t *testing.T) {
	srv := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/opds/books", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	if len(feed.Entries) != 0 {
		t.Errorf("expected 0 entries in empty catalog, got %d", len(feed.Entries))
	}
}

func TestHandleAllBooks_WithBooks(t *testing.T) {
	srv := newTestServer(t, Options{})
	uploadBook(t, srv, "book1.epub", "Go Programming", "Rob Pike")
	uploadBook(t, srv, "book2.epub", "Rust in Action", "Tim McNamara")

	req := httptest.NewRequest(http.MethodGet, "/opds/books", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	if len(feed.Entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(feed.Entries))
	}
}

func TestHandleAllBooks_Pagination_FirstPage(t *testing.T) {
	srv := newTestServer(t, Options{})
	uploadBook(t, srv, "a.epub", "Book A", "Author A")
	uploadBook(t, srv, "b.epub", "Book B", "Author B")
	uploadBook(t, srv, "c.epub", "Book C", "Author C")

	req := httptest.NewRequest(http.MethodGet, "/opds/books?offset=0&limit=2", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	if len(feed.Entries) != 2 {
		t.Errorf("expected 2 entries on first page (limit=2), got %d", len(feed.Entries))
	}
	// Should have a "next" link since there are 3 total books
	hasNext := false
	for _, l := range feed.Links {
		if l.Rel == opds.RelNext {
			hasNext = true
		}
	}
	if !hasNext {
		t.Error("expected a 'next' pagination link on first page")
	}
}

func TestHandleAllBooks_Pagination_LastPage(t *testing.T) {
	srv := newTestServer(t, Options{})
	uploadBook(t, srv, "a.epub", "Book A", "Author A")
	uploadBook(t, srv, "b.epub", "Book B", "Author B")
	uploadBook(t, srv, "c.epub", "Book C", "Author C")

	// offset=2, limit=2 → last page (only 1 entry), no "next"
	req := httptest.NewRequest(http.MethodGet, "/opds/books?offset=2&limit=2", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	if len(feed.Entries) != 1 {
		t.Errorf("expected 1 entry on last page, got %d", len(feed.Entries))
	}
	for _, l := range feed.Links {
		if l.Rel == opds.RelNext {
			t.Error("unexpected 'next' pagination link on last page")
		}
	}
	// But should still have "first" and "last"
	hasFirst, hasLast := false, false
	for _, l := range feed.Links {
		if l.Rel == opds.RelFirst {
			hasFirst = true
		}
		if l.Rel == opds.RelLast {
			hasLast = true
		}
	}
	if !hasFirst || !hasLast {
		t.Error("expected 'first' and 'last' links on paginated feed")
	}
}

// ---- OPDS single book ----

func TestHandleBook_NotFound(t *testing.T) {
	srv := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/opds/books/nonexistent-id", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown book ID, got %d", rr.Code)
	}
}

func TestHandleBook_Found(t *testing.T) {
	srv := newTestServer(t, Options{})
	book := uploadBook(t, srv, "found.epub", "Found Book", "Found Author")

	req := httptest.NewRequest(http.MethodGet, "/opds/books/"+book.ID, nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	if len(feed.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(feed.Entries))
	}
	if feed.Entries[0].Title.Value != "Found Book" {
		t.Errorf("title: got %q, want Found Book", feed.Entries[0].Title.Value)
	}
}

// ---- OPDS search ----

func TestHandleSearch_MissingQuery(t *testing.T) {
	srv := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/opds/search", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing q param, got %d", rr.Code)
	}
}

func TestHandleSearch_NoResults(t *testing.T) {
	srv := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/opds/search?q=doesnotexist", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	if len(feed.Entries) != 0 {
		t.Errorf("expected 0 results for unknown query, got %d", len(feed.Entries))
	}
}

func TestHandleSearch_WithResults(t *testing.T) {
	srv := newTestServer(t, Options{})
	uploadBook(t, srv, "golang.epub", "Learning Go", "Jon Bodner")
	uploadBook(t, srv, "python.epub", "Learning Python", "Mark Lutz")

	req := httptest.NewRequest(http.MethodGet, "/opds/search?q=Learning+Go", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	// "Learning Go" should match at least one book
	if len(feed.Entries) == 0 {
		t.Error("expected at least 1 search result for 'Learning Go'")
	}
}

// ---- OPDS authors ----

func TestHandleAuthors_Empty(t *testing.T) {
	srv := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/opds/authors", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	if len(feed.Entries) != 0 {
		t.Errorf("expected 0 author entries in empty catalog, got %d", len(feed.Entries))
	}
}

func TestHandleAuthors_WithBooks(t *testing.T) {
	srv := newTestServer(t, Options{})
	uploadBook(t, srv, "a.epub", "Book A", "Alice Smith")
	uploadBook(t, srv, "b.epub", "Book B", "Bob Jones")

	req := httptest.NewRequest(http.MethodGet, "/opds/authors", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	if len(feed.Entries) != 2 {
		t.Errorf("expected 2 author entries, got %d", len(feed.Entries))
	}
}

func TestHandleAuthorBooks_NotFound(t *testing.T) {
	srv := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/opds/authors/"+url.PathEscape("Unknown Author"), nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (empty feed) for unknown author, got %d", rr.Code)
	}
	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	if len(feed.Entries) != 0 {
		t.Errorf("expected 0 entries for unknown author, got %d", len(feed.Entries))
	}
}

func TestHandleAuthorBooks_WithBooks(t *testing.T) {
	srv := newTestServer(t, Options{})
	uploadBook(t, srv, "alice1.epub", "Alice Book 1", "Alice Smith")
	uploadBook(t, srv, "alice2.epub", "Alice Book 2", "Alice Smith")
	uploadBook(t, srv, "bob.epub", "Bob Book", "Bob Jones")

	authorPath := url.PathEscape("Alice Smith")
	req := httptest.NewRequest(http.MethodGet, "/opds/authors/"+authorPath, nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	if len(feed.Entries) != 2 {
		t.Errorf("expected 2 books by Alice Smith, got %d", len(feed.Entries))
	}
}

// ---- OPDS tags ----

func TestHandleTags_Empty(t *testing.T) {
	srv := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/opds/tags", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

// ---- OPDS OpenSearch ----

func TestHandleOpenSearch_ValidXML(t *testing.T) {
	srv := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/opds/opensearch.xml", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/opensearchdescription+xml") {
		t.Errorf("unexpected Content-Type: %q", ct)
	}
	// Must be parseable XML
	var v interface{}
	dec := xml.NewDecoder(rr.Body)
	if err := dec.Decode(&v); err != nil {
		t.Errorf("OpenSearch response is not valid XML: %v", err)
	}
}

// ---- API books ----

func TestHandleAPIBooks_Empty(t *testing.T) {
	srv := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/api/books", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("unexpected Content-Type: %q", ct)
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	books, _ := resp["books"].([]interface{})
	if len(books) != 0 {
		t.Errorf("expected 0 books, got %d", len(books))
	}
	total, _ := resp["total"].(float64)
	if total != 0 {
		t.Errorf("expected total=0, got %v", total)
	}
}

func TestHandleAPIBooks_WithBooks(t *testing.T) {
	srv := newTestServer(t, Options{})
	uploadBook(t, srv, "x.epub", "API Book X", "Author X")
	uploadBook(t, srv, "y.epub", "API Book Y", "Author Y")

	req := httptest.NewRequest(http.MethodGet, "/api/books", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	books, _ := resp["books"].([]interface{})
	if len(books) != 2 {
		t.Errorf("expected 2 books, got %d", len(books))
	}
	total, _ := resp["total"].(float64)
	if total != 2 {
		t.Errorf("expected total=2, got %v", total)
	}
}

func TestHandleAPIBooks_Search(t *testing.T) {
	srv := newTestServer(t, Options{})
	uploadBook(t, srv, "match.epub", "Searchable Title", "The Author")
	uploadBook(t, srv, "nomatch.epub", "Other Book", "The Author")

	req := httptest.NewRequest(http.MethodGet, "/api/books?q=Searchable", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	books, _ := resp["books"].([]interface{})
	if len(books) == 0 {
		t.Error("expected at least 1 book matching 'Searchable'")
	}
}

func TestHandleAPIBooks_BookFields(t *testing.T) {
	srv := newTestServer(t, Options{})
	uploadBook(t, srv, "fields.epub", "Field Test Book", "Field Author")

	req := httptest.NewRequest(http.MethodGet, "/api/books", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp struct {
		Books []bookJSON `json:"books"`
		Total int        `json:"total"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Books) == 0 {
		t.Fatal("expected at least 1 book")
	}
	b := resp.Books[0]
	if b.ID == "" {
		t.Error("book ID must not be empty")
	}
	if b.Title == "" {
		t.Error("book title must not be empty")
	}
	if b.DownloadURL == "" {
		t.Error("book downloadUrl must not be empty")
	}
	if !strings.HasPrefix(b.DownloadURL, "/opds/books/") {
		t.Errorf("unexpected downloadUrl: %q", b.DownloadURL)
	}
}

func TestHandleAPIBooks_Pagination(t *testing.T) {
	srv := newTestServer(t, Options{})
	// Upload 3 books
	uploadBook(t, srv, "a.epub", "Book A", "Author A")
	uploadBook(t, srv, "b.epub", "Book B", "Author B")
	uploadBook(t, srv, "c.epub", "Book C", "Author C")

	// Page 1: limit=2, offset=0 → 2 books, total=3
	req := httptest.NewRequest(http.MethodGet, "/api/books?limit=2&offset=0", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp1 struct {
		Books []bookJSON `json:"books"`
		Total int        `json:"total"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp1); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp1.Books) != 2 {
		t.Errorf("expected 2 books on first page, got %d", len(resp1.Books))
	}
	if resp1.Total != 3 {
		t.Errorf("expected total=3, got %d", resp1.Total)
	}

	// Page 2: limit=2, offset=2 → 1 book, total=3
	req2 := httptest.NewRequest(http.MethodGet, "/api/books?limit=2&offset=2", nil)
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr2.Code)
	}
	var resp2 struct {
		Books []bookJSON `json:"books"`
		Total int        `json:"total"`
	}
	if err := json.NewDecoder(rr2.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp2.Books) != 1 {
		t.Errorf("expected 1 book on second page, got %d", len(resp2.Books))
	}
	if resp2.Total != 3 {
		t.Errorf("expected total=3, got %d", resp2.Total)
	}
}

// ---- API update book ----

func TestHandleAPIUpdateBook_NotFound(t *testing.T) {
	srv := newTestServer(t, Options{})
	body := strings.NewReader(`{"title":"New Title"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/books/nonexistent", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 for nonexistent book, got %d", rr.Code)
	}
}

func TestHandleAPIUpdateBook_InvalidJSON(t *testing.T) {
	srv := newTestServer(t, Options{})
	book := uploadBook(t, srv, "edit.epub", "Original Title", "Original Author")

	req := httptest.NewRequest(http.MethodPatch, "/api/books/"+book.ID, strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", rr.Code)
	}
}

func TestHandleAPIUpdateBook_UpdateTitle(t *testing.T) {
	srv := newTestServer(t, Options{})
	book := uploadBook(t, srv, "update.epub", "Original Title", "Original Author")

	newTitle := "Updated Title"
	body := strings.NewReader(`{"title":"Updated Title"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/books/"+book.ID, body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var updated bookJSON
	if err := json.NewDecoder(rr.Body).Decode(&updated); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if updated.Title != newTitle {
		t.Errorf("title: got %q, want %q", updated.Title, newTitle)
	}
	if updated.ID != book.ID {
		t.Errorf("ID changed: got %q, want %q", updated.ID, book.ID)
	}
}

func TestHandleAPIUpdateBook_UpdateIsRead(t *testing.T) {
	srv := newTestServer(t, Options{})
	book := uploadBook(t, srv, "read.epub", "Read Test", "Read Author")

	// Initially not read
	if book.IsRead {
		t.Skip("book was unexpectedly marked as read after upload")
	}

	body := strings.NewReader(`{"isRead":true}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/books/"+book.ID, body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var updated bookJSON
	if err := json.NewDecoder(rr.Body).Decode(&updated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !updated.IsRead {
		t.Error("expected isRead=true after update")
	}
}

func TestHandleAPIUpdateBook_UpdateSeries(t *testing.T) {
	srv := newTestServer(t, Options{})
	book := uploadBook(t, srv, "series.epub", "Series Book", "Series Author")

	body := strings.NewReader(`{"series":"My Series","seriesIndex":"2"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/books/"+book.ID, body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var updated bookJSON
	if err := json.NewDecoder(rr.Body).Decode(&updated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if updated.Series != "My Series" {
		t.Errorf("series: got %q, want My Series", updated.Series)
	}
	if updated.SeriesIndex != "2" {
		t.Errorf("seriesIndex: got %q, want 2", updated.SeriesIndex)
	}
}

func TestHandleAPIUpdateBook_UpdateTags(t *testing.T) {
	srv := newTestServer(t, Options{})
	book := uploadBook(t, srv, "tags.epub", "Tagged Book", "Tag Author")

	body := strings.NewReader(`{"tags":["fiction","adventure"]}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/books/"+book.ID, body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var updated bookJSON
	if err := json.NewDecoder(rr.Body).Decode(&updated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(updated.Tags) != 2 {
		t.Errorf("tags: got %v, want [fiction adventure]", updated.Tags)
	}
}

// ---- Pagination helper unit tests ----

func TestPaginationLink_PreservesExistingQueryParams(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/opds/books?q=test&offset=10&limit=5", nil)
	link := paginationLink(req, 20, 5)
	if !strings.Contains(link, "q=test") {
		t.Errorf("paginationLink lost q param: %q", link)
	}
	if !strings.Contains(link, "offset=20") {
		t.Errorf("paginationLink wrong offset: %q", link)
	}
	if !strings.Contains(link, "limit=5") {
		t.Errorf("paginationLink wrong limit: %q", link)
	}
}

func TestAddPaginationLinks_NoPaginationForSmallSet(t *testing.T) {
	feed := opds.NewAcquisitionFeed("urn:test", "Test")
	req := httptest.NewRequest(http.MethodGet, "/opds/books", nil)
	// 5 books, limit 50 → no need for next/prev, but first/last still added
	addPaginationLinks(feed, req, 0, 50, 5, opds.MIMEAcquisitionFeed)

	hasPrev, hasNext := false, false
	for _, l := range feed.Links {
		if l.Rel == opds.RelPrevious {
			hasPrev = true
		}
		if l.Rel == opds.RelNext {
			hasNext = true
		}
	}
	if hasPrev {
		t.Error("unexpected 'previous' link on first page with no overflow")
	}
	if hasNext {
		t.Error("unexpected 'next' link when all results fit on one page")
	}
}

func TestAddPaginationLinks_MiddlePage(t *testing.T) {
	feed := opds.NewAcquisitionFeed("urn:test", "Test")
	req := httptest.NewRequest(http.MethodGet, "/opds/books", nil)
	// offset=10, limit=10, total=30 → middle page
	addPaginationLinks(feed, req, 10, 10, 30, opds.MIMEAcquisitionFeed)

	rels := map[string]string{}
	for _, l := range feed.Links {
		rels[l.Rel] = l.Href
	}
	if _, ok := rels[opds.RelFirst]; !ok {
		t.Error("missing 'first' link")
	}
	if _, ok := rels[opds.RelLast]; !ok {
		t.Error("missing 'last' link")
	}
	if _, ok := rels[opds.RelNext]; !ok {
		t.Error("missing 'next' link on middle page")
	}
	if _, ok := rels[opds.RelPrevious]; !ok {
		t.Error("missing 'previous' link on middle page")
	}
}

func TestAddPaginationLinks_ZeroTotal(t *testing.T) {
	feed := opds.NewAcquisitionFeed("urn:test", "Test")
	req := httptest.NewRequest(http.MethodGet, "/opds/books", nil)
	addPaginationLinks(feed, req, 0, 50, 0, opds.MIMEAcquisitionFeed)
	if len(feed.Links) != 0 {
		t.Errorf("expected no pagination links for empty result set, got %d", len(feed.Links))
	}
}

// ---- API refresh ----

func TestHandleAPIRefresh_Success(t *testing.T) {
	// newTestServer uses fs.Backend which implements catalog.Refresher.
	srv := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]bool
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp["ok"] {
		t.Errorf("expected {\"ok\":true}, got %v", resp)
	}
}

func TestHandleAPIRefresh_NotSupported(t *testing.T) {
	// Use a catalog that does NOT implement catalog.Refresher.
	srv := New(noRefreshCatalog{}, Options{})
	req := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 when backend lacks Refresher, got %d", rr.Code)
	}
}

func TestHandleAPIRefresh_BackendError(t *testing.T) {
	// Use a backend whose Refresh() always returns an error.
	dir := t.TempDir()
	base, err := fsbackend.New(dir)
	if err != nil {
		t.Fatalf("backend.New: %v", err)
	}
	srv := New(&failRefreshBackend{base}, Options{})
	req := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when Refresh() fails, got %d", rr.Code)
	}
}

// ---- API single book ----

func TestHandleAPIBook_NotFound(t *testing.T) {
	srv := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/api/books/nonexistent", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestHandleAPIBook_Found(t *testing.T) {
	srv := newTestServer(t, Options{})
	uploadBook(t, srv, "single.epub", "Single Book", "Solo Author")

	// Get the book ID from the list
	req := httptest.NewRequest(http.MethodGet, "/api/books", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	var listResp struct {
		Books []bookJSON `json:"books"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listResp.Books) == 0 {
		t.Fatal("expected book in list")
	}
	id := listResp.Books[0].ID

	// Fetch single book
	req2 := httptest.NewRequest(http.MethodGet, "/api/books/"+id, nil)
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr2.Code)
	}
	var b bookJSON
	if err := json.NewDecoder(rr2.Body).Decode(&b); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b.ID != id {
		t.Errorf("id: got %q, want %q", b.ID, id)
	}
	if b.DownloadURL == "" {
		t.Error("downloadUrl must not be empty")
	}
}

// ---- Health check ----

func TestHandleHealth_ReturnsJSON(t *testing.T) {
	srv := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("status: got %q, want ok", resp["status"])
	}
}

// ---- OPDS token authentication ----

func TestOPDSTokenAuth_ValidToken(t *testing.T) {
	srv := New(noRefreshCatalog{}, Options{Password: "pw", OPDSToken: "secret-token"})
	req := httptest.NewRequest(http.MethodGet, "/opds?token=secret-token", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 with valid token, got %d", rr.Code)
	}
}

func TestOPDSTokenAuth_InvalidToken(t *testing.T) {
	srv := New(noRefreshCatalog{}, Options{Password: "pw", OPDSToken: "secret-token"})
	req := httptest.NewRequest(http.MethodGet, "/opds?token=wrong", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong token, got %d", rr.Code)
	}
}

func TestOPDSTokenAuth_NoToken_Returns401(t *testing.T) {
	srv := New(noRefreshCatalog{}, Options{Password: "pw", OPDSToken: "secret-token"})
	req := httptest.NewRequest(http.MethodGet, "/opds", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 when no token provided, got %d", rr.Code)
	}
}

func TestAPIConfig_ReturnsToken(t *testing.T) {
	srv := New(noRefreshCatalog{}, Options{OPDSToken: "mytoken"})
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["opdsToken"] != "mytoken" {
		t.Errorf("opdsToken: got %v, want mytoken", resp["opdsToken"])
	}
}

// ---- OPDS token propagation through feed links ----

// TestWithToken verifies the withToken helper appends the token correctly.
func TestWithToken_NoToken(t *testing.T) {
	if got := withToken("/opds/books", ""); got != "/opds/books" {
		t.Errorf("withToken with empty tok: got %q, want /opds/books", got)
	}
}

func TestWithToken_NoExistingQuery(t *testing.T) {
	got := withToken("/opds/books", "secret")
	want := "/opds/books?token=secret"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWithToken_WithExistingQuery(t *testing.T) {
	got := withToken("/opds/books?offset=0&limit=10", "secret")
	want := "/opds/books?offset=0&limit=10&token=secret"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestOPDSRootFeed_TokenPropagation verifies that when the root feed is requested
// with a token, all navigation entry links in the feed include the token.
func TestOPDSRootFeed_TokenPropagation(t *testing.T) {
	srv := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/opds?token=mytoken", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}

	// Every navigation entry link href must contain the token
	for _, entry := range feed.Entries {
		for _, link := range entry.Links {
			if !strings.Contains(link.Href, "token=mytoken") {
				t.Errorf("navigation entry %q link %q does not contain token", entry.Title.Value, link.Href)
			}
		}
	}

	// Self and start links must also contain the token
	for _, link := range feed.Links {
		if link.Rel == opds.RelSelf || link.Rel == opds.RelStart {
			if !strings.Contains(link.Href, "token=mytoken") {
				t.Errorf("feed link (rel=%s) %q does not contain token", link.Rel, link.Href)
			}
		}
	}
}

// TestOPDSAllBooks_TokenPropagationInEntries verifies that when the books feed
// is requested with a token, acquisition and cover link hrefs include the token.
func TestOPDSAllBooks_TokenPropagationInEntries(t *testing.T) {
	srv := newTestServer(t, Options{})
	uploadBook(t, srv, "test.epub", "Token Test", "Author")

	req := httptest.NewRequest(http.MethodGet, "/opds/books?token=mytoken", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}

	if len(feed.Entries) == 0 {
		t.Fatal("expected at least 1 entry")
	}

	for _, entry := range feed.Entries {
		for _, link := range entry.Links {
			if link.Rel == opds.RelAcquisition && !strings.Contains(link.Href, "token=mytoken") {
				t.Errorf("acquisition link %q does not contain token", link.Href)
			}
		}
	}
}

// TestOPDSRootFeed_NoTokenWhenAbsent verifies that when no token is in the request,
// feed links do not gain a spurious token parameter.
func TestOPDSRootFeed_NoTokenWhenAbsent(t *testing.T) {
	srv := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/opds", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}

	for _, entry := range feed.Entries {
		for _, link := range entry.Links {
			if strings.Contains(link.Href, "token=") {
				t.Errorf("navigation link %q unexpectedly contains token= when none was requested", link.Href)
			}
		}
	}
}

// ---- Cover image token authentication ----

// TestCoverToken_ValidToken verifies that a valid OPDS token grants access to /covers/{id}.
// The cover endpoint should return 501 (backend doesn't support covers in this mock)
// rather than 401 (unauthorized), confirming the token was accepted.
func TestCoverToken_ValidToken(t *testing.T) {
	srv := New(noRefreshCatalog{}, Options{Password: "pw", OPDSToken: "cover-tok"})
	req := httptest.NewRequest(http.MethodGet, "/covers/someid?token=cover-tok", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	// The mock backend doesn't implement CoverProvider so we expect 501,
	// NOT 401. A 401 would mean the token was rejected.
	if rr.Code == http.StatusUnauthorized {
		t.Errorf("cover with valid token should not return 401; got %d", rr.Code)
	}
}

// TestCoverToken_InvalidToken verifies that a wrong token is rejected for /covers/{id}.
func TestCoverToken_InvalidToken(t *testing.T) {
	srv := New(noRefreshCatalog{}, Options{Password: "pw", OPDSToken: "cover-tok"})
	req := httptest.NewRequest(http.MethodGet, "/covers/someid?token=wrong", nil)
	req.Header.Set("Accept", "image/jpeg")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("cover with wrong token: expected 401, got %d", rr.Code)
	}
}

// TestCoverToken_NoToken verifies that a missing token is rejected for /covers/{id}
// when an OPDS token is configured.
func TestCoverToken_NoToken(t *testing.T) {
	srv := New(noRefreshCatalog{}, Options{Password: "pw", OPDSToken: "cover-tok"})
	req := httptest.NewRequest(http.MethodGet, "/covers/someid", nil)
	req.Header.Set("Accept", "image/jpeg")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("cover with no token: expected 401, got %d", rr.Code)
	}
}

// TestMCPInfo_GET verifies that GET /mcp returns a small JSON document
// instead of falling through to the SPA catch-all and returning a 404.
// Without this, operators get an opaque 404 with zero diagnostics when
// they curl the endpoint to check it is reachable.
func TestMCPInfo_GET(t *testing.T) {
	srv := newTestServer(t, Options{OPDSToken: "tok"})
	req := httptest.NewRequest(http.MethodGet, "/mcp?token=tok", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /mcp: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("GET /mcp: expected application/json, got %q", ct)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if doc["method"] != "POST" || doc["endpoint"] != "/mcp" {
		t.Errorf("missing method/endpoint hints in info doc: %v", doc)
	}
}

// TestMCP_GET_Unauthenticated verifies that a GET /mcp without auth returns
// 401 instead of redirecting to /login (which would confuse JSON-RPC clients
// inspecting the endpoint with `curl`).
func TestMCP_GET_Unauthenticated(t *testing.T) {
	srv := newTestServer(t, Options{Password: "pw", OPDSToken: "tok"})
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Accept", "text/html")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("GET /mcp without auth: expected 401, got %d", rr.Code)
	}
}

// TestHandleSW_InjectsVersion verifies that GET /sw.js substitutes the
// __APP_VERSION__ placeholder with the running binary version so each release
// gets its own service-worker cache namespace. A regression here means clients
// stay stuck on the previous cached HTML forever.
func TestHandleSW_InjectsVersion(t *testing.T) {
	const swContents = `const CACHE_NAME = 'nxt-opds-static-' + '__APP_VERSION__';`
	staticFS := fstest.MapFS{
		"sw.js": &fstest.MapFile{Data: []byte(swContents)},
	}
	srv := newTestServer(t, Options{
		StaticFS: staticFS,
		Version:  "v1.101.0",
	})

	req := httptest.NewRequest(http.MethodGet, "/sw.js", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /sw.js: expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "__APP_VERSION__") {
		t.Errorf("expected __APP_VERSION__ to be replaced, got: %s", body)
	}
	if !strings.Contains(body, "nxt-opds-static-") || !strings.Contains(body, "v1.101.0") {
		t.Errorf("expected cache name to contain version v1.101.0, got: %s", body)
	}
}

// TestHandleSW_DefaultsToDev verifies that when no version is provided (e.g.
// `go run` without ldflags), the cache name falls back to a deterministic
// "dev" suffix rather than something empty that could collide across builds.
func TestHandleSW_DefaultsToDev(t *testing.T) {
	staticFS := fstest.MapFS{
		"sw.js": &fstest.MapFile{Data: []byte(`'__APP_VERSION__'`)},
	}
	srv := newTestServer(t, Options{StaticFS: staticFS})

	req := httptest.NewRequest(http.MethodGet, "/sw.js", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if !strings.Contains(rr.Body.String(), "'dev'") {
		t.Errorf("expected dev fallback, got: %s", rr.Body.String())
	}
}

// TestHandleAPIUpdateBook_SpiceRating_ValidAndInvalid exercises the new
// spiceRating PATCH field: a value of 4 is accepted, persisted and returned;
// an out-of-range value (7) is rejected with 400.
func TestHandleAPIUpdateBook_SpiceRating_ValidAndInvalid(t *testing.T) {
	srv := newTestServer(t, Options{})
	book := uploadBook(t, srv, "spice.epub", "Spicy Book", "Author A")

	// Valid: spiceRating=4 with ageRating=18 must persist.
	body := strings.NewReader(`{"ageRating":18,"spiceRating":4}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/books/"+book.ID, body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("valid spice: got %d: %s", rr.Code, rr.Body.String())
	}
	var updated bookJSON
	if err := json.NewDecoder(rr.Body).Decode(&updated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if updated.SpiceRating != 4 {
		t.Errorf("SpiceRating: got %d, want 4", updated.SpiceRating)
	}
	if updated.AgeRating != 18 {
		t.Errorf("AgeRating: got %d, want 18", updated.AgeRating)
	}

	// Out-of-range: 7 must be rejected with 400.
	bad := strings.NewReader(`{"spiceRating":7}`)
	req2 := httptest.NewRequest(http.MethodPatch, "/api/books/"+book.ID, bad)
	req2.Header.Set("Content-Type", "application/json")
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("spiceRating=7 expected 400, got %d", rr2.Code)
	}

	// Negative: must also be rejected.
	neg := strings.NewReader(`{"spiceRating":-1}`)
	req3 := httptest.NewRequest(http.MethodPatch, "/api/books/"+book.ID, neg)
	req3.Header.Set("Content-Type", "application/json")
	rr3 := httptest.NewRecorder()
	srv.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusBadRequest {
		t.Errorf("spiceRating=-1 expected 400, got %d", rr3.Code)
	}
}

// TestHandleAPIBooks_SpiceMaxIgnored verifies the deprecated ?spice_max=
// query parameter is now silently ignored (no error, no filtering applied).
// Clients that bookmarked the v1.122–v1.127 URL still get a valid response;
// they just need to migrate to ?spice=N to keep filtering.
func TestHandleAPIBooks_SpiceMaxIgnored(t *testing.T) {
	srv := newTestServer(t, Options{})
	spicy := uploadBook(t, srv, "spicy.epub", "Spicy Book", "Author A")
	mild := uploadBook(t, srv, "mild.epub", "Mild Book", "Author B")
	child := uploadBook(t, srv, "child.epub", "Child Book", "Author C")

	patch := func(id, body string) {
		req := httptest.NewRequest(http.MethodPatch, "/api/books/"+id, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("patch %s: %d - %s", id, rr.Code, rr.Body.String())
		}
	}
	patch(spicy.ID, `{"ageRating":18,"spiceRating":4}`)
	patch(mild.ID, `{"ageRating":16,"spiceRating":1}`)
	patch(child.ID, `{"ageRating":6}`)

	// ?spice_max=2 should be ignored — the Spicy 18+ book stays visible.
	req := httptest.NewRequest(http.MethodGet, "/api/books?spice_max=2", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/books?spice_max=2: %d", rr.Code)
	}
	var resp struct {
		Books []bookJSON `json:"books"`
		Total int        `json:"total"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	titles := map[string]bool{}
	for _, b := range resp.Books {
		titles[b.Title] = true
	}
	if !titles["Spicy Book"] {
		t.Error("?spice_max=2 is now a no-op; Spicy Book should remain visible")
	}
	if !titles["Mild Book"] || !titles["Child Book"] {
		t.Error("?spice_max=2 is now a no-op; all books should remain visible")
	}
}

// TestHandleAPIBooks_SpiceExactFilter verifies that the new ?spice=N query
// parameter is an EXACT match scoped to 16+/18+ books and that books under 16
// are excluded (even when their stored spice value would happen to match).
func TestHandleAPIBooks_SpiceExactFilter(t *testing.T) {
	srv := newTestServer(t, Options{})
	spicy := uploadBook(t, srv, "spicy.epub", "Spicy Book", "Author A")
	mild := uploadBook(t, srv, "mild.epub", "Mild Book", "Author B")
	other := uploadBook(t, srv, "other.epub", "Other Adult Book", "Author C")
	child := uploadBook(t, srv, "child.epub", "Child Book", "Author D")

	patch := func(id, body string) {
		req := httptest.NewRequest(http.MethodPatch, "/api/books/"+id, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("patch %s: %d - %s", id, rr.Code, rr.Body.String())
		}
	}
	patch(spicy.ID, `{"ageRating":18,"spiceRating":4}`)
	patch(mild.ID, `{"ageRating":16,"spiceRating":1}`)
	patch(other.ID, `{"ageRating":18,"spiceRating":2}`)
	patch(child.ID, `{"ageRating":6}`)

	// ?spice=4 → only "Spicy Book" (exact match, sub-16 books are excluded).
	req := httptest.NewRequest(http.MethodGet, "/api/books?spice=4", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/books?spice=4: %d", rr.Code)
	}
	var resp struct {
		Books []bookJSON `json:"books"`
		Total int        `json:"total"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	titles := map[string]bool{}
	for _, b := range resp.Books {
		titles[b.Title] = true
	}
	if !titles["Spicy Book"] {
		t.Error("?spice=4 must include Spicy Book (16+, spice=4)")
	}
	if titles["Other Adult Book"] {
		t.Error("?spice=4 must exclude Other Adult Book (spice=2)")
	}
	if titles["Mild Book"] {
		t.Error("?spice=4 must exclude Mild Book (spice=1)")
	}
	if titles["Child Book"] {
		t.Error("?spice=4 must exclude Child Book (under 16, sub-16 has no meaningful spice)")
	}
}

// ---- OPDS spice navigation ----

// TestHandleRoot_HasSpiceNavEntry verifies that GET /opds advertises a
// "Niveaux de piment" navigation entry that links to /opds/spice.
func TestHandleRoot_HasSpiceNavEntry(t *testing.T) {
	srv := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/opds", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	var found *opds.Entry
	for i, e := range feed.Entries {
		if e.ID == "urn:nxt-opds:spice" {
			found = &feed.Entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("root feed missing urn:nxt-opds:spice entry; entries=%+v", feed.Entries)
	}
	if found.Title.Value != "Niveaux de piment" {
		t.Errorf("spice title: got %q", found.Title.Value)
	}
	if len(found.Links) == 0 || !strings.Contains(found.Links[0].Href, "/opds/spice") {
		t.Errorf("spice entry should link to /opds/spice; got %+v", found.Links)
	}
}

// TestHandleOPDS2Root_NavigationStripsDeprecatedSubFeeds verifies that the
// OPDS v2 root no longer advertises the sub-feeds now surfaced via facets
// (Piment) or groups (Pile à lire / Recommandations / Non lus / Liste de
// souhaits).  Only Authors / Tags / Publishers / Publications stay in
// `navigation`.
func TestHandleOPDS2Root_NavigationStripsDeprecatedSubFeeds(t *testing.T) {
	srv := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/opds/v2", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var feed opds2.Feed
	if err := json.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	deprecated := []string{"/opds/v2/spice", "/opds/v2/unread", "/opds/v2/to-read", "/opds/v2/recommendations", "/opds/v2/wishlist"}
	for _, nv := range feed.Navigation {
		for _, d := range deprecated {
			if strings.Contains(nv.Href, d) {
				t.Errorf("nav item %q must not point at deprecated %q; got href %q", nv.Title, d, nv.Href)
			}
		}
	}
	// Required: the 4 essential sub-feeds remain.
	want := map[string]bool{"/opds/v2/publications": false, "/opds/v2/authors": false, "/opds/v2/tags": false, "/opds/v2/publishers": false}
	for _, nv := range feed.Navigation {
		for w := range want {
			if strings.Contains(nv.Href, w) {
				want[w] = true
			}
		}
	}
	for w, found := range want {
		if !found {
			t.Errorf("essential nav item %q missing from root", w)
		}
	}

	// Back-compat: deprecated routes that we kept still answer 200 (or 501).
	// Wishlist and to-read/recommendations are excluded — wishlist is now
	// fully removed (v1.136) since it never carried real publications, and
	// the user-scoped routes require auth/state to evaluate.
	keptRoutes := []string{"/opds/v2/spice", "/opds/v2/unread"}
	for _, d := range keptRoutes {
		req2 := httptest.NewRequest(http.MethodGet, d, nil)
		rr2 := httptest.NewRecorder()
		srv.ServeHTTP(rr2, req2)
		if rr2.Code != http.StatusOK && rr2.Code != http.StatusNotImplemented {
			t.Errorf("GET %s: expected 200 (or 501 when backend lacks support), got %d", d, rr2.Code)
		}
	}

	// The wishlist routes (v1 and v2) MUST return 404 — wishlists are not
	// publications, they're tracked via REST API + MCP only.
	for _, p := range []string{"/opds/wishlist", "/opds/v2/wishlist"} {
		req2 := httptest.NewRequest(http.MethodGet, p, nil)
		rr2 := httptest.NewRecorder()
		srv.ServeHTTP(rr2, req2)
		if rr2.Code != http.StatusNotFound {
			t.Errorf("GET %s: expected 404 (wishlist OPDS feeds removed), got %d", p, rr2.Code)
		}
	}
}

// TestHandleOPDS2Search_AcceptsCanonicalQueryVariable verifies the search
// endpoint reads the spec-canonical ?query= variable.
func TestHandleOPDS2Search_AcceptsCanonicalQueryVariable(t *testing.T) {
	srv := newTestServer(t, Options{})
	uploadBook(t, srv, "hobbit.epub", "The Hobbit", "Tolkien")
	uploadBook(t, srv, "rust.epub", "Rust in Action", "McNamara")

	req := httptest.NewRequest(http.MethodGet, "/opds/v2/search?query=hobb", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("?query=: expected 200, got %d - %s", rr.Code, rr.Body.String())
	}
	var feed opds2.Feed
	if err := json.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if feed.Metadata.NumberOfItems != 1 || len(feed.Publications) != 1 {
		t.Errorf("?query=hobb expected 1 hit, got %d", feed.Metadata.NumberOfItems)
	}
}

// TestHandleOPDS2Search_FallsBackOnLegacyQ verifies the legacy ?q= still
// works (Vue SPA path is unchanged).
func TestHandleOPDS2Search_FallsBackOnLegacyQ(t *testing.T) {
	srv := newTestServer(t, Options{})
	uploadBook(t, srv, "hobbit.epub", "The Hobbit", "Tolkien")

	req := httptest.NewRequest(http.MethodGet, "/opds/v2/search?q=hobb", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("?q=: expected 200, got %d - %s", rr.Code, rr.Body.String())
	}
	var feed opds2.Feed
	if err := json.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if feed.Metadata.NumberOfItems != 1 {
		t.Errorf("?q=hobb expected 1 hit, got %d", feed.Metadata.NumberOfItems)
	}
}

// TestHandleOPDS2_SearchLinkPresentOnSubFeeds verifies the search templated
// link is exposed in `links[]` of every OPDS v2 sub-feed (per OPDS convention
// so the reader's search bar stays visible from any catalogue page).
func TestHandleOPDS2_SearchLinkPresentOnSubFeeds(t *testing.T) {
	srv := newTestServer(t, Options{})
	uploadBook(t, srv, "alpha.epub", "Alpha", "X")

	paths := []string{
		"/opds/v2",
		"/opds/v2/publications",
		"/opds/v2/authors",
		"/opds/v2/tags",
		"/opds/v2/publishers",
		"/opds/v2/unread",
		"/opds/v2/spice",
	}
	for _, p := range paths {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("GET %s: %d", p, rr.Code)
			continue
		}
		var feed opds2.Feed
		if err := json.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
			t.Errorf("%s: invalid JSON: %v", p, err)
			continue
		}
		var found bool
		for _, l := range feed.Links {
			if l.Rel == "search" {
				found = true
				if !l.Templated {
					t.Errorf("%s: search link must be templated", p)
				}
				if !strings.Contains(l.Href, "{?query}") {
					t.Errorf("%s: search href should use canonical {?query}; got %q", p, l.Href)
				}
				break
			}
		}
		if !found {
			t.Errorf("%s: missing rel=search link in links[]; got %+v", p, feed.Links)
		}
	}
}

// TestHandleOPDS2Root_NavigationCompactCollection verifies the OPDS v2 root
// "navigation" field is a Compact Collection per spec
// (https://drafts.opds.io/opds-2.0): a flat list of Link Objects with no
// `rel` (matches the reference catalog https://test.opds.io/2.0/home.json).
// In particular, the legacy `rel:"current"` must NOT appear anywhere in the
// document, and `links[].rel` on self/start/search must remain well-formed.
func TestHandleOPDS2Root_NavigationCompactCollection(t *testing.T) {
	srv := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/opds/v2", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	// (a) Raw JSON must not contain rel:"current" anywhere.
	if strings.Contains(rr.Body.String(), `"current"`) {
		t.Errorf("OPDS v2 root must not emit rel:\"current\"; body=%s", rr.Body.String())
	}

	var feed opds2.Feed
	if err := json.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(feed.Navigation) == 0 {
		t.Fatal("expected at least one navigation item")
	}
	// (b) Every navigation item is a bare Link Object with NO rel set
	// (aligns with test.opds.io/2.0/home.json reference).
	for i, nv := range feed.Navigation {
		if nv.Rel != nil {
			t.Errorf("navigation[%d] %q has rel=%v; spec-aligned compact collection should have no rel", i, nv.Title, nv.Rel)
		}
		if nv.Title == "" {
			t.Errorf("navigation[%d] missing title", i)
		}
		if nv.Href == "" {
			t.Errorf("navigation[%d] %q missing href", i, nv.Title)
		}
	}

	// (c) links[] on the root must keep self/start/search rels.
	var hasSelf, hasStart, hasSearch bool
	for _, l := range feed.Links {
		switch l.Rel {
		case "self":
			hasSelf = true
		case "start":
			hasStart = true
		case "search":
			hasSearch = true
			if !l.Templated {
				t.Errorf("search link must be templated; got %+v", l)
			}
		}
	}
	if !hasSelf || !hasStart || !hasSearch {
		t.Errorf("links[] missing required self/start/search rels; got %+v", feed.Links)
	}
}

// TestHandleSpiceLevels_SixEntries verifies GET /opds/spice returns a
// 6-entry navigation feed whose links carry ?spice=0..5 (exact match) against
// /opds/books.
func TestHandleSpiceLevels_SixEntries(t *testing.T) {
	srv := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/opds/spice", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	if len(feed.Entries) != 6 {
		t.Fatalf("expected 6 spice entries, got %d", len(feed.Entries))
	}
	for i, e := range feed.Entries {
		expectedHref := fmt.Sprintf("/opds/books?spice=%d", i)
		if len(e.Links) == 0 || !strings.Contains(e.Links[0].Href, expectedHref) {
			t.Errorf("entry %d: expected href to contain %q, got %+v", i, expectedHref, e.Links)
		}
	}
}

// TestHandleOPDS2SpiceLevels_SixItems verifies the OPDS v2 spice feed.
func TestHandleOPDS2SpiceLevels_SixItems(t *testing.T) {
	srv := newTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/opds/v2/spice", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var feed opds2.Feed
	if err := json.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(feed.Navigation) != 6 {
		t.Fatalf("expected 6 nav items, got %d", len(feed.Navigation))
	}
	for i, nv := range feed.Navigation {
		expectedHref := fmt.Sprintf("/opds/v2/publications?spice=%d", i)
		if !strings.Contains(nv.Href, expectedHref) {
			t.Errorf("nav item %d: expected href to contain %q, got %q", i, expectedHref, nv.Href)
		}
	}
}

// TestHandleAllBooks_OPDS_SpiceExactFilter verifies the new ?spice=N (exact)
// query parameter is honoured on /opds/books — only the 18+/spice=4 book is
// returned for ?spice=4, and sub-16 titles are excluded.
func TestHandleAllBooks_OPDS_SpiceExactFilter(t *testing.T) {
	srv := newTestServer(t, Options{})
	spicy := uploadBook(t, srv, "spicy.epub", "Spicy Book", "Author A")
	mild := uploadBook(t, srv, "mild.epub", "Mild Book", "Author B")
	child := uploadBook(t, srv, "child.epub", "Child Book", "Author C")

	patch := func(id, body string) {
		req := httptest.NewRequest(http.MethodPatch, "/api/books/"+id, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("patch %s: %d - %s", id, rr.Code, rr.Body.String())
		}
	}
	patch(spicy.ID, `{"ageRating":18,"spiceRating":4}`)
	patch(mild.ID, `{"ageRating":16,"spiceRating":1}`)
	patch(child.ID, `{"ageRating":6}`)

	req := httptest.NewRequest(http.MethodGet, "/opds/books?spice=4", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /opds/books?spice=4: %d", rr.Code)
	}
	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	titles := map[string]bool{}
	for _, e := range feed.Entries {
		titles[e.Title.Value] = true
	}
	if !titles["Spicy Book"] {
		t.Error("OPDS ?spice=4 must include Spicy Book (18+/spice=4)")
	}
	if titles["Mild Book"] {
		t.Error("OPDS ?spice=4 must exclude Mild Book (spice=1)")
	}
	if titles["Child Book"] {
		t.Error("OPDS ?spice=4 must exclude Child Book (sub-16)")
	}
}

// TestHandleSpiceLevels_HiddenForChildProfile verifies that when the request
// is authenticated as a child profile, /opds/spice returns an empty list and
// the root feed omits the spice nav entry — the maxAgeRating already strips
// the relevant titles anyway.
func TestHandleSpiceLevels_HiddenForChildProfile(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if _, err := backend.CreateUser("Admin", "#000", true, false, 0); err != nil {
		t.Fatalf("admin: %v", err)
	}
	kid, err := backend.CreateUser("Kid", "#0f0", false, true, 6)
	if err != nil {
		t.Fatalf("kid: %v", err)
	}
	cookies := loginAsHelper(t, srv, kid.ID)

	// Root feed — spice entry must be absent for the child.
	rootReq := httptest.NewRequest(http.MethodGet, "/opds", nil)
	for _, c := range cookies {
		rootReq.AddCookie(c)
	}
	rootRR := httptest.NewRecorder()
	srv.ServeHTTP(rootRR, rootReq)
	if rootRR.Code != http.StatusOK {
		t.Fatalf("GET /opds: %d", rootRR.Code)
	}
	var rootFeed opds.Feed
	if err := xml.Unmarshal(rootRR.Body.Bytes(), &rootFeed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	for _, e := range rootFeed.Entries {
		if e.ID == "urn:nxt-opds:spice" {
			t.Fatalf("root feed should hide spice entry for child profile; got %+v", e)
		}
	}

	// /opds/spice — should return an empty navigation feed (no levels).
	spiceReq := httptest.NewRequest(http.MethodGet, "/opds/spice", nil)
	for _, c := range cookies {
		spiceReq.AddCookie(c)
	}
	spiceRR := httptest.NewRecorder()
	srv.ServeHTTP(spiceRR, spiceReq)
	if spiceRR.Code != http.StatusOK {
		t.Fatalf("GET /opds/spice: %d", spiceRR.Code)
	}
	var spiceFeed opds.Feed
	if err := xml.Unmarshal(spiceRR.Body.Bytes(), &spiceFeed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	if len(spiceFeed.Entries) != 0 {
		t.Errorf("child profile should see no spice entries; got %d", len(spiceFeed.Entries))
	}
}

// patchBook is a tiny helper that PATCHes a book's metadata via the API and
// fails the test on any non-200 response.
func patchBook(t *testing.T, srv *Server, id, jsonBody string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/books/"+id, strings.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("patch %s: %d - %s", id, rr.Code, rr.Body.String())
	}
}

// findFacetGroup returns the FacetGroup with the given title or nil.
func findFacetGroup(feed *opds2.Feed, title string) *opds2.FacetGroup {
	for i := range feed.Facets {
		if feed.Facets[i].Metadata.Title == title {
			return &feed.Facets[i]
		}
	}
	return nil
}

// findFacetLink returns the Link with the given count-property value, or nil.
func findFacetLinkByCount(fg *opds2.FacetGroup, count int) *opds2.Link {
	for i := range fg.Links {
		if fg.Links[i].Properties != nil && fg.Links[i].Properties.NumberOfItems == count {
			return &fg.Links[i]
		}
	}
	return nil
}

// TestHandleOPDS2Publications_FacetsAge verifies that GET /opds/v2/publications
// exposes a "Classification d'âge" facet group when classified books exist,
// and that each link carries a NumberOfItems property reflecting the bucket
// count after applying the (current query + this facet value).
func TestHandleOPDS2Publications_FacetsAge(t *testing.T) {
	srv := newTestServer(t, Options{})
	// Three books with three different age ratings.
	a := uploadBook(t, srv, "a.epub", "Six Plus", "Author A")
	b := uploadBook(t, srv, "b.epub", "Twelve Plus", "Author B")
	c := uploadBook(t, srv, "c.epub", "Eighteen Plus", "Author C")
	patchBook(t, srv, a.ID, `{"ageRating":6}`)
	patchBook(t, srv, b.ID, `{"ageRating":12}`)
	patchBook(t, srv, c.ID, `{"ageRating":18,"spiceRating":3}`)

	req := httptest.NewRequest(http.MethodGet, "/opds/v2/publications", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /opds/v2/publications: %d", rr.Code)
	}
	var feed opds2.Feed
	if err := json.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	ageGroup := findFacetGroup(&feed, "Classification d'âge")
	if ageGroup == nil {
		t.Fatalf("missing 'Classification d'âge' facet group; got facets=%+v", feed.Facets)
	}
	if len(ageGroup.Links) < 3 {
		t.Fatalf("age facet should have at least 3 links (6+/12+/18+); got %d", len(ageGroup.Links))
	}
	// Every link must carry a NumberOfItems hint and an href that contains age_rating=.
	for i, l := range ageGroup.Links {
		if l.Properties == nil || l.Properties.NumberOfItems <= 0 {
			t.Errorf("age facet link %d (%q) missing NumberOfItems; got %+v", i, l.Title, l.Properties)
		}
		if !strings.Contains(l.Href, "age_rating=") {
			t.Errorf("age facet link %d href should carry age_rating=; got %q", i, l.Href)
		}
	}
	// The "Piment" facet must be present (one 18+/spice=3 book exists).
	if spice := findFacetGroup(&feed, "Piment"); spice == nil {
		t.Errorf("spice facet should exist when at least one 16+/18+ book matches; got nil")
	}
}

// TestHandleOPDS2Publications_FacetsHiddenForChild verifies that the spice
// facet is omitted entirely when the request is authenticated as a child
// profile (MaxAgeRating > 0).  The age facet still appears but only with
// buckets <= MaxAge.
func TestHandleOPDS2Publications_FacetsHiddenForChild(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if _, err := backend.CreateUser("Admin", "#000", true, false, 0); err != nil {
		t.Fatalf("admin: %v", err)
	}
	kid, err := backend.CreateUser("Kid", "#0f0", false, true, 10)
	if err != nil {
		t.Fatalf("kid: %v", err)
	}

	// Add two more books directly via the backend's StoreBook (no HTTP auth).
	storeMin := func(filename, title, author string) string {
		data := buildEPUBBytes(title, author)
		bk, err := backend.StoreBook(filename, io.NopCloser(bytes.NewReader(data)))
		if err != nil {
			t.Fatalf("StoreBook %s: %v", filename, err)
		}
		return bk.ID
	}
	id12 := storeMin("b12.epub", "Twelve Plus", "B")
	id18 := storeMin("b18.epub", "Eighteen Plus", "C")

	// Update metadata directly via backend (admin-equivalent path).
	mustUpdate := func(id string, age, spice int) {
		_, err := backend.UpdateBook(id, catalog.BookUpdate{AgeRating: &age, SpiceRating: &spice})
		if err != nil {
			t.Fatalf("UpdateBook %s: %v", id, err)
		}
	}
	mustUpdate(id12, 12, 0)
	mustUpdate(id18, 18, 3)
	// The pre-existing "Alpha" book gets a 6+ rating.
	allBooks, _, err := backend.AllBooks(0, 10)
	if err != nil {
		t.Fatalf("AllBooks: %v", err)
	}
	for _, bk := range allBooks {
		if bk.Title == "Alpha" {
			six := 6
			zero := 0
			if _, err := backend.UpdateBook(bk.ID, catalog.BookUpdate{AgeRating: &six, SpiceRating: &zero}); err != nil {
				t.Fatalf("UpdateBook alpha: %v", err)
			}
		}
	}

	// Authenticate the request as the child user via its session cookie.
	cookies := loginAsHelper(t, srv, kid.ID)
	req := httptest.NewRequest(http.MethodGet, "/opds/v2/publications", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /opds/v2/publications (child): %d - %s", rr.Code, rr.Body.String())
	}
	var feed opds2.Feed
	if err := json.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Spice facet must be absent.
	if spice := findFacetGroup(&feed, "Piment"); spice != nil {
		t.Errorf("child profile must NOT see spice facet; got %+v", spice)
	}
	// Age facet must NOT expose buckets above MaxAge=10.
	ageGroup := findFacetGroup(&feed, "Classification d'âge")
	if ageGroup == nil {
		t.Fatalf("age facet should still exist for child profile")
	}
	for _, l := range ageGroup.Links {
		if strings.Contains(l.Href, "age_rating=12") || strings.Contains(l.Href, "age_rating=16") || strings.Contains(l.Href, "age_rating=18") {
			t.Errorf("child profile (MaxAge=10) must not see facet for ages above 10; got %q", l.Href)
		}
	}
}

// TestHandleOPDS2TagBooks_FacetsPreserveTag verifies that facets on a
// scoped feed (here /opds/v2/tags/{name}) build hrefs that point back to
// the same scoped path — the tag scope survives.  Counts must reflect the
// scoped subset, not the global library.
func TestHandleOPDS2TagBooks_FacetsPreserveTag(t *testing.T) {
	srv := newTestServer(t, Options{})
	a := uploadBook(t, srv, "a.epub", "Tagged Six", "Author A")
	b := uploadBook(t, srv, "b.epub", "Tagged Twelve", "Author B")
	other := uploadBook(t, srv, "c.epub", "Untagged Six", "Author C")
	patchBook(t, srv, a.ID, `{"ageRating":6,"tags":["Fantasy"]}`)
	patchBook(t, srv, b.ID, `{"ageRating":12,"tags":["Fantasy"]}`)
	patchBook(t, srv, other.ID, `{"ageRating":6,"tags":["Horror"]}`)

	req := httptest.NewRequest(http.MethodGet, "/opds/v2/tags/Fantasy", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /opds/v2/tags/Fantasy: %d - %s", rr.Code, rr.Body.String())
	}
	var feed opds2.Feed
	if err := json.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	ageGroup := findFacetGroup(&feed, "Classification d'âge")
	if ageGroup == nil {
		t.Fatalf("expected age facet on scoped feed; got %+v", feed.Facets)
	}
	// Each facet href should point back to /opds/v2/tags/Fantasy.
	var foundSix, foundTwelve bool
	for _, l := range ageGroup.Links {
		if !strings.HasPrefix(l.Href, "/opds/v2/tags/Fantasy") {
			t.Errorf("facet href must preserve tag scope; got %q", l.Href)
		}
		if strings.Contains(l.Href, "age_rating=6") && l.Properties.NumberOfItems == 1 {
			foundSix = true
		}
		if strings.Contains(l.Href, "age_rating=12") && l.Properties.NumberOfItems == 1 {
			foundTwelve = true
		}
	}
	if !foundSix {
		t.Errorf("expected age_rating=6 facet with count=1 (the only Fantasy 6+); got %+v", ageGroup.Links)
	}
	if !foundTwelve {
		t.Errorf("expected age_rating=12 facet with count=1; got %+v", ageGroup.Links)
	}
	// "Untagged Six" must NOT contribute to the Fantasy scope's count.
	for _, l := range ageGroup.Links {
		if strings.Contains(l.Href, "age_rating=6") && l.Properties.NumberOfItems > 1 {
			t.Errorf("Fantasy scope should report 1 for age_rating=6, not %d", l.Properties.NumberOfItems)
		}
	}
}

// TestHandleOPDS2Publications_FacetCountMatchesSearch verifies that for each
// age facet link, navigating to its href produces the same count as the
// facet's NumberOfItems property (round-trip consistency).
func TestHandleOPDS2Publications_FacetCountMatchesSearch(t *testing.T) {
	srv := newTestServer(t, Options{})
	a := uploadBook(t, srv, "a.epub", "Six A", "Author A")
	b := uploadBook(t, srv, "b.epub", "Six B", "Author B")
	c := uploadBook(t, srv, "c.epub", "Twelve C", "Author C")
	patchBook(t, srv, a.ID, `{"ageRating":6}`)
	patchBook(t, srv, b.ID, `{"ageRating":6}`)
	patchBook(t, srv, c.ID, `{"ageRating":12}`)

	req := httptest.NewRequest(http.MethodGet, "/opds/v2/publications", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /opds/v2/publications: %d", rr.Code)
	}
	var feed opds2.Feed
	if err := json.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	age := findFacetGroup(&feed, "Classification d'âge")
	if age == nil {
		t.Fatalf("missing age facet")
	}
	// Find the 6+ link and assert NumberOfItems==2 and a follow-up call returns 2 publications.
	six := findFacetLinkByCount(age, 2)
	if six == nil || !strings.Contains(six.Href, "age_rating=6") {
		t.Fatalf("expected age_rating=6 link with count=2; got %+v", age.Links)
	}

	// Follow the facet href.
	req2 := httptest.NewRequest(http.MethodGet, six.Href, nil)
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("follow facet href %q: %d", six.Href, rr2.Code)
	}
	var follow opds2.Feed
	if err := json.Unmarshal(rr2.Body.Bytes(), &follow); err != nil {
		t.Fatalf("follow invalid JSON: %v", err)
	}
	if follow.Metadata.NumberOfItems != 2 {
		t.Errorf("facet round-trip: expected 2 publications, got %d", follow.Metadata.NumberOfItems)
	}
}

// findGroup returns the first Group whose metadata title matches.
func findGroup(feed *opds2.Feed, title string) *opds2.Group {
	for i := range feed.Groups {
		if feed.Groups[i].Metadata.Title == title {
			return &feed.Groups[i]
		}
	}
	return nil
}

// TestHandleOPDS2Root_GroupsLatestOnEmptyLib verifies that on a freshly
// initialized single-user install the root feed exposes only the "Derniers
// ajoutés" group (alpha.epub is pre-populated by newSQLiteTestServer) and
// the to-read / recos / unread groups are absent because they require
// state that doesn't exist yet.
func TestHandleOPDS2Root_GroupsLatestOnEmptyLib(t *testing.T) {
	srv, _, _ := newSQLiteTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/opds/v2", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /opds/v2: %d - %s", rr.Code, rr.Body.String())
	}
	var feed opds2.Feed
	if err := json.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(feed.Groups) == 0 {
		t.Fatalf("expected at least one group; got %+v", feed.Groups)
	}
	latest := findGroup(&feed, "Derniers ajoutés")
	if latest == nil {
		t.Fatalf("missing 'Derniers ajoutés' group; got %+v", feed.Groups)
	}
	if latest.Metadata.NumberOfItems == 0 {
		t.Errorf("'Derniers ajoutés' NumberOfItems should be > 0; got %d", latest.Metadata.NumberOfItems)
	}
	if len(latest.Publications) == 0 {
		t.Errorf("'Derniers ajoutés' should have publications; got 0")
	}
	if len(latest.Links) == 0 || latest.Links[0].Rel != "self" {
		t.Errorf("'Derniers ajoutés' must have a links[0].rel=self link; got %+v", latest.Links)
	}
	if !strings.Contains(latest.Links[0].Href, "/opds/v2/publications") {
		t.Errorf("'Derniers ajoutés' self link should point to /opds/v2/publications; got %q", latest.Links[0].Href)
	}
	// To-read / unread / recos must NOT appear (no user identified, no pile, no recos).
	if g := findGroup(&feed, "Pile à lire"); g != nil {
		t.Errorf("anonymous request must not see 'Pile à lire' group; got %+v", g)
	}
	if g := findGroup(&feed, "Recommandations reçues"); g != nil {
		t.Errorf("anonymous request must not see 'Recommandations reçues' group; got %+v", g)
	}
	// Spec compliance: every group must have either Navigation or Publications.
	for i, g := range feed.Groups {
		if len(g.Navigation) == 0 && len(g.Publications) == 0 {
			t.Errorf("group[%d] %q violates spec: neither navigation nor publications", i, g.Metadata.Title)
		}
	}
}

// TestHandleOPDS2Root_GroupsToReadForUser verifies that an authenticated
// user with books in her pile sees a "Pile à lire" acquisition group on
// the root feed, with NumberOfItems = total pile size (not bounded by the
// carousel limit).
func TestHandleOPDS2Root_GroupsToReadForUser(t *testing.T) {
	srv, backend, bookID := newSQLiteTestServer(t, Options{Password: "pw"})
	alice, err := backend.CreateUser("Alice", "#ff0000", false, false, 0)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := backend.AddToReadList(alice.ID, bookID); err != nil {
		t.Fatalf("AddToReadList: %v", err)
	}

	cookies := loginAsHelper(t, srv, alice.ID)
	req := httptest.NewRequest(http.MethodGet, "/opds/v2", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /opds/v2: %d - %s", rr.Code, rr.Body.String())
	}
	var feed opds2.Feed
	if err := json.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	pile := findGroup(&feed, "Pile à lire")
	if pile == nil {
		t.Fatalf("missing 'Pile à lire' group; got %+v", feed.Groups)
	}
	if pile.Metadata.NumberOfItems != 1 {
		t.Errorf("Pile à lire NumberOfItems: expected 1, got %d", pile.Metadata.NumberOfItems)
	}
	if len(pile.Publications) != 1 {
		t.Errorf("Pile à lire publications: expected 1, got %d", len(pile.Publications))
	}
	if len(pile.Links) == 0 || pile.Links[0].Rel != "self" {
		t.Errorf("Pile à lire must have self link; got %+v", pile.Links)
	}
	if !strings.Contains(pile.Links[0].Href, "/opds/v2/to-read") {
		t.Errorf("Pile à lire self link should point to /opds/v2/to-read; got %q", pile.Links[0].Href)
	}
}

// TestHandleOPDS2Root_GroupsNoEmptyEntries verifies that no Group ever
// appears without Publications or Navigation (OPDS 2.0 §2.5 requires
// each Group to be one or the other).
func TestHandleOPDS2Root_GroupsNoEmptyEntries(t *testing.T) {
	srv, _, _ := newSQLiteTestServer(t, Options{})
	req := httptest.NewRequest(http.MethodGet, "/opds/v2", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /opds/v2: %d - %s", rr.Code, rr.Body.String())
	}
	var feed opds2.Feed
	if err := json.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for i, g := range feed.Groups {
		if len(g.Publications) == 0 && len(g.Navigation) == 0 {
			t.Errorf("group[%d] %q is empty (neither navigation nor publications)", i, g.Metadata.Title)
		}
		if g.Metadata.Title == "" {
			t.Errorf("group[%d] missing metadata.title", i)
		}
	}
}

// TestBookToPublication_CantookConformity verifies that the JSON shape
// returned for an OPDS 2.0 acquisition feed matches the conventions
// expected by Cantook / Aldiko / Feedbooks-style readers (per the
// reference catalog https://test.opds.io/2.0/home.json):
//
//   - images[] entries carry NO `rel` (legacy OPDS 1 image rels dropped).
//   - acquisition links use `rel = "http://opds-spec.org/acquisition/open-access"`
//     (Cantook then renders a direct "Télécharger" button).
//   - All emitted URLs (images + acquisition) are ABSOLUTE — they start
//     with the request's external base URL (http://host).
func TestBookToPublication_CantookConformity(t *testing.T) {
	srv := newTestServer(t, Options{})
	uploadBook(t, srv, "alpha.epub", "Alpha", "Author A")

	req := httptest.NewRequest(http.MethodGet, "/opds/v2/publications", nil)
	req.Host = "books.example.com"
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /opds/v2/publications: %d - %s", rr.Code, rr.Body.String())
	}
	var feed opds2.Feed
	if err := json.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(feed.Publications) == 0 {
		t.Fatalf("expected at least one publication")
	}
	pub := feed.Publications[0]

	// (a) images[] entries must have NO rel.
	for i, img := range pub.Images {
		if img.Rel != nil {
			t.Errorf("images[%d] must not carry rel; got %v", i, img.Rel)
		}
		if img.Href == "" {
			t.Errorf("images[%d] missing href", i)
			continue
		}
		// (c) URL must be absolute.
		if !strings.HasPrefix(img.Href, "http://") && !strings.HasPrefix(img.Href, "https://") {
			t.Errorf("images[%d] href must be absolute (start with http(s)://); got %q", i, img.Href)
		}
	}

	// (b) acquisition rel must be open-access.
	var acqCount int
	for _, l := range pub.Links {
		if l.Rel == "http://opds-spec.org/acquisition/open-access" {
			acqCount++
			if !strings.HasPrefix(l.Href, "http://") && !strings.HasPrefix(l.Href, "https://") {
				t.Errorf("acquisition link must have an absolute href; got %q", l.Href)
			}
			// Must NOT carry the legacy rel.
		}
		if l.Rel == "http://opds-spec.org/acquisition" {
			t.Errorf("legacy bare 'acquisition' rel should be replaced by open-access; got %+v", l)
		}
	}
	if acqCount == 0 {
		t.Errorf("expected at least one acquisition link with rel=open-access; got %+v", pub.Links)
	}
}

// TestAbsURL_LeavesAlreadyAbsoluteUnchanged is a tiny unit test on the
// absURL helper to make sure already-absolute URLs survive untouched
// (otherwise a future caller that hands an external URL would get a
// double prefix).
func TestAbsURL_LeavesAlreadyAbsoluteUnchanged(t *testing.T) {
	cases := []struct{ base, in, want string }{
		{"https://x.example", "/covers/abc", "https://x.example/covers/abc"},
		{"https://x.example", "https://cdn/y.jpg", "https://cdn/y.jpg"},
		{"https://x.example", "http://other/y.jpg", "http://other/y.jpg"},
		{"https://x.example", "data:image/png;base64,xx", "data:image/png;base64,xx"},
		{"", "/covers/abc", "/covers/abc"},
		{"https://x.example", "covers/abc", "https://x.example/covers/abc"},
		{"https://x.example", "", ""},
	}
	for _, c := range cases {
		got := absURL(c.base, c.in)
		if got != c.want {
			t.Errorf("absURL(%q, %q) = %q; want %q", c.base, c.in, got, c.want)
		}
	}
}

// TestHandleOPDS2Root_GroupsHideAdultForChild verifies that a child user
// never sees a 16+/18+ book in any root Group — the MaxAgeRating filter
// flows through to Search() and the to-read post-filter clips items too.
func TestHandleOPDS2Root_GroupsHideAdultForChild(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if _, err := backend.CreateUser("Admin", "#000", true, false, 0); err != nil {
		t.Fatalf("admin: %v", err)
	}
	kid, err := backend.CreateUser("Kid", "#0f0", false, true, 10)
	if err != nil {
		t.Fatalf("kid: %v", err)
	}

	// Add an 18+ book directly via backend.
	data := buildEPUBBytes("Spicy", "Adult Author")
	adult, err := backend.StoreBook("spicy.epub", io.NopCloser(bytes.NewReader(data)))
	if err != nil {
		t.Fatalf("StoreBook: %v", err)
	}
	eighteen := 18
	if _, err := backend.UpdateBook(adult.ID, catalog.BookUpdate{AgeRating: &eighteen}); err != nil {
		t.Fatalf("UpdateBook: %v", err)
	}

	cookies := loginAsHelper(t, srv, kid.ID)
	req := httptest.NewRequest(http.MethodGet, "/opds/v2", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /opds/v2 (child): %d - %s", rr.Code, rr.Body.String())
	}
	var feed opds2.Feed
	if err := json.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// Walk every group's publications and ensure no title equals "Spicy".
	for _, g := range feed.Groups {
		for _, pub := range g.Publications {
			if pub.Metadata.Title == "Spicy" {
				t.Errorf("child profile must not see adult book in group %q", g.Metadata.Title)
			}
		}
	}
}
