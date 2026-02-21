package sqlite

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

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
