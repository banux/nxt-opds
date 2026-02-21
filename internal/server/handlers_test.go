package server

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	fsbackend "github.com/banux/nxt-opds/internal/backend/fs"
	"github.com/banux/nxt-opds/internal/catalog"
	"github.com/banux/nxt-opds/internal/opds"
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
