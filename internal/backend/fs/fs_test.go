package fs

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/banux/nxt-opds/internal/catalog"
	"github.com/banux/nxt-opds/internal/epub"
)

// createMinimalEPUB writes a valid minimal EPUB file to path.
// The EPUB contains a container.xml pointing to content.opf with basic metadata.
func createMinimalEPUB(t *testing.T, path, title, author, subject string) {
	t.Helper()

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
    <dc:subject>` + subject + `</dc:subject>
    <dc:language>en</dc:language>
    <dc:date>2024-01-01</dc:date>
  </metadata>
</package>`

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	addFile := func(name, content string) {
		f, err := w.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %q: %v", name, err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatalf("write zip entry %q: %v", name, err)
		}
	}

	addFile("META-INF/container.xml", containerXML)
	addFile("content.opf", contentOPF)

	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}

	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatalf("write epub file: %v", err)
	}
}

func TestBackend_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	books, total, err := b.AllBooks(0, 50)
	if err != nil {
		t.Fatalf("AllBooks() error: %v", err)
	}
	if total != 0 {
		t.Errorf("expected 0 books, got %d", total)
	}
	if len(books) != 0 {
		t.Errorf("expected empty books slice, got %d", len(books))
	}
}

func TestBackend_SingleEPUB(t *testing.T) {
	dir := t.TempDir()
	epubPath := filepath.Join(dir, "test.epub")
	createMinimalEPUB(t, epubPath, "Test Book", "Test Author", "Fiction")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	books, total, err := b.AllBooks(0, 50)
	if err != nil {
		t.Fatalf("AllBooks() error: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected 1 book, got %d", total)
	}

	bk := books[0]
	if bk.Title != "Test Book" {
		t.Errorf("title: got %q, want %q", bk.Title, "Test Book")
	}
	if len(bk.Authors) != 1 || bk.Authors[0].Name != "Test Author" {
		t.Errorf("authors: got %v, want [{Test Author}]", bk.Authors)
	}
	if len(bk.Tags) != 1 || bk.Tags[0] != "Fiction" {
		t.Errorf("tags: got %v, want [Fiction]", bk.Tags)
	}
	if bk.Language != "en" {
		t.Errorf("language: got %q, want %q", bk.Language, "en")
	}
}

func TestBackend_BookByID(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "book.epub"), "My Book", "An Author", "")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	books, _, _ := b.AllBooks(0, 50)
	if len(books) == 0 {
		t.Fatal("no books found")
	}

	id := books[0].ID
	bk, err := b.BookByID(id)
	if err != nil {
		t.Fatalf("BookByID(%q) error: %v", id, err)
	}
	if bk.ID != id {
		t.Errorf("BookByID returned wrong ID: %q", bk.ID)
	}

	_, err = b.BookByID("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent ID, got nil")
	}
}

func TestBackend_Search(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "go.epub"), "Learning Go", "John Doe", "Programming")
	createMinimalEPUB(t, filepath.Join(dir, "python.epub"), "Python Cookbook", "Jane Smith", "Programming")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	books, total, err := b.Search(catalog.SearchQuery{Query: "go", Limit: 50})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	// "Learning Go" matches "go" in title
	if total != 1 {
		t.Errorf("search 'go': expected 1 result, got %d", total)
	}
	if len(books) > 0 && books[0].Title != "Learning Go" {
		t.Errorf("expected 'Learning Go', got %q", books[0].Title)
	}
}

func TestBackend_SearchBySeries(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "a.epub"), "Book A", "Author A", "")
	createMinimalEPUB(t, filepath.Join(dir, "b.epub"), "Book B", "Author B", "")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	// Set series on book A via UpdateBook.
	series := "Dune"
	books, _, _ := b.Search(catalog.SearchQuery{Limit: 50})
	if len(books) < 2 {
		t.Fatalf("expected 2 books, got %d", len(books))
	}
	var bookAID string
	for _, bk := range books {
		if bk.Title == "Book A" {
			bookAID = bk.ID
		}
	}
	if _, err := b.UpdateBook(bookAID, catalog.BookUpdate{Series: &series}); err != nil {
		t.Fatalf("UpdateBook() error: %v", err)
	}

	results, total, err := b.Search(catalog.SearchQuery{Query: "dune", Limit: 50})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if total != 1 {
		t.Errorf("search 'dune': expected 1 result, got %d", total)
	}
	if len(results) > 0 && results[0].Title != "Book A" {
		t.Errorf("expected 'Book A', got %q", results[0].Title)
	}
}

func TestBackend_Search_SeriesSizeFilter(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "solo.epub"), "Solo Title", "Author S", "")
	createMinimalEPUB(t, filepath.Join(dir, "duo1.epub"), "Duo One", "Author D", "")
	createMinimalEPUB(t, filepath.Join(dir, "duo2.epub"), "Duo Two", "Author D", "")
	createMinimalEPUB(t, filepath.Join(dir, "saga1.epub"), "Saga One", "Author M", "")
	createMinimalEPUB(t, filepath.Join(dir, "saga2.epub"), "Saga Two", "Author M", "")
	createMinimalEPUB(t, filepath.Join(dir, "saga3.epub"), "Saga Three", "Author M", "")
	createMinimalEPUB(t, filepath.Join(dir, "saga4.epub"), "Saga Four", "Author M", "")
	createMinimalEPUB(t, filepath.Join(dir, "saga5.epub"), "Saga Five", "Author M", "")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	duo := "Duo Series"
	saga := "Saga Series"
	all, _, _ := b.Search(catalog.SearchQuery{Limit: 50})
	for _, bk := range all {
		switch bk.Title {
		case "Duo One", "Duo Two":
			if _, err := b.UpdateBook(bk.ID, catalog.BookUpdate{Series: &duo}); err != nil {
				t.Fatalf("UpdateBook Duo: %v", err)
			}
		case "Saga One", "Saga Two", "Saga Three", "Saga Four", "Saga Five":
			if _, err := b.UpdateBook(bk.ID, catalog.BookUpdate{Series: &saga}); err != nil {
				t.Fatalf("UpdateBook Saga: %v", err)
			}
		}
	}

	cases := []struct {
		name  string
		size  string
		count int
	}{
		{"standalone", "standalone", 1},
		{"short", "short", 2},
		{"medium", "medium", 5},
		{"long", "long", 0},
	}
	for _, tc := range cases {
		_, total, err := b.Search(catalog.SearchQuery{SeriesSize: tc.size, Limit: 50})
		if err != nil {
			t.Fatalf("%s: Search: %v", tc.name, err)
		}
		if total != tc.count {
			t.Errorf("%s: expected %d books, got %d", tc.name, tc.count, total)
		}
	}
}

func TestBackend_AuthorsAndTags(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "a.epub"), "Book A", "Author One", "SciFi")
	createMinimalEPUB(t, filepath.Join(dir, "b.epub"), "Book B", "Author Two", "Fantasy")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	authors, total, err := b.Authors(0, 50)
	if err != nil {
		t.Fatalf("Authors() error: %v", err)
	}
	if total != 2 {
		t.Errorf("expected 2 authors, got %d", total)
	}
	_ = authors

	tags, total, err := b.Tags(0, 50)
	if err != nil {
		t.Fatalf("Tags() error: %v", err)
	}
	if total != 2 {
		t.Errorf("expected 2 tags, got %d", total)
	}
	_ = tags
}

func TestBackend_BooksByAuthor(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "a.epub"), "Book A", "Common Author", "")
	createMinimalEPUB(t, filepath.Join(dir, "b.epub"), "Book B", "Common Author", "")
	createMinimalEPUB(t, filepath.Join(dir, "c.epub"), "Book C", "Other Author", "")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	books, total, err := b.BooksByAuthor("Common Author", 0, 50)
	if err != nil {
		t.Fatalf("BooksByAuthor() error: %v", err)
	}
	if total != 2 {
		t.Errorf("expected 2 books by 'Common Author', got %d", total)
	}
	_ = books
}

func TestBackend_Pagination(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		name := "book" + string(rune('A'+i)) + ".epub"
		createMinimalEPUB(t, filepath.Join(dir, name), "Book "+string(rune('A'+i)), "Author", "")
	}

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	_, total, _ := b.AllBooks(0, 100)
	if total != 5 {
		t.Fatalf("expected 5 books total, got %d", total)
	}

	page1, _, _ := b.AllBooks(0, 2)
	if len(page1) != 2 {
		t.Errorf("page1: expected 2 books, got %d", len(page1))
	}

	page2, _, _ := b.AllBooks(2, 2)
	if len(page2) != 2 {
		t.Errorf("page2: expected 2 books, got %d", len(page2))
	}

	page3, _, _ := b.AllBooks(4, 2)
	if len(page3) != 1 {
		t.Errorf("page3: expected 1 book, got %d", len(page3))
	}
}

func TestPathToID_Stable(t *testing.T) {
	id1 := epub.PathToID("/some/path/book.epub")
	id2 := epub.PathToID("/some/path/book.epub")
	if id1 != id2 {
		t.Errorf("PathToID is not stable: %q != %q", id1, id2)
	}

	id3 := epub.PathToID("/other/path/book.epub")
	if id1 == id3 {
		t.Error("different paths produced same ID")
	}
}

func TestLibrarianAssociation_FS(t *testing.T) {
	dir := t.TempDir()
	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Initial state: no association.
	got, err := b.Get()
	if err != nil {
		t.Fatalf("Get (empty): %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil association on fresh dir, got %+v", got)
	}

	// Set a first association.
	first := catalog.LibrarianAssociationData{
		LibrarianURL:      "https://librarian.example/",
		LibrarianInstance: "inst-fs",
		ChatSecret:        "chat-aaa",
		WebhookSecret:     "hook-bbb",
	}
	if err := b.Set(first); err != nil {
		t.Fatalf("Set (first): %v", err)
	}

	// File must exist with 0600 perms (secrets-on-disk).
	st, err := os.Stat(filepath.Join(dir, ".librarian.json"))
	if err != nil {
		t.Fatalf("stat .librarian.json: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0600 {
		t.Errorf("expected 0600 perms on .librarian.json, got %o", perm)
	}

	got, err = b.Get()
	if err != nil || got == nil {
		t.Fatalf("Get after Set: err=%v got=%v", err, got)
	}
	if got.LibrarianURL != first.LibrarianURL ||
		got.LibrarianInstance != first.LibrarianInstance ||
		got.ChatSecret != first.ChatSecret ||
		got.WebhookSecret != first.WebhookSecret {
		t.Errorf("association mismatch after Set: %+v", got)
	}
	createdAt1 := got.CreatedAt.Unix()

	// Replace: CreatedAt must be preserved.
	time.Sleep(1100 * time.Millisecond)
	second := catalog.LibrarianAssociationData{
		LibrarianURL:      "https://librarian.example/v2",
		LibrarianInstance: "inst-fs-v2",
		ChatSecret:        "rotated-chat",
		WebhookSecret:     "rotated-hook",
	}
	if err := b.Set(second); err != nil {
		t.Fatalf("Set (second): %v", err)
	}
	got, err = b.Get()
	if err != nil || got == nil {
		t.Fatalf("Get after second Set: err=%v got=%v", err, got)
	}
	if got.LibrarianURL != second.LibrarianURL || got.ChatSecret != "rotated-chat" {
		t.Errorf("replacement did not stick: %+v", got)
	}
	if got.CreatedAt.Unix() != createdAt1 {
		t.Errorf("CreatedAt should be preserved: was %d, now %d", createdAt1, got.CreatedAt.Unix())
	}
	if got.UpdatedAt.Unix() <= createdAt1 {
		t.Errorf("UpdatedAt should advance: createdAt=%d updatedAt=%d", createdAt1, got.UpdatedAt.Unix())
	}

	// Clear removes the file.
	if err := b.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".librarian.json")); !os.IsNotExist(err) {
		t.Errorf("expected file removed, got err=%v", err)
	}
	got, err = b.Get()
	if err != nil {
		t.Fatalf("Get after Clear: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after Clear, got %+v", got)
	}

	// Clear is idempotent.
	if err := b.Clear(); err != nil {
		t.Errorf("Clear (second call) should be idempotent, got: %v", err)
	}
}
