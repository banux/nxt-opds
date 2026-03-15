// Package sqlite implements a SQLite-backed catalog backend for nxt-opds.
// It scans a directory for EPUB/PDF files and persists all book metadata
// (including user overrides) in a SQLite database, enabling efficient queries
// and full-text search for large collections.
package sqlite

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/banux/nxt-opds/internal/catalog"
	"github.com/banux/nxt-opds/internal/epub"
	_ "modernc.org/sqlite" // register "sqlite" driver
)

const dbFilename = ".catalog.db"

// Backend is a SQLite-backed catalog backend.
type Backend struct {
	root      string
	coversDir string
	db        *sql.DB
}

// New opens (or creates) the SQLite catalog at {dir}/.catalog.db, applies
// schema migrations, syncs the filesystem, and returns the Backend.
func New(dir string) (*Backend, error) {
	coversDir := filepath.Join(dir, ".covers")
	if err := os.MkdirAll(coversDir, 0755); err != nil {
		return nil, fmt.Errorf("create covers dir: %w", err)
	}

	dbPath := filepath.Join(dir, dbFilename)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database %q: %w", dbPath, err)
	}

	// WAL mode for concurrent reads; foreign keys for cascade deletes.
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("configure database: %w", err)
	}

	b := &Backend{root: dir, coversDir: coversDir, db: db}
	if err := b.migrateSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	if err := b.Refresh(); err != nil {
		db.Close()
		return nil, fmt.Errorf("initial scan: %w", err)
	}
	return b, nil
}

// Close releases database resources.
func (b *Backend) Close() error {
	return b.db.Close()
}

// currentSchemaVersion is the latest schema version this binary expects.
// Increment this constant and add a new entry to schemaMigrations whenever
// the database schema changes.
const currentSchemaVersion = 10

// schemaMigration describes a single, idempotent database migration.
type schemaMigration struct {
	version int
	apply   func(db *sql.DB) error
}

// schemaMigrations is the ordered list of all schema migrations.
// Each migration is applied exactly once (when PRAGMA user_version < version).
var schemaMigrations = []schemaMigration{
	{version: 1, apply: migration1},
	{version: 2, apply: migration2},
	{version: 3, apply: migration3},
	{version: 4, apply: migration4},
	{version: 5, apply: migration5},
	{version: 6, apply: migration6},
	{version: 7, apply: migration7},
	{version: 8, apply: migration8},
	{version: 9, apply: migration9},
	{version: 10, apply: migration10},
}

// migration1 sets up the initial schema (version 0 → 1).
// It uses CREATE TABLE IF NOT EXISTS so it is safe to run on an existing
// pre-migration database (user_version was never set, so it is 0).
// For pre-migration databases that may be missing columns added incrementally
// (added_at, series_total, rating), it also attempts safe ALTER TABLE
// statements; "duplicate column" errors are intentionally ignored.
func migration1(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS books (
    id            TEXT PRIMARY KEY,
    title         TEXT NOT NULL DEFAULT '',
    summary       TEXT NOT NULL DEFAULT '',
    language      TEXT NOT NULL DEFAULT '',
    publisher     TEXT NOT NULL DEFAULT '',
    published_at  INTEGER,
    updated_at    INTEGER NOT NULL,
    added_at      INTEGER NOT NULL DEFAULT 0,
    series        TEXT NOT NULL DEFAULT '',
    series_index  TEXT NOT NULL DEFAULT '',
    series_total  TEXT NOT NULL DEFAULT '',
    collection       TEXT NOT NULL DEFAULT '',
    collection_index TEXT NOT NULL DEFAULT '',
    is_read          INTEGER NOT NULL DEFAULT 0,
    rating        INTEGER NOT NULL DEFAULT 0,
    cover_url     TEXT NOT NULL DEFAULT '',
    thumbnail_url TEXT NOT NULL DEFAULT '',
    file_path     TEXT NOT NULL,
    file_mime     TEXT NOT NULL DEFAULT '',
    file_size     INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS book_authors (
    book_id     TEXT NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    author_name TEXT NOT NULL,
    author_uri  TEXT NOT NULL DEFAULT '',
    position    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (book_id, author_name)
);

CREATE TABLE IF NOT EXISTS book_tags (
    book_id TEXT NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    tag     TEXT NOT NULL,
    PRIMARY KEY (book_id, tag)
);

CREATE INDEX IF NOT EXISTS idx_book_authors_name ON book_authors(author_name);
CREATE INDEX IF NOT EXISTS idx_book_tags_tag     ON book_tags(tag);
CREATE INDEX IF NOT EXISTS idx_books_added_at    ON books(added_at DESC);
`)
	if err != nil {
		return err
	}

	// Safe column additions for databases that existed before these columns
	// were introduced. On a fresh database created above, the columns already
	// exist and these statements will return "duplicate column name" errors
	// which are intentionally swallowed.
	for _, alterSQL := range []string{
		`ALTER TABLE books ADD COLUMN added_at     INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE books ADD COLUMN series_total TEXT    NOT NULL DEFAULT ''`,
		`ALTER TABLE books ADD COLUMN rating       INTEGER NOT NULL DEFAULT 0`,
	} {
		_, _ = db.Exec(alterSQL)
	}
	return nil
}

// migration2 adds the collection column for editorial collection support (version 1 → 2).
func migration2(db *sql.DB) error {
	_, _ = db.Exec(`ALTER TABLE books ADD COLUMN collection TEXT NOT NULL DEFAULT ''`)
	return nil
}

// migration3 adds the collection_index column for editorial collection position (version 2 → 3).
func migration3(db *sql.DB) error {
	_, _ = db.Exec(`ALTER TABLE books ADD COLUMN collection_index TEXT NOT NULL DEFAULT ''`)
	return nil
}

// migration4 adds multi-user support: users table and per-user read status (version 3 → 4).
func migration4(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS users (
    id       TEXT PRIMARY KEY,
    name     TEXT NOT NULL UNIQUE,
    color    TEXT NOT NULL DEFAULT '#3B82F6',
    is_admin INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS user_read_status (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    book_id TEXT NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    is_read INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, book_id)
);

CREATE INDEX IF NOT EXISTS idx_user_read_status_user ON user_read_status(user_id);
CREATE INDEX IF NOT EXISTS idx_user_read_status_book ON user_read_status(book_id);
`)
	return err
}

// migrateSchema reads PRAGMA user_version, applies every outstanding migration
// in order, and updates user_version after each successful migration.
// This ensures the database schema is always brought up to currentSchemaVersion
// without data loss.
func (b *Backend) migrateSchema() error {
	var version int
	if err := b.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	for _, m := range schemaMigrations {
		if m.version <= version {
			continue // already applied
		}
		if err := m.apply(b.db); err != nil {
			return fmt.Errorf("apply migration v%d: %w", m.version, err)
		}
		// PRAGMA user_version does not support ? placeholders.
		if _, err := b.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, m.version)); err != nil {
			return fmt.Errorf("set schema version to %d: %w", m.version, err)
		}
	}
	return nil
}

// Refresh scans the root directory for EPUB/PDF files, inserts newly
// discovered books, and removes DB entries whose files no longer exist.
// Existing books in the DB are not re-parsed (metadata is preserved).
func (b *Backend) Refresh() error {
	// Build a set of file paths currently on disk.
	onDisk := make(map[string]bool)
	err := filepath.WalkDir(b.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".epub" || ext == ".pdf" {
			onDisk[path] = true
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("scanning directory %q: %w", b.root, err)
	}

	// Fetch the file paths already in the DB.
	rows, err := b.db.Query(`SELECT id, file_path FROM books`)
	if err != nil {
		return fmt.Errorf("query books: %w", err)
	}
	inDB := make(map[string]string) // file_path -> id
	for rows.Next() {
		var id, fp string
		if err := rows.Scan(&id, &fp); err != nil {
			rows.Close()
			return err
		}
		inDB[fp] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Insert newly discovered files.
	for path := range onDisk {
		if _, exists := inDB[path]; exists {
			continue // already indexed
		}
		var bk catalog.Book
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".epub":
			bk, err = epub.ParseBook(path, b.coversDir)
			if err != nil {
				continue // skip unreadable EPUBs
			}
		case ".pdf":
			bk = epub.ParsePath(path)
		}
		if err := b.insertBook(bk); err != nil {
			// Log but don't abort; best-effort indexing.
			continue
		}
		b.updateMaintenanceAt(bk.ID)
	}

	// Delete books whose files have been removed from disk.
	for fp, id := range inDB {
		if !onDisk[fp] {
			if _, err := b.db.Exec(`DELETE FROM books WHERE id = ?`, id); err != nil {
				return fmt.Errorf("delete stale book %q: %w", id, err)
			}
		}
	}

	return nil
}

// insertBook adds a book to the database. It is a no-op if the book ID already exists.
func (b *Backend) insertBook(bk catalog.Book) error {
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var pubAt *int64
	if !bk.PublishedAt.IsZero() {
		t := bk.PublishedAt.Unix()
		pubAt = &t
	}
	updAt := bk.UpdatedAt.Unix()
	addedAt := bk.AddedAt.Unix()
	if bk.AddedAt.IsZero() {
		addedAt = time.Now().Unix()
	}

	filePath := ""
	fileMIME := ""
	fileSize := int64(0)
	if len(bk.Files) > 0 {
		filePath = bk.Files[0].Path
		fileMIME = bk.Files[0].MIMEType
		fileSize = bk.Files[0].Size
	}

	_, err = tx.Exec(`
INSERT OR IGNORE INTO books
    (id, title, summary, language, publisher, published_at, updated_at, added_at,
     series, series_index, series_total, collection, collection_index, is_read, rating, age_rating, cover_url, thumbnail_url,
     file_path, file_mime, file_size)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		bk.ID, bk.Title, bk.Summary, bk.Language, bk.Publisher,
		pubAt, updAt, addedAt,
		bk.Series, bk.SeriesIndex, bk.SeriesTotal, bk.Collection, bk.CollectionIndex, boolToInt(bk.IsRead), bk.Rating, bk.AgeRating,
		bk.CoverURL, bk.ThumbnailURL,
		filePath, fileMIME, fileSize,
	)
	if err != nil {
		return err
	}

	for i, a := range bk.Authors {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO book_authors (book_id, author_name, author_uri, position) VALUES (?,?,?,?)`,
			bk.ID, a.Name, a.URI, i); err != nil {
			return err
		}
	}
	for _, t := range bk.Tags {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO book_tags (book_id, tag) VALUES (?,?)`, bk.ID, t); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// updateMaintenanceAt sets last_maintenance_at to NOW for the given book ID.
// It is called after a book has been fully parsed and its cover extracted.
func (b *Backend) updateMaintenanceAt(id string) {
	_, _ = b.db.Exec(`UPDATE books SET last_maintenance_at = ? WHERE id = ?`, time.Now().Unix(), id)
}

// CoverPath returns the filesystem path to the cached cover image for a book ID.
func (b *Backend) CoverPath(id string) (string, error) {
	return epub.CoverPath(b.coversDir, id)
}

// UpdateCover replaces the cover image for the given book ID with the data
// from src, updates the cover_url and thumbnail_url columns in the database,
// and removes any previously cached cover files for that ID.
// It implements catalog.CoverUpdater.
func (b *Backend) UpdateCover(id string, src io.ReadCloser, ext string) error {
	defer src.Close()

	// Remove existing cover files for this book (any extension).
	for _, oldExt := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp"} {
		_ = os.Remove(filepath.Join(b.coversDir, id+oldExt))
	}

	destPath := filepath.Join(b.coversDir, id+ext)
	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create cover file: %w", err)
	}

	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		_ = os.Remove(destPath)
		return fmt.Errorf("write cover: %w", err)
	}
	out.Close()

	coverURL := "/covers/" + id
	_, err = b.db.Exec(
		`UPDATE books SET cover_url=?, thumbnail_url=? WHERE id=?`,
		coverURL, coverURL, id,
	)
	if err != nil {
		return fmt.Errorf("update cover_url: %w", err)
	}
	return nil
}

// DeleteBook removes the book with the given ID from the DB and deletes its
// file and cover image from disk. It implements catalog.Deleter.
func (b *Backend) DeleteBook(id string) error {
	// Look up the file path before deleting the row.
	var filePath string
	err := b.db.QueryRow(`SELECT file_path FROM books WHERE id = ?`, id).Scan(&filePath)
	if err == sql.ErrNoRows {
		return fmt.Errorf("book %q not found", id)
	}
	if err != nil {
		return fmt.Errorf("query book %q: %w", id, err)
	}

	// Delete the DB row (CASCADE removes book_authors and book_tags).
	if _, err := b.db.Exec(`DELETE FROM books WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete book %q from DB: %w", id, err)
	}

	// Best-effort: delete file and cover from disk.
	_ = os.Remove(filePath)
	coverPath := filepath.Join(b.coversDir, id+".jpg")
	_ = os.Remove(coverPath)

	return nil
}

// Root returns top-level navigation entries.
func (b *Backend) Root() ([]catalog.NavEntry, error) {
	return []catalog.NavEntry{
		{
			ID:      "urn:nxt-opds:all-books",
			Title:   "All Books",
			Content: "Browse all books in the catalog",
			Href:    "/opds/books",
			Rel:     "http://opds-spec.org/sort/new",
		},
		{
			ID:      "urn:nxt-opds:by-author",
			Title:   "By Author",
			Content: "Browse books by author",
			Href:    "/opds/authors",
			Rel:     "subsection",
		},
		{
			ID:      "urn:nxt-opds:by-tag",
			Title:   "By Genre",
			Content: "Browse books by genre/tag",
			Href:    "/opds/tags",
			Rel:     "subsection",
		},
	}, nil
}

// AllBooks returns all books ordered by added_at descending with pagination.
func (b *Backend) AllBooks(offset, limit int) ([]catalog.Book, int, error) {
	total, err := b.countBooks(`SELECT COUNT(*) FROM books`)
	if err != nil {
		return nil, 0, err
	}
	books, err := b.queryBooks(`ORDER BY added_at DESC, LOWER(title) LIMIT ? OFFSET ?`, limit, offset)
	return books, total, err
}

// BookByID returns a single book by its unique ID.
func (b *Backend) BookByID(id string) (*catalog.Book, error) {
	books, err := b.queryBooks(`WHERE b.id = ? LIMIT 1`, id)
	if err != nil {
		return nil, err
	}
	if len(books) == 0 {
		return nil, fmt.Errorf("book %q not found", id)
	}
	return &books[0], nil
}

// sortClause returns the SQL ORDER BY clause for the given SearchQuery.
func sortClause(q catalog.SearchQuery) string {
	switch q.SortBy {
	case "series_index":
		// Numeric sort by series_index (stored as text), fallback to title.
		return "CAST(b.series_index AS REAL), b.series_index, LOWER(b.title)"
	case "title":
		if q.SortOrder == "desc" {
			return "LOWER(b.title) DESC"
		}
		return "LOWER(b.title) ASC"
	default: // "added" or ""
		if q.SortOrder == "asc" {
			return "b.added_at ASC, LOWER(b.title)"
		}
		return "b.added_at DESC, LOWER(b.title)"
	}
}

// Search performs a case-insensitive substring search over title and authors.
// If q.Query is empty all books are candidates (filtered only by q.UnreadOnly / q.Series).
func (b *Backend) Search(q catalog.SearchQuery) ([]catalog.Book, int, error) {
	var extraClauses []string
	var extraArgs []any

	if q.UnreadOnly {
		if q.UserID != "" {
			// Per-user unread: book has no is_read=1 entry for this user.
			extraClauses = append(extraClauses, `NOT EXISTS (
				SELECT 1 FROM user_read_status _urs
				WHERE _urs.book_id = b.id AND _urs.user_id = ? AND _urs.is_read = 1)`)
			extraArgs = append(extraArgs, q.UserID)
		} else {
			extraClauses = append(extraClauses, "b.is_read = 0")
		}
	}
	if q.Series != "" {
		extraClauses = append(extraClauses, "b.series = ?")
		extraArgs = append(extraArgs, q.Series)
	}
	if q.Author != "" {
		extraClauses = append(extraClauses, "EXISTS (SELECT 1 FROM book_authors _ba WHERE _ba.book_id = b.id AND LOWER(_ba.author_name) = LOWER(?))")
		extraArgs = append(extraArgs, q.Author)
	}
	if q.Tag != "" {
		extraClauses = append(extraClauses, "EXISTS (SELECT 1 FROM book_tags _bt WHERE _bt.book_id = b.id AND LOWER(_bt.tag) = LOWER(?))")
		extraArgs = append(extraArgs, q.Tag)
	}
	if q.Publisher != "" {
		extraClauses = append(extraClauses, "LOWER(b.publisher) = LOWER(?)")
		extraArgs = append(extraArgs, q.Publisher)
	}
	if q.Collection != "" {
		extraClauses = append(extraClauses, "LOWER(b.collection) = LOWER(?)")
		extraArgs = append(extraArgs, q.Collection)
	}
	if q.MaxAgeRating > 0 {
		extraClauses = append(extraClauses, "(b.age_rating = 0 OR b.age_rating <= ?)")
		extraArgs = append(extraArgs, q.MaxAgeRating)
	}
	if q.AgeRating == -1 {
		// Show only unclassified books
		extraClauses = append(extraClauses, "b.age_rating = 0")
	} else if q.AgeRating > 0 {
		extraClauses = append(extraClauses, "b.age_rating = ?")
		extraArgs = append(extraArgs, q.AgeRating)
	}

	extraWhere := ""
	for _, c := range extraClauses {
		extraWhere += " AND " + c
	}

	orderBy := "ORDER BY " + sortClause(q)

	if q.Query == "" {
		total, err := b.countBooks(`SELECT COUNT(*) FROM books b WHERE 1=1`+extraWhere, extraArgs...)
		if err != nil {
			return nil, 0, err
		}
		args := append(extraArgs, q.Limit, q.Offset)
		books, err := b.queryBooks(`WHERE 1=1`+extraWhere+` `+orderBy+` LIMIT ? OFFSET ?`, args...)
		return books, total, err
	}

	like := "%" + strings.ToLower(q.Query) + "%"

	countArgs := append([]any{like, like, like}, extraArgs...)
	total, err := b.countBooks(`
SELECT COUNT(DISTINCT b.id) FROM books b
LEFT JOIN book_authors ba ON ba.book_id = b.id
WHERE (LOWER(b.title) LIKE ? OR LOWER(ba.author_name) LIKE ? OR LOWER(b.series) LIKE ?)`+extraWhere, countArgs...)
	if err != nil {
		return nil, 0, err
	}

	queryArgs := append([]any{like, like, like}, extraArgs...)
	queryArgs = append(queryArgs, q.Limit, q.Offset)
	books, err := b.queryBooks(`
JOIN (
    SELECT DISTINCT b2.id FROM books b2
    LEFT JOIN book_authors ba2 ON ba2.book_id = b2.id
    WHERE (LOWER(b2.title) LIKE ? OR LOWER(ba2.author_name) LIKE ? OR LOWER(b2.series) LIKE ?)
) AS matched ON b.id = matched.id
WHERE 1=1`+extraWhere+`
`+orderBy+` LIMIT ? OFFSET ?`, queryArgs...)
	return books, total, err
}

// BooksByAuthor returns books by a specific author with pagination.
func (b *Backend) BooksByAuthor(author string, offset, limit int) ([]catalog.Book, int, error) {
	total, err := b.countBooks(`
SELECT COUNT(*) FROM books b
JOIN book_authors ba ON ba.book_id = b.id
WHERE ba.author_name = ?`, author)
	if err != nil {
		return nil, 0, err
	}
	books, err := b.queryBooks(`
JOIN book_authors ba ON ba.book_id = b.id
WHERE ba.author_name = ?
ORDER BY LOWER(b.title) LIMIT ? OFFSET ?`, author, limit, offset)
	return books, total, err
}

// BooksByTag returns books with a specific tag with pagination.
func (b *Backend) BooksByTag(tag string, offset, limit int) ([]catalog.Book, int, error) {
	total, err := b.countBooks(`
SELECT COUNT(*) FROM books b
JOIN book_tags bt ON bt.book_id = b.id
WHERE bt.tag = ?`, tag)
	if err != nil {
		return nil, 0, err
	}
	books, err := b.queryBooks(`
JOIN book_tags bt ON bt.book_id = b.id
WHERE bt.tag = ?
ORDER BY LOWER(b.title) LIMIT ? OFFSET ?`, tag, limit, offset)
	return books, total, err
}

// Authors returns all distinct author names with pagination.
func (b *Backend) Authors(offset, limit int) ([]string, int, error) {
	var total int
	if err := b.db.QueryRow(`SELECT COUNT(DISTINCT author_name) FROM book_authors`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := b.db.Query(`
SELECT DISTINCT author_name FROM book_authors
ORDER BY LOWER(author_name) LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, 0, err
		}
		names = append(names, name)
	}
	return names, total, rows.Err()
}

// Tags returns all distinct tags with pagination.
func (b *Backend) Tags(offset, limit int) ([]string, int, error) {
	var total int
	if err := b.db.QueryRow(`SELECT COUNT(DISTINCT tag) FROM book_tags`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := b.db.Query(`
SELECT DISTINCT tag FROM book_tags
ORDER BY LOWER(tag) LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, 0, err
		}
		tags = append(tags, tag)
	}
	return tags, total, rows.Err()
}

// Publishers returns all distinct non-empty publisher names sorted alphabetically with pagination.
func (b *Backend) Publishers(offset, limit int) ([]string, int, error) {
	var total int
	if err := b.db.QueryRow(`SELECT COUNT(DISTINCT publisher) FROM books WHERE publisher != ''`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := b.db.Query(`
SELECT DISTINCT publisher FROM books
WHERE publisher != ''
ORDER BY LOWER(publisher) LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var pubs []string
	for rows.Next() {
		var pub string
		if err := rows.Scan(&pub); err != nil {
			return nil, 0, err
		}
		pubs = append(pubs, pub)
	}
	return pubs, total, rows.Err()
}

// BooksByPublisher returns books by a specific publisher with pagination.
func (b *Backend) BooksByPublisher(publisher string, offset, limit int) ([]catalog.Book, int, error) {
	total, err := b.countBooks(`
SELECT COUNT(*) FROM books b
WHERE b.publisher = ?`, publisher)
	if err != nil {
		return nil, 0, err
	}
	books, err := b.queryBooks(`
WHERE b.publisher = ?
ORDER BY LOWER(b.title) LIMIT ? OFFSET ?`, publisher, limit, offset)
	return books, total, err
}

// Series returns all distinct non-empty series names sorted alphabetically
// with the number of books in each. It implements catalog.SeriesLister.
func (b *Backend) Series() ([]catalog.SeriesEntry, error) {
	rows, err := b.db.Query(`
SELECT series, COUNT(*) FROM books
WHERE series != ''
GROUP BY series
ORDER BY LOWER(series)`)
	if err != nil {
		return nil, fmt.Errorf("query series: %w", err)
	}
	defer rows.Close()
	var entries []catalog.SeriesEntry
	for rows.Next() {
		var e catalog.SeriesEntry
		if err := rows.Scan(&e.Name, &e.Count); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// Collections returns all distinct non-empty editorial collection names sorted
// alphabetically. It implements catalog.CollectionLister.
func (b *Backend) Collections() ([]string, error) {
	rows, err := b.db.Query(`
SELECT DISTINCT collection FROM books
WHERE collection != ''
ORDER BY LOWER(collection)`)
	if err != nil {
		return nil, fmt.Errorf("query collections: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// UpdateBook applies the given update to the book and persists it to the DB.
// It implements catalog.Updater.
func (b *Backend) UpdateBook(id string, update catalog.BookUpdate) (*catalog.Book, error) {
	bk, err := b.BookByID(id)
	if err != nil {
		return nil, err
	}

	// Apply updates to the in-memory copy.
	if update.Title != nil {
		bk.Title = *update.Title
	}
	if update.Authors != nil {
		bk.Authors = make([]catalog.Author, 0, len(update.Authors))
		for _, name := range update.Authors {
			bk.Authors = append(bk.Authors, catalog.Author{Name: name})
		}
	}
	if update.Tags != nil {
		bk.Tags = update.Tags
	}
	if update.Summary != nil {
		bk.Summary = *update.Summary
	}
	if update.Publisher != nil {
		bk.Publisher = *update.Publisher
	}
	if update.Language != nil {
		bk.Language = *update.Language
	}
	if update.Series != nil {
		bk.Series = *update.Series
	}
	if update.SeriesIndex != nil {
		bk.SeriesIndex = *update.SeriesIndex
	}
	if update.SeriesTotal != nil {
		bk.SeriesTotal = *update.SeriesTotal
	}
	if update.Collection != nil {
		bk.Collection = *update.Collection
	}
	if update.CollectionIndex != nil {
		bk.CollectionIndex = *update.CollectionIndex
	}
	if update.IsRead != nil {
		bk.IsRead = *update.IsRead
	}
	if update.Rating != nil {
		bk.Rating = *update.Rating
	}
	if update.AgeRating != nil {
		bk.AgeRating = *update.AgeRating
	}
	bk.UpdatedAt = time.Now()

	// Persist to DB.
	tx, err := b.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.Exec(`
UPDATE books SET
    title=?, summary=?, language=?, publisher=?,
    updated_at=?, series=?, series_index=?, series_total=?, collection=?, collection_index=?, is_read=?, rating=?, age_rating=?
WHERE id=?`,
		bk.Title, bk.Summary, bk.Language, bk.Publisher,
		bk.UpdatedAt.Unix(), bk.Series, bk.SeriesIndex, bk.SeriesTotal, bk.Collection, bk.CollectionIndex, boolToInt(bk.IsRead), bk.Rating, bk.AgeRating,
		id,
	)
	if err != nil {
		return nil, fmt.Errorf("update book: %w", err)
	}

	// Replace authors.
	if _, err := tx.Exec(`DELETE FROM book_authors WHERE book_id=?`, id); err != nil {
		return nil, err
	}
	for i, a := range bk.Authors {
		if _, err := tx.Exec(`INSERT INTO book_authors (book_id, author_name, author_uri, position) VALUES (?,?,?,?)`,
			id, a.Name, a.URI, i); err != nil {
			return nil, err
		}
	}

	// Replace tags.
	if _, err := tx.Exec(`DELETE FROM book_tags WHERE book_id=?`, id); err != nil {
		return nil, err
	}
	for _, t := range bk.Tags {
		if _, err := tx.Exec(`INSERT INTO book_tags (book_id, tag) VALUES (?,?)`, id, t); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return bk, nil
}

// DeleteTag removes the given tag from all books in the DB.
// It implements catalog.TagDeleter.
func (b *Backend) DeleteTag(tag string) error {
	_, err := b.db.Exec(`DELETE FROM book_tags WHERE tag = ?`, tag)
	return err
}

// StoreBook saves the uploaded file to the books directory, indexes it, and
// returns the resulting Book. It implements catalog.Uploader.
func (b *Backend) StoreBook(filename string, src io.ReadCloser) (*catalog.Book, error) {
	defer src.Close()

	filename = filepath.Base(filename)
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".epub", ".pdf":
	default:
		return nil, fmt.Errorf("unsupported file type %q (only .epub and .pdf are accepted)", ext)
	}

	destPath := filepath.Join(b.root, filename)
	if _, err := os.Stat(destPath); err == nil {
		return nil, fmt.Errorf("file %q already exists in the catalog", filename)
	}

	tmp, err := os.CreateTemp(b.root, ".upload-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("write upload: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return nil, fmt.Errorf("rename upload: %w", err)
	}

	var bk catalog.Book
	switch ext {
	case ".epub":
		bk, err = epub.ParseBook(destPath, b.coversDir)
		if err != nil {
			return nil, fmt.Errorf("parse epub %q: %w", filename, err)
		}
	case ".pdf":
		bk = epub.ParsePath(destPath)
	}

	if err := b.insertBook(bk); err != nil {
		return nil, fmt.Errorf("index uploaded book: %w", err)
	}
	b.updateMaintenanceAt(bk.ID)
	return &bk, nil
}

// Backup creates a consistent snapshot of the catalog database in destDir
// using SQLite's VACUUM INTO statement, which produces a defragmented copy
// even while the database is in use.  The backup file is named
// "catalog-YYYYMMDD-HHMMSS.db".  Afterwards the oldest backups in destDir
// are pruned so that at most keep files remain (keep ≤ 0 = unlimited).
// It implements catalog.Backupper.
func (b *Backend) Backup(destDir string, keep int) (string, error) {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", fmt.Errorf("create backup dir %q: %w", destDir, err)
	}

	name := "catalog-" + time.Now().Format("20060102-150405") + ".db"
	destPath := filepath.Join(destDir, name)

	if _, err := b.db.Exec(`VACUUM INTO ?`, destPath); err != nil {
		return "", fmt.Errorf("vacuum into %q: %w", destPath, err)
	}

	if keep > 0 {
		if err := pruneBackups(destDir, keep); err != nil {
			// Non-fatal: log via return but don't abort.
			return destPath, fmt.Errorf("prune backups: %w", err)
		}
	}
	return destPath, nil
}

// pruneBackups keeps only the most recent keep files matching the backup
// naming pattern "catalog-*.db" in dir, deleting older ones.
func pruneBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read backup dir: %w", err)
	}

	// Collect files that match the backup naming convention.
	var backups []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if len(n) >= 8 && n[:8] == "catalog-" && filepath.Ext(n) == ".db" {
			backups = append(backups, filepath.Join(dir, n))
		}
	}

	// os.ReadDir returns entries sorted by name, and our timestamp-named
	// files sort chronologically, so oldest entries are first.
	if len(backups) > keep {
		for _, old := range backups[:len(backups)-keep] {
			_ = os.Remove(old) // best-effort
		}
	}
	return nil
}

// --- query helpers ---

// bookRow is the raw data scanned from the books table plus JSON-encoded relations.
type bookRow struct {
	ID           string
	Title        string
	Summary      string
	Language     string
	Publisher    string
	PublishedAt  *int64
	UpdatedAt    int64
	AddedAt      int64
	Series       string
	SeriesIndex  string
	SeriesTotal  string
	Collection      string
	CollectionIndex string
	IsRead          int
	Rating          int
	AgeRating       int
	CoverURL     string
	ThumbnailURL string
	FilePath     string
	FileMIME     string
	FileSize     int64
	LastMaintenanceAt int64
	AuthorsJSON       *string // JSON array of {name,uri} objects, may be NULL
	TagsJSON          *string // JSON array of strings, may be NULL
}

func (r bookRow) toBook() catalog.Book {
	bk := catalog.Book{
		ID:           r.ID,
		Title:        r.Title,
		Summary:      r.Summary,
		Language:     r.Language,
		Publisher:    r.Publisher,
		Series:       r.Series,
		SeriesIndex:  r.SeriesIndex,
		SeriesTotal:  r.SeriesTotal,
		Collection:      r.Collection,
		CollectionIndex: r.CollectionIndex,
		IsRead:          r.IsRead != 0,
		Rating:          r.Rating,
		AgeRating:       r.AgeRating,
		CoverURL:     r.CoverURL,
		ThumbnailURL: r.ThumbnailURL,
		UpdatedAt:         time.Unix(r.UpdatedAt, 0),
		AddedAt:           time.Unix(r.AddedAt, 0),
		LastMaintenanceAt: func() time.Time {
			if r.LastMaintenanceAt == 0 {
				return time.Time{}
			}
			return time.Unix(r.LastMaintenanceAt, 0)
		}(),
		Files: []catalog.File{
			{MIMEType: r.FileMIME, Path: r.FilePath, Size: r.FileSize},
		},
	}
	if r.PublishedAt != nil {
		bk.PublishedAt = time.Unix(*r.PublishedAt, 0)
	}
	if r.AuthorsJSON != nil && *r.AuthorsJSON != "" {
		var raw []struct {
			Name string `json:"name"`
			URI  string `json:"uri"`
		}
		if err := json.Unmarshal([]byte(*r.AuthorsJSON), &raw); err == nil {
			for _, a := range raw {
				bk.Authors = append(bk.Authors, catalog.Author{Name: a.Name, URI: a.URI})
			}
		}
	}
	if r.TagsJSON != nil && *r.TagsJSON != "" {
		var tags []string
		if err := json.Unmarshal([]byte(*r.TagsJSON), &tags); err == nil {
			bk.Tags = tags
		}
	}
	return bk
}

// bookSelectColumns is the SELECT list for querying full book records.
const bookSelectColumns = `
    b.id, b.title, b.summary, b.language, b.publisher,
    b.published_at, b.updated_at, b.added_at, b.series, b.series_index, b.series_total, b.collection, b.collection_index, b.is_read, b.rating, b.age_rating,
    b.cover_url, b.thumbnail_url, b.file_path, b.file_mime, b.file_size,
    COALESCE(b.last_maintenance_at, 0),
    (SELECT json_group_array(json_object('name',ba.author_name,'uri',ba.author_uri))
       FROM book_authors ba WHERE ba.book_id = b.id) AS authors_json,
    (SELECT json_group_array(bt.tag)
       FROM book_tags bt WHERE bt.book_id = b.id) AS tags_json`

// queryBooks executes a SELECT with the given WHERE/JOIN/ORDER/LIMIT clause
// appended after "FROM books b". The clause may use positional ? args.
func (b *Backend) queryBooks(clause string, args ...any) ([]catalog.Book, error) {
	q := `SELECT` + bookSelectColumns + ` FROM books b ` + clause
	rows, err := b.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query books: %w", err)
	}
	defer rows.Close()

	var books []catalog.Book
	for rows.Next() {
		var r bookRow
		if err := rows.Scan(
			&r.ID, &r.Title, &r.Summary, &r.Language, &r.Publisher,
			&r.PublishedAt, &r.UpdatedAt, &r.AddedAt, &r.Series, &r.SeriesIndex, &r.SeriesTotal, &r.Collection, &r.CollectionIndex, &r.IsRead, &r.Rating, &r.AgeRating,
			&r.CoverURL, &r.ThumbnailURL, &r.FilePath, &r.FileMIME, &r.FileSize,
			&r.LastMaintenanceAt,
			&r.AuthorsJSON, &r.TagsJSON,
		); err != nil {
			return nil, err
		}
		books = append(books, r.toBook())
	}
	return books, rows.Err()
}

// countBooks executes a count query. If the query string starts with "SELECT",
// it is used as-is; otherwise it is treated as a WHERE clause appended to a
// default count query.
func (b *Backend) countBooks(query string, args ...any) (int, error) {
	// If the caller passed a full query (starts with SELECT), use it directly.
	q := query
	if !strings.HasPrefix(strings.TrimSpace(strings.ToUpper(query)), "SELECT") {
		q = `SELECT COUNT(*) FROM books b ` + query
	}
	var n int
	err := b.db.QueryRow(q, args...).Scan(&n)
	return n, err
}

// ─── UserManager ────────────────────────────────────────────────────────────

// Users returns all registered users sorted by name.
func (b *Backend) Users() ([]catalog.User, error) {
	rows, err := b.db.Query(`SELECT id, name, color, is_admin, COALESCE(is_child,0), COALESCE(max_age,10) FROM users ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []catalog.User
	for rows.Next() {
		var u catalog.User
		var isAdmin, isChild int
		if err := rows.Scan(&u.ID, &u.Name, &u.Color, &isAdmin, &isChild, &u.MaxAge); err != nil {
			return nil, err
		}
		u.IsAdmin = isAdmin == 1
		u.IsChild = isChild == 1
		users = append(users, u)
	}
	return users, rows.Err()
}

// UserByID returns the user with the given ID.
func (b *Backend) UserByID(id string) (*catalog.User, error) {
	var u catalog.User
	var isAdmin, isChild int
	err := b.db.QueryRow(`SELECT id, name, color, is_admin, COALESCE(is_child,0), COALESCE(max_age,10) FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Name, &u.Color, &isAdmin, &isChild, &u.MaxAge)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("user not found: %s", id)
	}
	if err != nil {
		return nil, err
	}
	u.IsAdmin = isAdmin == 1
	u.IsChild = isChild == 1
	return &u, nil
}

// CreateUser creates a new user and returns it.
func (b *Backend) CreateUser(name, color string, isAdmin, isChild bool, maxAge int) (*catalog.User, error) {
	id, err := newID()
	if err != nil {
		return nil, fmt.Errorf("generate user id: %w", err)
	}
	admin := 0
	if isAdmin {
		admin = 1
	}
	child := 0
	if isChild {
		child = 1
	}
	if maxAge <= 0 {
		maxAge = 10
	}
	_, err = b.db.Exec(
		`INSERT INTO users (id, name, color, is_admin, is_child, max_age) VALUES (?, ?, ?, ?, ?, ?)`,
		id, name, color, admin, child, maxAge,
	)
	if err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}
	return &catalog.User{ID: id, Name: name, Color: color, IsAdmin: isAdmin, IsChild: isChild, MaxAge: maxAge}, nil
}

// DeleteUser removes the user with the given ID.
func (b *Backend) DeleteUser(id string) error {
	_, err := b.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

// UpdateUser updates the name, color, admin, child status and max age of an existing user.
func (b *Backend) UpdateUser(id, name, color string, isAdmin, isChild bool, maxAge int) (*catalog.User, error) {
	admin := 0
	if isAdmin {
		admin = 1
	}
	child := 0
	if isChild {
		child = 1
	}
	if maxAge <= 0 {
		maxAge = 10
	}
	_, err := b.db.Exec(
		`UPDATE users SET name = ?, color = ?, is_admin = ?, is_child = ?, max_age = ? WHERE id = ?`,
		name, color, admin, child, maxAge, id,
	)
	if err != nil {
		return nil, fmt.Errorf("update user: %w", err)
	}
	return &catalog.User{ID: id, Name: name, Color: color, IsAdmin: isAdmin, IsChild: isChild, MaxAge: maxAge}, nil
}

// ─── UserReadManager ─────────────────────────────────────────────────────────

// SetUserRead marks (isRead=true) or clears (isRead=false) a book as read for a user.
func (b *Backend) SetUserRead(userID, bookID string, isRead bool) error {
	if isRead {
		_, err := b.db.Exec(`
INSERT INTO user_read_status (user_id, book_id, is_read) VALUES (?, ?, 1)
ON CONFLICT(user_id, book_id) DO UPDATE SET is_read = 1`, userID, bookID)
		return err
	}
	_, err := b.db.Exec(
		`DELETE FROM user_read_status WHERE user_id = ? AND book_id = ?`,
		userID, bookID,
	)
	return err
}

// UserReadStatuses returns a map of bookID → isRead for the given user
// and the supplied list of book IDs.
func (b *Backend) UserReadStatuses(userID string, bookIDs []string) (map[string]bool, error) {
	result := make(map[string]bool, len(bookIDs))
	if len(bookIDs) == 0 || userID == "" {
		return result, nil
	}
	// Build placeholders: (?, ?, ...)
	placeholders := strings.Repeat("?,", len(bookIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := []any{userID}
	for _, id := range bookIDs {
		args = append(args, id)
	}
	rows, err := b.db.Query(
		`SELECT book_id, is_read FROM user_read_status WHERE user_id = ? AND book_id IN (`+placeholders+`)`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var bookID string
		var isRead int
		if err := rows.Scan(&bookID, &isRead); err != nil {
			return nil, err
		}
		result[bookID] = isRead == 1
	}
	return result, rows.Err()
}

// BookReadColors returns for each supplied bookID the hex colours of all users
// who have marked that book as read.
func (b *Backend) BookReadColors(bookIDs []string) (map[string][]string, error) {
	result := make(map[string][]string)
	if len(bookIDs) == 0 {
		return result, nil
	}
	placeholders := strings.Repeat("?,", len(bookIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(bookIDs))
	for i, id := range bookIDs {
		args[i] = id
	}
	rows, err := b.db.Query(`
SELECT urs.book_id, u.color
FROM user_read_status urs
JOIN users u ON u.id = urs.user_id
WHERE urs.is_read = 1 AND urs.book_id IN (`+placeholders+`)
ORDER BY urs.book_id, u.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var bookID, color string
		if err := rows.Scan(&bookID, &color); err != nil {
			return nil, err
		}
		result[bookID] = append(result[bookID], color)
	}
	return result, rows.Err()
}

// ─── Recommender ─────────────────────────────────────────────────────────────

// migration5 adds the recommendations table (version 4 → 5).
func migration5(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS recommendations (
    from_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    to_user_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    book_id      TEXT NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    message      TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (from_user_id, to_user_id, book_id)
);
CREATE INDEX IF NOT EXISTS idx_rec_to_user   ON recommendations(to_user_id);
CREATE INDEX IF NOT EXISTS idx_rec_from_user ON recommendations(from_user_id);
`)
	return err
}

// RecommendBook creates or replaces a recommendation from fromUserID to toUserID for bookID.
func (b *Backend) RecommendBook(fromUserID, toUserID, bookID, message string) error {
	_, err := b.db.Exec(`
INSERT INTO recommendations (from_user_id, to_user_id, book_id, message, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(from_user_id, to_user_id, book_id) DO UPDATE SET
    message = excluded.message, created_at = excluded.created_at`,
		fromUserID, toUserID, bookID, message, time.Now().Unix())
	return err
}

// RemoveRecommendation deletes the recommendation (if any) from fromUserID to toUserID for bookID.
func (b *Backend) RemoveRecommendation(fromUserID, toUserID, bookID string) error {
	_, err := b.db.Exec(
		`DELETE FROM recommendations WHERE from_user_id=? AND to_user_id=? AND book_id=?`,
		fromUserID, toUserID, bookID)
	return err
}

// scanRecommendationRows scans rows produced by a query that selects:
//
//	user.id, user.name, user.color, user.is_admin,
//	r.message, r.created_at,
//	<bookSelectColumns>
//
// otherUserFirst controls whether the first 4 columns are the "from" user (true)
// or the "to" user (false).
func scanRecommendationRows(rows *sql.Rows, fromFirst bool) ([]catalog.Recommendation, error) {
	defer rows.Close()
	var result []catalog.Recommendation
	for rows.Next() {
		var u catalog.User
		var isAdminInt int
		var message string
		var createdAtUnix int64
		var r bookRow
		if err := rows.Scan(
			&u.ID, &u.Name, &u.Color, &isAdminInt,
			&message, &createdAtUnix,
			&r.ID, &r.Title, &r.Summary, &r.Language, &r.Publisher,
			&r.PublishedAt, &r.UpdatedAt, &r.AddedAt,
			&r.Series, &r.SeriesIndex, &r.SeriesTotal,
			&r.Collection, &r.CollectionIndex,
			&r.IsRead, &r.Rating, &r.AgeRating,
			&r.CoverURL, &r.ThumbnailURL, &r.FilePath, &r.FileMIME, &r.FileSize,
			&r.LastMaintenanceAt,
			&r.AuthorsJSON, &r.TagsJSON,
		); err != nil {
			return nil, err
		}
		u.IsAdmin = isAdminInt == 1
		rec := catalog.Recommendation{
			Book:      r.toBook(),
			Message:   message,
			CreatedAt: time.Unix(createdAtUnix, 0),
		}
		if fromFirst {
			rec.FromUser = u
		} else {
			rec.ToUser = u
		}
		result = append(result, rec)
	}
	return result, rows.Err()
}

// RecommendationsForUser returns all recommendations addressed to toUserID, newest first.
func (b *Backend) RecommendationsForUser(toUserID string) ([]catalog.Recommendation, error) {
	rows, err := b.db.Query(`
SELECT fu.id, fu.name, fu.color, fu.is_admin,
       r.message, r.created_at,`+bookSelectColumns+`
FROM recommendations r
JOIN users fu ON fu.id = r.from_user_id
JOIN books b  ON b.id  = r.book_id
WHERE r.to_user_id = ?
ORDER BY r.created_at DESC`, toUserID)
	if err != nil {
		return nil, err
	}
	recs, err := scanRecommendationRows(rows, true)
	if err != nil {
		return nil, err
	}
	// Populate ToUser for each rec (we know it's the queried user).
	tu, err := b.UserByID(toUserID)
	if err != nil {
		return nil, err
	}
	for i := range recs {
		recs[i].ToUser = *tu
	}
	return recs, nil
}

// RecommendationsByUser returns all recommendations sent by fromUserID, newest first.
func (b *Backend) RecommendationsByUser(fromUserID string) ([]catalog.Recommendation, error) {
	rows, err := b.db.Query(`
SELECT tu.id, tu.name, tu.color, tu.is_admin,
       r.message, r.created_at,`+bookSelectColumns+`
FROM recommendations r
JOIN users tu ON tu.id = r.to_user_id
JOIN books b  ON b.id  = r.book_id
WHERE r.from_user_id = ?
ORDER BY r.created_at DESC`, fromUserID)
	if err != nil {
		return nil, err
	}
	recs, err := scanRecommendationRows(rows, false)
	if err != nil {
		return nil, err
	}
	// Populate FromUser for each rec.
	fu, err := b.UserByID(fromUserID)
	if err != nil {
		return nil, err
	}
	for i := range recs {
		recs[i].FromUser = *fu
	}
	return recs, nil
}

// BookRecipients returns the IDs of users to whom fromUserID has recommended bookID.
func (b *Backend) BookRecipients(fromUserID, bookID string) ([]string, error) {
	rows, err := b.db.Query(
		`SELECT to_user_id FROM recommendations WHERE from_user_id=? AND book_id=?`,
		fromUserID, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ─── WishlistManager ──────────────────────────────────────────────────────────

// migration6 adds the wishlist table (version 5 → 6).
func migration6(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS wishlist (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL DEFAULT '',
    title        TEXT NOT NULL DEFAULT '',
    author       TEXT NOT NULL DEFAULT '',
    release_date TEXT NOT NULL DEFAULT '',
    notes        TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_wishlist_user ON wishlist(user_id);
`)
	return err
}

// WishlistItems returns all wishlist items. If userID is non-empty, only that user's items are returned.
func (b *Backend) WishlistItems(userID string) ([]catalog.WishlistItem, error) {
	var rows *sql.Rows
	var err error
	if userID != "" {
		rows, err = b.db.Query(`
SELECT w.id, w.user_id, COALESCE(u.name,''), COALESCE(u.color,'#6B7280'),
       w.title, w.author, w.release_date, w.notes, w.created_at
FROM wishlist w
LEFT JOIN users u ON u.id = w.user_id
WHERE w.user_id = ?
ORDER BY w.created_at DESC`, userID)
	} else {
		rows, err = b.db.Query(`
SELECT w.id, w.user_id, COALESCE(u.name,''), COALESCE(u.color,'#6B7280'),
       w.title, w.author, w.release_date, w.notes, w.created_at
FROM wishlist w
LEFT JOIN users u ON u.id = w.user_id
ORDER BY w.created_at DESC`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []catalog.WishlistItem
	for rows.Next() {
		var it catalog.WishlistItem
		var createdAtUnix int64
		if err := rows.Scan(
			&it.ID, &it.UserID, &it.UserName, &it.UserColor,
			&it.Title, &it.Author, &it.ReleaseDate, &it.Notes, &createdAtUnix,
		); err != nil {
			return nil, err
		}
		it.CreatedAt = time.Unix(createdAtUnix, 0)
		items = append(items, it)
	}
	return items, rows.Err()
}

// AddWishlistItem creates a new wishlist item and returns it.
func (b *Backend) AddWishlistItem(userID, title, author, releaseDate, notes string) (*catalog.WishlistItem, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	_, err = b.db.Exec(`
INSERT INTO wishlist (id, user_id, title, author, release_date, notes, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, userID, title, author, releaseDate, notes, now.Unix())
	if err != nil {
		return nil, err
	}
	it := &catalog.WishlistItem{
		ID:          id,
		UserID:      userID,
		Title:       title,
		Author:      author,
		ReleaseDate: releaseDate,
		Notes:       notes,
		CreatedAt:   now,
	}
	// Populate user name/color if available.
	if userID != "" {
		if u, err := b.UserByID(userID); err == nil {
			it.UserName = u.Name
			it.UserColor = u.Color
		}
	}
	return it, nil
}

// UpdateWishlistItem updates the editable fields of an existing wishlist item.
func (b *Backend) UpdateWishlistItem(id, title, author, releaseDate, notes string) (*catalog.WishlistItem, error) {
	_, err := b.db.Exec(`
UPDATE wishlist SET title=?, author=?, release_date=?, notes=? WHERE id=?`,
		title, author, releaseDate, notes, id)
	if err != nil {
		return nil, err
	}
	// Re-fetch to return the full item.
	rows, err := b.db.Query(`
SELECT w.id, w.user_id, COALESCE(u.name,''), COALESCE(u.color,'#6B7280'),
       w.title, w.author, w.release_date, w.notes, w.created_at
FROM wishlist w
LEFT JOIN users u ON u.id = w.user_id
WHERE w.id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, fmt.Errorf("wishlist item %q not found", id)
	}
	var it catalog.WishlistItem
	var createdAtUnix int64
	if err := rows.Scan(
		&it.ID, &it.UserID, &it.UserName, &it.UserColor,
		&it.Title, &it.Author, &it.ReleaseDate, &it.Notes, &createdAtUnix,
	); err != nil {
		return nil, err
	}
	it.CreatedAt = time.Unix(createdAtUnix, 0)
	return &it, nil
}

// DeleteWishlistItem removes the wishlist item with the given ID.
func (b *Backend) DeleteWishlistItem(id string) error {
	_, err := b.db.Exec(`DELETE FROM wishlist WHERE id=?`, id)
	return err
}

// newID generates a random 16-byte hex string for use as a unique ID.
func newID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// migration7 adds age_rating to books and is_child to users (version 6 → 7).
func migration7(db *sql.DB) error {
	_, _ = db.Exec(`ALTER TABLE books ADD COLUMN age_rating INTEGER NOT NULL DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE users ADD COLUMN is_child INTEGER NOT NULL DEFAULT 0`)
	return nil
}

// migration8 adds the sessions table for persistent session storage (version 7 → 8).
func migration8(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS sessions (
    token    TEXT PRIMARY KEY,
    user_id  TEXT NOT NULL DEFAULT '',
    expiry   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expiry);
`)
	return err
}

// migration9 adds max_age to users for per-child-profile age filtering (version 8 → 9).
func migration9(db *sql.DB) error {
	_, _ = db.Exec(`ALTER TABLE users ADD COLUMN max_age INTEGER NOT NULL DEFAULT 10`)
	return nil
}

// migration10 adds last_maintenance_at to books for per-book indexing timestamp (version 9 → 10).
func migration10(db *sql.DB) error {
	_, _ = db.Exec(`ALTER TABLE books ADD COLUMN last_maintenance_at INTEGER NOT NULL DEFAULT 0`)
	return nil
}

// SaveSession upserts a session token into the sessions table.
// Implements catalog.SessionPersistence.
func (b *Backend) SaveSession(token, userID string, expiry time.Time) error {
	_, err := b.db.Exec(
		`INSERT INTO sessions (token, user_id, expiry) VALUES (?, ?, ?)
         ON CONFLICT(token) DO UPDATE SET user_id=excluded.user_id, expiry=excluded.expiry`,
		token, userID, expiry.Unix(),
	)
	return err
}

// DeleteSession removes a session token from the sessions table.
// Implements catalog.SessionPersistence.
func (b *Backend) DeleteSession(token string) error {
	_, err := b.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// LoadSessions returns all non-expired session tokens from the database.
// Implements catalog.SessionPersistence.
func (b *Backend) LoadSessions() ([]catalog.SessionData, error) {
	now := time.Now().Unix()
	rows, err := b.db.Query(
		`SELECT token, user_id, expiry FROM sessions WHERE expiry > ?`, now,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []catalog.SessionData
	for rows.Next() {
		var sd catalog.SessionData
		var expiryUnix int64
		if err := rows.Scan(&sd.Token, &sd.UserID, &expiryUnix); err != nil {
			return nil, err
		}
		sd.Expiry = time.Unix(expiryUnix, 0)
		sessions = append(sessions, sd)
	}
	return sessions, rows.Err()
}

// PruneExpiredSessions removes all sessions whose expiry is in the past.
// Implements catalog.SessionPersistence.
func (b *Backend) PruneExpiredSessions() error {
	_, err := b.db.Exec(`DELETE FROM sessions WHERE expiry <= ?`, time.Now().Unix())
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
