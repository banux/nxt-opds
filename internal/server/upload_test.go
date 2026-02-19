package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	fsbackend "github.com/banux/nxt-opds/internal/backend/fs"
	"github.com/banux/nxt-opds/internal/catalog"
)

// buildEPUBBytes returns the raw bytes of a minimal valid EPUB.
func buildEPUBBytes(title, author string) []byte {
	containerXML := `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`

	contentOPF := `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>` + title + `</dc:title>
    <dc:creator>` + author + `</dc:creator>
    <dc:language>en</dc:language>
  </metadata>
</package>`

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, entry := range []struct{ name, body string }{
		{"META-INF/container.xml", containerXML},
		{"content.opf", contentOPF},
	} {
		f, _ := w.Create(entry.name)
		_, _ = f.Write([]byte(entry.body))
	}
	_ = w.Close()
	return buf.Bytes()
}

// buildMultipartBody creates a multipart/form-data body with a single file field.
func buildMultipartBody(t *testing.T, fieldName, filename string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile(fieldName, filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	_ = mw.Close()
	return &body, mw.FormDataContentType()
}

func TestHandleUpload_Success(t *testing.T) {
	dir := t.TempDir()
	backend, err := fsbackend.New(dir)
	if err != nil {
		t.Fatalf("backend.New: %v", err)
	}
	srv := New(backend, Options{})

	epubData := buildEPUBBytes("Uploaded Book", "Upload Author")
	body, ct := buildMultipartBody(t, "file", "uploaded.epub", epubData)

	req := httptest.NewRequest(http.MethodPost, "/api/upload", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", rr.Code, rr.Body.String())
	}

	var book catalog.Book
	if err := json.NewDecoder(rr.Body).Decode(&book); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if book.Title != "Uploaded Book" {
		t.Errorf("title: got %q, want %q", book.Title, "Uploaded Book")
	}
	if len(book.Authors) == 0 || book.Authors[0].Name != "Upload Author" {
		t.Errorf("author: got %v, want Upload Author", book.Authors)
	}

	// Verify file was persisted
	if _, err := os.Stat(filepath.Join(dir, "uploaded.epub")); os.IsNotExist(err) {
		t.Error("uploaded file not found on disk")
	}

	// Verify book is now in catalog
	books, total, _ := backend.AllBooks(0, 50)
	if total != 1 {
		t.Errorf("catalog total: got %d, want 1", total)
	}
	if len(books) > 0 && books[0].ID != book.ID {
		t.Errorf("book ID mismatch: catalog=%q, response=%q", books[0].ID, book.ID)
	}
}

func TestHandleUpload_UnsupportedType(t *testing.T) {
	dir := t.TempDir()
	backend, _ := fsbackend.New(dir)
	srv := New(backend, Options{})

	body, ct := buildMultipartBody(t, "file", "document.txt", []byte("hello"))

	req := httptest.NewRequest(http.MethodPost, "/api/upload", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", rr.Code)
	}
}

func TestHandleUpload_MissingField(t *testing.T) {
	dir := t.TempDir()
	backend, _ := fsbackend.New(dir)
	srv := New(backend, Options{})

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("other", "value")
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestHandleUpload_Duplicate(t *testing.T) {
	dir := t.TempDir()
	backend, _ := fsbackend.New(dir)
	srv := New(backend, Options{})

	epubData := buildEPUBBytes("Dup Book", "Dup Author")

	upload := func() int {
		body, ct := buildMultipartBody(t, "file", "dup.epub", epubData)
		req := httptest.NewRequest(http.MethodPost, "/api/upload", body)
		req.Header.Set("Content-Type", ct)
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr.Code
	}

	if code := upload(); code != http.StatusCreated {
		t.Fatalf("first upload: expected 201, got %d", code)
	}
	if code := upload(); code != http.StatusUnprocessableEntity {
		t.Errorf("duplicate upload: expected 422, got %d", code)
	}
}

func TestHandleDownload_Success(t *testing.T) {
	dir := t.TempDir()
	backend, _ := fsbackend.New(dir)
	srv := New(backend, Options{})

	// Upload a book first
	epubData := buildEPUBBytes("Download Me", "DL Author")
	body, ct := buildMultipartBody(t, "file", "dl.epub", epubData)
	req := httptest.NewRequest(http.MethodPost, "/api/upload", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("upload failed: %d %s", rr.Code, rr.Body.String())
	}

	var book catalog.Book
	_ = json.NewDecoder(rr.Body).Decode(&book)

	// Request the download
	dlURL := "/opds/books/" + book.ID + "/download"
	req2 := httptest.NewRequest(http.MethodGet, dlURL, nil)
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Errorf("download: expected 200, got %d: %s", rr2.Code, rr2.Body.String())
	}
	if ct := rr2.Header().Get("Content-Type"); ct != "application/epub+zip" {
		t.Errorf("Content-Type: got %q, want %q", ct, "application/epub+zip")
	}
}
