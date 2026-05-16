package sqlite

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/banux/nxt-opds/internal/catalog"
	_ "modernc.org/sqlite"
)

// openSQLite opens a raw SQLite database for test setup purposes.
func openSQLite(path string) (*sql.DB, error) {
	return sql.Open("sqlite", path)
}

// createMinimalEPUB writes a valid minimal EPUB file to path.
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

func TestSQLiteBackend_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer b.Close()

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

func TestSQLiteBackend_SingleEPUB(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "test.epub"), "Test Book", "Test Author", "Fiction")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer b.Close()

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

func TestSQLiteBackend_BookByID(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "book.epub"), "My Book", "An Author", "")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer b.Close()

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

func TestSQLiteBackend_Search(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "go.epub"), "Learning Go", "John Doe", "Programming")
	createMinimalEPUB(t, filepath.Join(dir, "python.epub"), "Python Cookbook", "Jane Smith", "Programming")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer b.Close()

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

func TestSQLiteBackend_SearchBySeries(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "a.epub"), "Book A", "Author A", "")
	createMinimalEPUB(t, filepath.Join(dir, "b.epub"), "Book B", "Author B", "")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer b.Close()

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
	series := "Dune"
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

func TestSQLiteBackend_AuthorsAndTags(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "a.epub"), "Book A", "Author One", "SciFi")
	createMinimalEPUB(t, filepath.Join(dir, "b.epub"), "Book B", "Author Two", "Fantasy")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer b.Close()

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

func TestSQLiteBackend_BooksByAuthor(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "a.epub"), "Book A", "Common Author", "")
	createMinimalEPUB(t, filepath.Join(dir, "b.epub"), "Book B", "Common Author", "")
	createMinimalEPUB(t, filepath.Join(dir, "c.epub"), "Book C", "Other Author", "")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer b.Close()

	books, total, err := b.BooksByAuthor("Common Author", 0, 50)
	if err != nil {
		t.Fatalf("BooksByAuthor() error: %v", err)
	}
	if total != 2 {
		t.Errorf("expected 2 books by 'Common Author', got %d", total)
	}
	_ = books
}

func TestSQLiteBackend_Pagination(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		name := "book" + string(rune('A'+i)) + ".epub"
		createMinimalEPUB(t, filepath.Join(dir, name), "Book "+string(rune('A'+i)), "Author", "")
	}

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer b.Close()

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

func TestSQLiteBackend_UpdateBook(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "book.epub"), "Original Title", "Original Author", "Sci-Fi")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer b.Close()

	books, _, _ := b.AllBooks(0, 50)
	if len(books) == 0 {
		t.Fatal("no books found")
	}
	id := books[0].ID

	newTitle := "Updated Title"
	newAuthors := []string{"New Author"}
	newTags := []string{"Fantasy", "Adventure"}
	isRead := true

	updated, err := b.UpdateBook(id, catalog.BookUpdate{
		Title:   &newTitle,
		Authors: newAuthors,
		Tags:    newTags,
		IsRead:  &isRead,
	})
	if err != nil {
		t.Fatalf("UpdateBook() error: %v", err)
	}

	if updated.Title != newTitle {
		t.Errorf("title: got %q, want %q", updated.Title, newTitle)
	}
	if len(updated.Authors) != 1 || updated.Authors[0].Name != "New Author" {
		t.Errorf("authors: got %v, want [{New Author}]", updated.Authors)
	}
	if len(updated.Tags) != 2 {
		t.Errorf("tags: got %v, want [Fantasy Adventure]", updated.Tags)
	}
	if !updated.IsRead {
		t.Error("IsRead should be true")
	}

	// Verify persistence: reopen DB
	b.Close()
	b2, err := New(dir)
	if err != nil {
		t.Fatalf("reopen New() error: %v", err)
	}
	defer b2.Close()

	bk, err := b2.BookByID(id)
	if err != nil {
		t.Fatalf("BookByID after reopen error: %v", err)
	}
	if bk.Title != newTitle {
		t.Errorf("after reopen title: got %q, want %q", bk.Title, newTitle)
	}
	if !bk.IsRead {
		t.Error("after reopen IsRead should be true")
	}
}

func TestSQLiteBackend_UploadedBooksNotMarkedAsIndexed(t *testing.T) {
	dir := t.TempDir()

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer b.Close()

	// Upload a book
	epubContent := func() *bytes.Buffer {
		// Create a minimal EPUB in memory
		buf := new(bytes.Buffer)
		w := zip.NewWriter(buf)
		defer w.Close()

		containerXML := `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`

		contentOPF := `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Uploaded Book</dc:title>
    <dc:creator>Test Author</dc:creator>
    <dc:language>en</dc:language>
  </metadata>
</package>`

		f, _ := w.Create("META-INF/container.xml")
		f.Write([]byte(containerXML))
		f, _ = w.Create("content.opf")
		f.Write([]byte(contentOPF))

		return buf
	}()

	uploaded, err := b.StoreBook("uploaded.epub", io.NopCloser(epubContent))
	if err != nil {
		t.Fatalf("StoreBook() error: %v", err)
	}

	t.Logf("Uploaded book: ID=%s, LastMaintenanceAt=%v, IsZero=%v", uploaded.ID, uploaded.LastMaintenanceAt, uploaded.LastMaintenanceAt.IsZero())

	// Check that the uploaded book appears in NotIndexed filter
	notIndexedBooks, total, err := b.Search(catalog.SearchQuery{NotIndexed: true, Limit: 50})
	if err != nil {
		t.Fatalf("Search(NotIndexed) error: %v", err)
	}

	if total != 1 {
		t.Errorf("expected 1 not-indexed book (uploaded book), got %d", total)
	}
	if len(notIndexedBooks) != 1 {
		t.Errorf("expected 1 book in result, got %d", len(notIndexedBooks))
	}
	if notIndexedBooks[0].ID != uploaded.ID {
		t.Errorf("expected uploaded book in results, got %s", notIndexedBooks[0].ID)
	}
}

func TestSQLiteBackend_NotIndexedFilter(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "book1.epub"), "Book 1", "Author", "")
	createMinimalEPUB(t, filepath.Join(dir, "book2.epub"), "Book 2", "Author", "")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer b.Close()

	// After New(), Refresh() is called, so all books should be indexed
	// Get all books first
	allBooks, _, err := b.Search(catalog.SearchQuery{Limit: 50})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(allBooks) != 2 {
		t.Errorf("expected 2 books total, got %d", len(allBooks))
	}
	t.Logf("All books: %v", len(allBooks))
	for _, b := range allBooks {
		t.Logf("  - %s: LastMaintenanceAt=%v (IsZero=%v)", b.Title, b.LastMaintenanceAt, b.LastMaintenanceAt.IsZero())
	}

	// All books should be indexed (not indexed count = 0)
	notIndexedBooks, total, err := b.Search(catalog.SearchQuery{NotIndexed: true, Limit: 50})
	if err != nil {
		t.Fatalf("Search(NotIndexed) error: %v", err)
	}
	t.Logf("NotIndexed books count: %d (total=%d)", len(notIndexedBooks), total)
	if total != 0 {
		t.Errorf("expected 0 not-indexed books (all should be indexed by Refresh), got %d", total)
	}

	// Manually create a book with zero lastMaintenanceAt
	zeroBook := catalog.Book{
		ID:       "test-zero",
		Title:    "Zero Maintenance Book",
		Language: "en",
		Files: []catalog.File{
			{Path: "/tmp/test.epub", MIMEType: "application/epub+zip", Size: 1000},
		},
	}
	if err := b.insertBook(zeroBook); err != nil {
		t.Fatalf("insertBook() error: %v", err)
	}
	// Don't call updateMaintenanceAt, so lastMaintenanceAt remains 0

	// Now search for not indexed books
	notIndexedBooks, total, err = b.Search(catalog.SearchQuery{NotIndexed: true, Limit: 50})
	if err != nil {
		t.Fatalf("Search(NotIndexed) after insert error: %v", err)
	}
	t.Logf("NotIndexed books after insert: count=%d, total=%d", len(notIndexedBooks), total)
	if total != 1 {
		t.Errorf("expected 1 not-indexed book, got %d (total=%d)", len(notIndexedBooks), total)
	}
	if len(notIndexedBooks) != 1 {
		t.Errorf("expected 1 book in result, got %d", len(notIndexedBooks))
	}

	// Mark the book as indexed
	now := time.Now()
	updated, err := b.UpdateBook("test-zero", catalog.BookUpdate{
		LastMaintenanceAt: &now,
	})
	if err != nil {
		t.Fatalf("UpdateBook() error: %v", err)
	}
	if updated.LastMaintenanceAt.IsZero() {
		t.Error("LastMaintenanceAt should not be zero after update")
	}
	t.Logf("Updated book: LastMaintenanceAt=%v", updated.LastMaintenanceAt)

	// Now search for not indexed books again
	notIndexedBooks, total, err = b.Search(catalog.SearchQuery{NotIndexed: true, Limit: 50})
	if err != nil {
		t.Fatalf("Search(NotIndexed) after update error: %v", err)
	}
	t.Logf("NotIndexed books after update: count=%d, total=%d", len(notIndexedBooks), total)
	if total != 0 {
		t.Errorf("expected 0 not-indexed books after update, got %d (total=%d)", len(notIndexedBooks), total)
	}
}

func TestSQLiteBackend_Refresh_RemovesDeletedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "book.epub")
	createMinimalEPUB(t, path, "Temp Book", "Author", "")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer b.Close()

	_, total, _ := b.AllBooks(0, 50)
	if total != 1 {
		t.Fatalf("expected 1 book before delete, got %d", total)
	}

	// Remove the file and refresh
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	if err := b.Refresh(); err != nil {
		t.Fatalf("Refresh() error: %v", err)
	}

	_, total, _ = b.AllBooks(0, 50)
	if total != 0 {
		t.Errorf("expected 0 books after delete + refresh, got %d", total)
	}
}

// TestMigrateSchema_FreshDB verifies that migrateSchema sets PRAGMA user_version
// to currentSchemaVersion on a brand-new database.
func TestMigrateSchema_FreshDB(t *testing.T) {
	dir := t.TempDir()
	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer b.Close()

	var version int
	if err := b.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("expected user_version=%d, got %d", currentSchemaVersion, version)
	}
}

// TestMigrateSchema_Idempotent verifies that calling New() on an already-migrated
// database is safe (no duplicate-column errors, version unchanged).
func TestMigrateSchema_Idempotent(t *testing.T) {
	dir := t.TempDir()

	// First open migrates to currentSchemaVersion.
	b1, err := New(dir)
	if err != nil {
		t.Fatalf("first New() error: %v", err)
	}
	b1.Close()

	// Second open should be a no-op (all migrations already applied).
	b2, err := New(dir)
	if err != nil {
		t.Fatalf("second New() error: %v", err)
	}
	defer b2.Close()

	var version int
	if err := b2.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("expected user_version=%d after re-open, got %d", currentSchemaVersion, version)
	}
}

// TestMigrateSchema_PreMigrationDB simulates a legacy database (user_version=0,
// tables already created without all columns) and verifies that migrateSchema
// upgrades it safely.
func TestMigrateSchema_PreMigrationDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, dbFilename)

	// Create a legacy database: tables exist but rating / series_total columns
	// are missing (as they were in early versions of nxt-opds).
	db, err := openSQLite(dbPath)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	_, err = db.Exec(`
CREATE TABLE books (
    id           TEXT PRIMARY KEY,
    title        TEXT NOT NULL DEFAULT '',
    updated_at   INTEGER NOT NULL DEFAULT 0,
    added_at     INTEGER NOT NULL DEFAULT 0,
    series       TEXT NOT NULL DEFAULT '',
    series_index TEXT NOT NULL DEFAULT '',
    is_read      INTEGER NOT NULL DEFAULT 0,
    cover_url    TEXT NOT NULL DEFAULT '',
    file_path    TEXT NOT NULL DEFAULT '',
    file_mime    TEXT NOT NULL DEFAULT '',
    file_size    INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE book_authors (book_id TEXT, author_name TEXT, author_uri TEXT DEFAULT '', position INTEGER DEFAULT 0, PRIMARY KEY(book_id,author_name));
CREATE TABLE book_tags   (book_id TEXT, tag TEXT, PRIMARY KEY(book_id,tag));
`)
	if err != nil {
		db.Close()
		t.Fatalf("create legacy schema: %v", err)
	}
	// Leave user_version = 0 to mimic a pre-migration database.
	db.Close()

	// New() must migrate without errors.
	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() on legacy db error: %v", err)
	}
	defer b.Close()

	// user_version must now be currentSchemaVersion.
	var version int
	if err := b.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("expected user_version=%d after migration, got %d", currentSchemaVersion, version)
	}

	// The rating column must now exist (it was missing in the legacy schema).
	if _, err := b.db.Exec(`UPDATE books SET rating = 0`); err != nil {
		t.Errorf("rating column not present after migration: %v", err)
	}
}

// TestBackup_CreatesFile verifies that Backup() creates a non-empty .db file
// in the specified directory and returns its path.
func TestBackup_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer b.Close()

	backupDir := filepath.Join(dir, "backups")
	path, err := b.Backup(backupDir, 7)
	if err != nil {
		t.Fatalf("Backup() error: %v", err)
	}

	// File must exist and be non-empty.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("backup file not found at %s: %v", path, err)
	}
	if info.Size() == 0 {
		t.Error("backup file is empty")
	}

	// File name must follow the naming convention.
	name := filepath.Base(path)
	if len(name) < 8 || name[:8] != "catalog-" || filepath.Ext(name) != ".db" {
		t.Errorf("unexpected backup filename %q", name)
	}
}

// TestBackup_PrunesOldFiles verifies that Backup() removes excess backups so
// that at most keep files are retained.
func TestBackup_PrunesOldFiles(t *testing.T) {
	dir := t.TempDir()
	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer b.Close()

	backupDir := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		t.Fatal(err)
	}

	keep := 2

	// Pre-seed the backup directory with 4 stale dummy backup files so that
	// a single real Backup() call (keep=2) should prune down to 2 files.
	staleNames := []string{
		"catalog-20240101-000000.db",
		"catalog-20240102-000000.db",
		"catalog-20240103-000000.db",
		"catalog-20240104-000000.db",
	}
	for _, n := range staleNames {
		if err := os.WriteFile(filepath.Join(backupDir, n), []byte("dummy"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// One real backup (timestamp newer than all stale names) triggers pruning.
	if _, err := b.Backup(backupDir, keep); err != nil {
		t.Fatalf("Backup() error: %v", err)
	}

	// Count remaining backup files.
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatalf("read backup dir: %v", err)
	}
	var count int
	for _, e := range entries {
		if !e.IsDir() {
			count++
		}
	}
	if count != keep {
		t.Errorf("expected %d backups after pruning, got %d", keep, count)
	}
}

// TestSearch_FTS5_AuthorMatch verifies that the FTS5 index returns a book
// when the query matches an author (a column not present on the books table
// itself — the trigger denormalises it).
func TestSearch_FTS5_AuthorMatch(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "a.epub"), "Foundation", "Isaac Asimov", "SF")
	createMinimalEPUB(t, filepath.Join(dir, "b.epub"), "Hyperion", "Dan Simmons", "SF")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	books, total, err := b.Search(catalog.SearchQuery{Query: "asimov", Limit: 50})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if total != 1 || len(books) != 1 || books[0].Title != "Foundation" {
		t.Errorf("expected only 'Foundation' for 'asimov', got total=%d books=%v", total, books)
	}
}

// TestSearch_FTS5_PrefixMatch verifies that a partial token matches via the
// trailing `*` prefix operator added by buildFTSMatchQuery.
func TestSearch_FTS5_PrefixMatch(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "a.epub"), "Programming Rust", "Jim Blandy", "Tech")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	for _, q := range []string{"prog", "rust", "blandy"} {
		_, total, err := b.Search(catalog.SearchQuery{Query: q, Limit: 50})
		if err != nil {
			t.Fatalf("Search %q: %v", q, err)
		}
		if total != 1 {
			t.Errorf("query %q: expected 1, got %d", q, total)
		}
	}
}

// TestSearch_FTS5_SpecialCharsFallback ensures a query made of only operator
// characters does not crash and returns no result rather than a 5xx.
func TestSearch_FTS5_SpecialCharsFallback(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "a.epub"), "Hyperion", "Dan Simmons", "SF")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	// Only punctuation — buildFTSMatchQuery returns "", so we exercise the
	// LIKE fallback path; "+++" doesn't match anything but must not error.
	if _, _, err := b.Search(catalog.SearchQuery{Query: "+++", Limit: 50}); err != nil {
		t.Errorf("Search('+++') returned error: %v", err)
	}
}

// TestSearch_FTS5_FollowsTitleUpdates verifies that the AFTER UPDATE trigger
// keeps the FTS index in sync when a book is renamed.
func TestSearch_FTS5_FollowsTitleUpdates(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "a.epub"), "Old Title", "Some Author", "")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	books, _, _ := b.Search(catalog.SearchQuery{Limit: 50})
	if len(books) != 1 {
		t.Fatalf("expected 1 book, got %d", len(books))
	}
	newTitle := "Brave New World"
	if _, err := b.UpdateBook(books[0].ID, catalog.BookUpdate{Title: &newTitle}); err != nil {
		t.Fatalf("UpdateBook: %v", err)
	}

	_, total, _ := b.Search(catalog.SearchQuery{Query: "brave", Limit: 50})
	if total != 1 {
		t.Errorf("FTS index not updated after rename — search 'brave' returned %d", total)
	}
	_, total, _ = b.Search(catalog.SearchQuery{Query: "old", Limit: 50})
	if total != 0 {
		t.Errorf("FTS index still contains old title — search 'old' returned %d", total)
	}
}

// TestSearch_SeriesSizeFilter verifies that the SeriesSize filter buckets
// books by how many siblings share their series name.
func TestSearch_SeriesSizeFilter(t *testing.T) {
	dir := t.TempDir()
	// 1 standalone, 1 series with 2 books (short), 1 series with 5 books (medium)
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
	defer b.Close()

	all, _, _ := b.Search(catalog.SearchQuery{Limit: 50})
	duo := "Duo Series"
	saga := "Saga Series"
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
		{"standalone", "standalone", 1}, // Solo
		{"short", "short", 2},           // Duo
		{"medium", "medium", 5},         // Saga (5 books)
		{"long", "long", 0},             // none
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

// TestAllRecommendations_AggregatesAcrossUsers verifies that
// AllRecommendations returns recommendations from every (from, to) pair in
// a single query, replacing the previous N+1 fan-out.
func TestAllRecommendations_AggregatesAcrossUsers(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "a.epub"), "Book A", "Author A", "")
	createMinimalEPUB(t, filepath.Join(dir, "b.epub"), "Book B", "Author B", "")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	alice, err := b.CreateUser("Alice", "#f00", false, false, 0)
	if err != nil {
		t.Fatalf("alice: %v", err)
	}
	bob, err := b.CreateUser("Bob", "#00f", false, false, 0)
	if err != nil {
		t.Fatalf("bob: %v", err)
	}
	carol, err := b.CreateUser("Carol", "#0f0", false, false, 0)
	if err != nil {
		t.Fatalf("carol: %v", err)
	}

	books, _, _ := b.Search(catalog.SearchQuery{Limit: 50})
	if len(books) != 2 {
		t.Fatalf("expected 2 books, got %d", len(books))
	}

	// Alice → Bob (book A); Bob → Carol (book B).
	if err := b.RecommendBook(alice.ID, bob.ID, books[0].ID, "good one"); err != nil {
		t.Fatalf("RecommendBook 1: %v", err)
	}
	if err := b.RecommendBook(bob.ID, carol.ID, books[1].ID, ""); err != nil {
		t.Fatalf("RecommendBook 2: %v", err)
	}

	recs, err := b.AllRecommendations()
	if err != nil {
		t.Fatalf("AllRecommendations: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 recommendations, got %d", len(recs))
	}
	// Both FromUser and ToUser must be populated (the previous per-user
	// helper only filled one of them).
	for _, r := range recs {
		if r.FromUser.ID == "" || r.ToUser.ID == "" {
			t.Errorf("FromUser/ToUser should be populated, got %+v / %+v", r.FromUser, r.ToUser)
		}
	}
}

// TestBackup_RejectsCorruptFile verifies that the integrity check refuses
// to keep a backup whose bytes have been mangled, so a silent VACUUM INTO
// failure (e.g. partial disk full) cannot end up retained as a valid backup.
func TestBackup_RejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	backupDir := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(backupDir, "corrupt.db")
	if err := os.WriteFile(corrupt, []byte("not a real sqlite file"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackupIntegrity(corrupt); err == nil {
		t.Errorf("expected integrity check to reject a non-SQLite file")
	}
}

// TestBackup_AcceptsValidBackup verifies that the integrity check passes for
// a backup just produced by Backup().
func TestBackup_AcceptsValidBackup(t *testing.T) {
	dir := t.TempDir()
	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	backupDir := filepath.Join(dir, "backups")
	path, err := b.Backup(backupDir, 7)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if err := verifyBackupIntegrity(path); err != nil {
		t.Errorf("freshly produced backup should pass integrity_check: %v", err)
	}
}

// TestReadStats_PerUser verifies that ReadStats aggregates per-user read-status
// correctly: count of books read, top authors/tags, and rating distribution.
func TestReadStats_PerUser(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "a.epub"), "Alpha", "Jane Doe", "SF")
	createMinimalEPUB(t, filepath.Join(dir, "b.epub"), "Beta", "Jane Doe", "SF")
	createMinimalEPUB(t, filepath.Join(dir, "c.epub"), "Gamma", "John Roe", "Roman")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	// Create a user and mark two books as read.
	u, err := b.CreateUser("Alice", "#ff00ff", false, false, 0)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	books, _, _ := b.AllBooks(0, 10)
	if len(books) < 3 {
		t.Fatalf("expected ≥3 books, got %d", len(books))
	}
	if err := b.SetUserRead(u.ID, books[0].ID, true); err != nil {
		t.Fatalf("SetUserRead: %v", err)
	}
	if err := b.SetUserRead(u.ID, books[1].ID, true); err != nil {
		t.Fatalf("SetUserRead: %v", err)
	}
	// Rate one of the read books.
	rating := 4
	if _, err := b.UpdateBook(books[0].ID, catalog.BookUpdate{Rating: &rating}); err != nil {
		t.Fatalf("UpdateBook rating: %v", err)
	}

	stats, err := b.ReadStats(u.ID)
	if err != nil {
		t.Fatalf("ReadStats: %v", err)
	}
	if stats.TotalBooks != 3 {
		t.Errorf("TotalBooks: got %d, want 3", stats.TotalBooks)
	}
	if stats.BooksRead != 2 {
		t.Errorf("BooksRead: got %d, want 2", stats.BooksRead)
	}
	if stats.BooksReadThisYear != 2 {
		t.Errorf("BooksReadThisYear: got %d, want 2 (timestamps stored on write)", stats.BooksReadThisYear)
	}
	if stats.RatedBooks != 1 {
		t.Errorf("RatedBooks: got %d, want 1", stats.RatedBooks)
	}
	if stats.AverageRating != 4 {
		t.Errorf("AverageRating: got %v, want 4", stats.AverageRating)
	}
	if stats.RatingDistribution[3] != 1 {
		t.Errorf("RatingDistribution[3]: got %d, want 1", stats.RatingDistribution[3])
	}
	// Both read books have the same author + tag, so it should be rank #1 with count 2.
	if len(stats.TopAuthors) == 0 || stats.TopAuthors[0].Label != "Jane Doe" || stats.TopAuthors[0].Count != 2 {
		t.Errorf("TopAuthors[0]: got %+v, want {Jane Doe 2}", stats.TopAuthors)
	}
	if len(stats.TopTags) == 0 || stats.TopTags[0].Label != "SF" || stats.TopTags[0].Count != 2 {
		t.Errorf("TopTags[0]: got %+v, want {SF 2}", stats.TopTags)
	}
	if len(stats.ByMonth) != 12 {
		t.Errorf("ByMonth len: got %d, want 12", len(stats.ByMonth))
	}
	// Last entry is the current month → should contain the 2 books we just marked.
	if n := stats.ByMonth[len(stats.ByMonth)-1].Count; n != 2 {
		t.Errorf("ByMonth[current]: got %d, want 2", n)
	}
}

// TestReadStats_NoUserID returns global stats (any reader) when userID is empty.
func TestReadStats_NoUserID(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "a.epub"), "Alpha", "Jane Doe", "SF")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	u1, _ := b.CreateUser("A", "#ff0000", false, false, 0)
	u2, _ := b.CreateUser("B", "#00ff00", false, false, 0)

	books, _, _ := b.AllBooks(0, 10)
	if len(books) == 0 {
		t.Fatal("no books")
	}
	_ = b.SetUserRead(u1.ID, books[0].ID, true)
	_ = b.SetUserRead(u2.ID, books[0].ID, true)

	stats, err := b.ReadStats("")
	if err != nil {
		t.Fatalf("ReadStats: %v", err)
	}
	if stats.TotalBooks != 1 {
		t.Errorf("TotalBooks: got %d, want 1", stats.TotalBooks)
	}
	// 1 distinct book read across 2 users = 1.
	if stats.BooksRead != 1 {
		t.Errorf("BooksRead (global): got %d, want 1", stats.BooksRead)
	}
}

// TestToReadList_AddListRemove verifies basic add/list/remove on the to-read list.
func TestToReadList_AddListRemove(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "a.epub"), "Alpha", "Jane Doe", "SF")
	createMinimalEPUB(t, filepath.Join(dir, "b.epub"), "Beta", "Jane Doe", "SF")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	u, err := b.CreateUser("Reader", "#ff00ff", false, false, 0)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	books, _, _ := b.AllBooks(0, 10)
	if len(books) < 2 {
		t.Fatalf("expected ≥2 books, got %d", len(books))
	}

	// Empty list initially.
	items, err := b.ToReadList(u.ID)
	if err != nil {
		t.Fatalf("ToReadList: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected empty list, got %d items", len(items))
	}

	// Add two books.
	if err := b.AddToReadList(u.ID, books[0].ID); err != nil {
		t.Fatalf("AddToReadList[0]: %v", err)
	}
	if err := b.AddToReadList(u.ID, books[1].ID); err != nil {
		t.Fatalf("AddToReadList[1]: %v", err)
	}

	// Adding the same book again is a no-op.
	if err := b.AddToReadList(u.ID, books[0].ID); err != nil {
		t.Fatalf("AddToReadList duplicate: %v", err)
	}

	items, err = b.ToReadList(u.ID)
	if err != nil {
		t.Fatalf("ToReadList: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Book.ID != books[0].ID {
		t.Errorf("position 0: got %q, want %q", items[0].Book.ID, books[0].ID)
	}
	if items[1].Book.ID != books[1].ID {
		t.Errorf("position 1: got %q, want %q", items[1].Book.ID, books[1].ID)
	}

	// Remove the first book.
	if err := b.RemoveFromToReadList(u.ID, books[0].ID); err != nil {
		t.Fatalf("RemoveFromToReadList: %v", err)
	}
	items, _ = b.ToReadList(u.ID)
	if len(items) != 1 || items[0].Book.ID != books[1].ID {
		t.Errorf("after remove: got %+v, want only books[1]", items)
	}
}

// TestToReadList_Reorder verifies that ReorderToReadList swaps positions
// and that books missing from the request are appended at the end.
func TestToReadList_Reorder(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "a.epub"), "Alpha", "Author", "Tag")
	createMinimalEPUB(t, filepath.Join(dir, "b.epub"), "Beta", "Author", "Tag")
	createMinimalEPUB(t, filepath.Join(dir, "c.epub"), "Gamma", "Author", "Tag")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	u, _ := b.CreateUser("Reader", "#ff0000", false, false, 0)
	books, _, _ := b.AllBooks(0, 10)
	if len(books) < 3 {
		t.Fatalf("expected ≥3 books, got %d", len(books))
	}

	for _, bk := range books {
		if err := b.AddToReadList(u.ID, bk.ID); err != nil {
			t.Fatalf("AddToReadList: %v", err)
		}
	}

	// Reverse the order, omitting the third book (it should land at the end).
	if err := b.ReorderToReadList(u.ID, []string{books[1].ID, books[0].ID}); err != nil {
		t.Fatalf("ReorderToReadList: %v", err)
	}
	items, _ := b.ToReadList(u.ID)
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	if items[0].Book.ID != books[1].ID {
		t.Errorf("position 0: got %q, want %q", items[0].Book.ID, books[1].ID)
	}
	if items[1].Book.ID != books[0].ID {
		t.Errorf("position 1: got %q, want %q", items[1].Book.ID, books[0].ID)
	}
	if items[2].Book.ID != books[2].ID {
		t.Errorf("position 2 (leftover): got %q, want %q", items[2].Book.ID, books[2].ID)
	}
}

// TestToReadList_RemovedOnRead verifies that marking a book as read
// automatically removes it from the user's to-read list.
func TestToReadList_RemovedOnRead(t *testing.T) {
	dir := t.TempDir()
	createMinimalEPUB(t, filepath.Join(dir, "a.epub"), "Alpha", "Author", "Tag")

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	u, _ := b.CreateUser("Reader", "#ff0000", false, false, 0)
	books, _, _ := b.AllBooks(0, 10)
	if len(books) == 0 {
		t.Fatal("no books")
	}
	if err := b.AddToReadList(u.ID, books[0].ID); err != nil {
		t.Fatalf("AddToReadList: %v", err)
	}

	// Mark as read → should remove from the to-read list.
	if err := b.SetUserRead(u.ID, books[0].ID, true); err != nil {
		t.Fatalf("SetUserRead: %v", err)
	}
	items, _ := b.ToReadList(u.ID)
	if len(items) != 0 {
		t.Errorf("expected empty list after read, got %d items", len(items))
	}
}

// TestWebhook_CRUD verifies the WebhookManager CRUD round-trip.
func TestWebhook_CRUD(t *testing.T) {
	dir := t.TempDir()
	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	// Empty list initially.
	hooks, err := b.Webhooks()
	if err != nil {
		t.Fatalf("Webhooks (empty): %v", err)
	}
	if len(hooks) != 0 {
		t.Fatalf("expected 0 webhooks initially, got %d", len(hooks))
	}

	// Create.
	h, err := b.CreateWebhook("My Webhook", "https://example.test/hook",
		[]string{catalog.WebhookEventBookCreated, catalog.WebhookEventBookUpdated},
		"s3cret", true)
	if err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}
	if h.ID == "" {
		t.Errorf("expected non-empty ID")
	}
	if h.CreatedAt.IsZero() {
		t.Errorf("expected non-zero CreatedAt")
	}
	if len(h.Events) != 2 {
		t.Errorf("expected 2 events, got %d", len(h.Events))
	}

	// Get by ID.
	got, err := b.WebhookByID(h.ID)
	if err != nil {
		t.Fatalf("WebhookByID: %v", err)
	}
	if got.URL != "https://example.test/hook" {
		t.Errorf("URL mismatch: %q", got.URL)
	}
	if got.Secret != "s3cret" {
		t.Errorf("secret mismatch: %q", got.Secret)
	}

	// Update: disable + change name.
	newName := "Renamed"
	disabled := false
	upd := catalog.WebhookUpdate{Name: &newName, Enabled: &disabled}
	got2, err := b.UpdateWebhook(h.ID, upd)
	if err != nil {
		t.Fatalf("UpdateWebhook: %v", err)
	}
	if got2.Name != "Renamed" || got2.Enabled {
		t.Errorf("update mismatch: %+v", got2)
	}

	// Record fire.
	if err := b.RecordWebhookFire(h.ID, "200 OK", time.Unix(1700000000, 0)); err != nil {
		t.Fatalf("RecordWebhookFire: %v", err)
	}
	got3, _ := b.WebhookByID(h.ID)
	if got3.LastStatus != "200 OK" || got3.LastFiredAt.IsZero() {
		t.Errorf("expected last-fired updated: %+v", got3)
	}

	// Delete.
	if err := b.DeleteWebhook(h.ID); err != nil {
		t.Fatalf("DeleteWebhook: %v", err)
	}
	if _, err := b.WebhookByID(h.ID); err == nil {
		t.Errorf("expected error after delete")
	}
}

func TestLibrarianAssociation_Singleton(t *testing.T) {
	dir := t.TempDir()
	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	// Initial state: no association.
	got, err := b.Get()
	if err != nil {
		t.Fatalf("Get (empty): %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil association on fresh DB, got %+v", got)
	}

	// Set a first association.
	first := catalog.LibrarianAssociationData{
		LibrarianURL:      "https://librarian.example/",
		LibrarianInstance: "inst-42",
		ChatSecret:        "chat-secret-aaaa",
		WebhookSecret:     "webhook-secret-bbbb",
	}
	if err := b.Set(first); err != nil {
		t.Fatalf("Set (first): %v", err)
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
	if got.CreatedAt.IsZero() {
		t.Errorf("CreatedAt should be set")
	}
	if got.UpdatedAt.IsZero() {
		t.Errorf("UpdatedAt should be set")
	}
	createdAt1 := got.CreatedAt.Unix()

	// Replace with a second association: CreatedAt must be preserved.
	time.Sleep(1100 * time.Millisecond) // ensure Unix-second-resolution UpdatedAt advances
	second := catalog.LibrarianAssociationData{
		LibrarianURL:      "https://librarian.example/v2",
		LibrarianInstance: "inst-43",
		ChatSecret:        "rotated-chat",
		WebhookSecret:     "rotated-webhook",
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
		t.Errorf("CreatedAt should be preserved across Set: was %d, now %d", createdAt1, got.CreatedAt.Unix())
	}
	if got.UpdatedAt.Unix() <= createdAt1 {
		t.Errorf("UpdatedAt should advance after a Set: createdAt=%d updatedAt=%d", createdAt1, got.UpdatedAt.Unix())
	}

	// Confirm the table really is a singleton — no second row.
	var n int
	if err := b.db.QueryRow(`SELECT COUNT(*) FROM librarian_association`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("expected exactly 1 row in librarian_association, got %d", n)
	}

	// Clear removes it.
	if err := b.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	got, err = b.Get()
	if err != nil {
		t.Fatalf("Get after Clear: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after Clear, got %+v", got)
	}

	// Clear is idempotent: calling it again must not error.
	if err := b.Clear(); err != nil {
		t.Errorf("Clear (second call) should be idempotent, got: %v", err)
	}
}
