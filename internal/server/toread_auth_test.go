package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqlitebackend "github.com/banux/nxt-opds/internal/backend/sqlite"
	"github.com/banux/nxt-opds/internal/opds"
)

// newSQLiteTestServer creates a Server backed by a SQLite catalog (which
// implements UserManager + ToReadManager) and pre-populates it with a single
// minimal EPUB so the to-read list has content to work with.
func newSQLiteTestServer(t *testing.T, opts Options) (*Server, *sqlitebackend.Backend, string) {
	t.Helper()
	dir := t.TempDir()
	writeMinimalEPUB(t, filepath.Join(dir, "alpha.epub"), "Alpha", "Jane Doe")

	backend, err := sqlitebackend.New(dir)
	if err != nil {
		t.Fatalf("sqlitebackend.New: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })

	srv := New(backend, opts)
	books, _, err := backend.AllBooks(0, 10)
	if err != nil || len(books) == 0 {
		t.Fatalf("AllBooks: %v (got %d books)", err, len(books))
	}
	return srv, backend, books[0].ID
}

// writeMinimalEPUB writes a tiny valid EPUB to path with the given metadata.
func writeMinimalEPUB(t *testing.T, path, title, author string) {
	t.Helper()
	containerXML := `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`
	contentOPF := `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>` + title + `</dc:title>
    <dc:creator>` + author + `</dc:creator>
    <dc:language>en</dc:language>
  </metadata>
  <manifest><item id="x" href="x.html" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="x"/></spine>
</package>`
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := map[string]string{
		"mimetype":              "application/epub+zip",
		"META-INF/container.xml": containerXML,
		"content.opf":           contentOPF,
		"x.html":                "<html><body>x</body></html>",
	}
	for name, content := range files {
		fw, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %q: %v", name, err)
		}
		if _, err := io.Copy(fw, strings.NewReader(content)); err != nil {
			t.Fatalf("zip write %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

// ─── Single-user mode ────────────────────────────────────────────────────────

// TestToRead_OPDS_SingleUserMode_NoUserParam_Returns200 verifies that when no
// users are registered, the to-read OPDS feed responds 200 (with an empty
// list) for a token-only request without ?user=.  Previously the handler
// rejected an empty session userID with 401 even in single-user mode.
func TestToRead_OPDS_SingleUserMode_NoUserParam_Returns200(t *testing.T) {
	srv, _, _ := newSQLiteTestServer(t, Options{Password: "pw", OPDSToken: "tok"})

	req := httptest.NewRequest(http.MethodGet, "/opds/to-read?token=tok", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	if len(feed.Entries) != 0 {
		t.Errorf("expected empty pile in single-user mode, got %d entries", len(feed.Entries))
	}
}

// ─── Multi-user mode ─────────────────────────────────────────────────────────

// TestToRead_OPDS_MultiUser_NoUser_Returns401 verifies that in multi-user
// mode, a token-authenticated request without ?user= is rejected with 401.
func TestToRead_OPDS_MultiUser_NoUser_Returns401(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw", OPDSToken: "tok"})
	if _, err := backend.CreateUser("Alice", "#ff0000", false, false, 0); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/opds/to-read?token=tok", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 in multi-user mode without ?user=, got %d", rr.Code)
	}
}

// TestToRead_OPDS_MultiUser_WithUserParam_Returns200 verifies that a
// token-authenticated request with a valid ?user= parameter succeeds and
// returns that user's pile.
func TestToRead_OPDS_MultiUser_WithUserParam_Returns200(t *testing.T) {
	srv, backend, bookID := newSQLiteTestServer(t, Options{Password: "pw", OPDSToken: "tok"})
	alice, err := backend.CreateUser("Alice", "#ff0000", false, false, 0)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := backend.AddToReadList(alice.ID, bookID); err != nil {
		t.Fatalf("AddToReadList: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/opds/to-read?token=tok&user="+alice.ID, nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	if len(feed.Entries) != 1 {
		t.Errorf("expected 1 entry in Alice's pile, got %d", len(feed.Entries))
	}
}

// TestToRead_OPDS_MultiUser_UnknownUser_Returns401 verifies that an unknown
// ?user= ID is rejected (not silently treated as empty).
func TestToRead_OPDS_MultiUser_UnknownUser_Returns401(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw", OPDSToken: "tok"})
	if _, err := backend.CreateUser("Alice", "#ff0000", false, false, 0); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/opds/to-read?token=tok&user=does-not-exist", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unknown user, got %d", rr.Code)
	}
}

// ─── OPDS root navigation ────────────────────────────────────────────────────

// TestToRead_OPDSRoot_MultiUser_EmitsPerUserEntries verifies that the OPDS v1
// root navigation feed emits one "Pile à lire de <name>" entry per user when
// the request is authenticated via OPDS token (no session userID).
func TestToRead_OPDSRoot_MultiUser_EmitsPerUserEntries(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw", OPDSToken: "tok"})
	alice, err := backend.CreateUser("Alice", "#ff0000", false, false, 0)
	if err != nil {
		t.Fatalf("CreateUser Alice: %v", err)
	}
	bob, err := backend.CreateUser("Bob", "#00ff00", false, false, 0)
	if err != nil {
		t.Fatalf("CreateUser Bob: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/opds?token=tok", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}

	gotAlice, gotBob := false, false
	for _, entry := range feed.Entries {
		if !strings.HasPrefix(entry.Title.Value, "Pile à lire de ") {
			continue
		}
		for _, link := range entry.Links {
			if !strings.Contains(link.Href, "token=tok") {
				t.Errorf("entry %q link missing token: %s", entry.Title.Value, link.Href)
			}
			if strings.Contains(link.Href, "user="+alice.ID) {
				gotAlice = true
			}
			if strings.Contains(link.Href, "user="+bob.ID) {
				gotBob = true
			}
		}
	}
	if !gotAlice {
		t.Error("expected a 'Pile à lire de Alice' entry with user=<alice.ID>")
	}
	if !gotBob {
		t.Error("expected a 'Pile à lire de Bob' entry with user=<bob.ID>")
	}
}

// TestToRead_OPDSRoot_SingleUser_EmitsGenericEntry verifies that in
// single-user mode (no users) a single generic "Pile à lire" entry is emitted.
func TestToRead_OPDSRoot_SingleUser_EmitsGenericEntry(t *testing.T) {
	srv, _, _ := newSQLiteTestServer(t, Options{Password: "pw", OPDSToken: "tok"})

	req := httptest.NewRequest(http.MethodGet, "/opds?token=tok", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}

	matches := 0
	for _, entry := range feed.Entries {
		if entry.Title.Value == "Pile à lire" {
			matches++
		}
	}
	if matches != 1 {
		t.Errorf("expected exactly 1 generic 'Pile à lire' entry in single-user mode, got %d", matches)
	}
}

// ─── API handlers ────────────────────────────────────────────────────────────

// TestToRead_API_SingleUserMode_NoUserParam_Returns200 verifies that the API
// list endpoint works in single-user mode without a ?user= parameter (this
// was the second 401 case the helper resolves).
func TestToRead_API_SingleUserMode_NoUserParam_Returns200(t *testing.T) {
	srv, _, _ := newSQLiteTestServer(t, Options{}) // no auth required

	req := httptest.NewRequest(http.MethodGet, "/api/to-read", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var items []toReadItemJSON
	if err := json.NewDecoder(rr.Body).Decode(&items); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected empty list, got %d items", len(items))
	}
}
