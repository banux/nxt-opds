// Package sqlite implements a SQLite-backed catalog backend for nxt-opds.
// It scans a directory for EPUB/PDF files and persists all book metadata
// (including user overrides) in a SQLite database, enabling efficient queries
// and full-text search for large collections.
package sqlite

import (
	"database/sql"
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

// New opens (or creates) the SQLite catalog at {dir}/.catalog.db, applies the
// schema, syncs the filesystem, and returns the Backend.
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
	if err := b.createSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
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

// createSchema creates the tables if they don't exist yet.
func (b *Backend) createSchema() error {
	_, err := b.db.Exec(`
CREATE TABLE IF NOT EXISTS books (
    id           TEXT PRIMARY KEY,
    title        TEXT NOT NULL DEFAULT '',
    summary      TEXT NOT NULL DEFAULT '',
    language     TEXT NOT NULL DEFAULT '',
    publisher    TEXT NOT NULL DEFAULT '',
    published_at INTEGER,
    updated_at   INTEGER NOT NULL,
    added_at     INTEGER NOT NULL DEFAULT 0,
    series       TEXT NOT NULL DEFAULT '',
    series_index TEXT NOT NULL DEFAULT '',
    is_read      INTEGER NOT NULL DEFAULT 0,
    cover_url    TEXT NOT NULL DEFAULT '',
    thumbnail_url TEXT NOT NULL DEFAULT '',
    file_path    TEXT NOT NULL,
    file_mime    TEXT NOT NULL DEFAULT '',
    file_size    INTEGER NOT NULL DEFAULT 0
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
CREATE INDEX IF NOT EXISTS idx_book_tags_tag ON book_tags(tag);
CREATE INDEX IF NOT EXISTS idx_books_added_at ON books(added_at DESC);
`)
	if err != nil {
		return err
	}
	// Migration: add added_at column to existing databases (ignore error if already exists).
	_, _ = b.db.Exec(`ALTER TABLE books ADD COLUMN added_at INTEGER NOT NULL DEFAULT 0`)
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
     series, series_index, is_read, cover_url, thumbnail_url,
     file_path, file_mime, file_size)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		bk.ID, bk.Title, bk.Summary, bk.Language, bk.Publisher,
		pubAt, updAt, addedAt,
		bk.Series, bk.SeriesIndex, boolToInt(bk.IsRead),
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

// CoverPath returns the filesystem path to the cached cover image for a book ID.
func (b *Backend) CoverPath(id string) (string, error) {
	return epub.CoverPath(b.coversDir, id)
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
// If q.Query is empty all books are candidates (filtered only by q.UnreadOnly).
func (b *Backend) Search(q catalog.SearchQuery) ([]catalog.Book, int, error) {
	unreadClause := ""
	if q.UnreadOnly {
		unreadClause = " AND b.is_read = 0"
	}

	orderBy := "ORDER BY " + sortClause(q)

	if q.Query == "" {
		// No text search: just apply optional unread filter.
		total, err := b.countBooks(`SELECT COUNT(*) FROM books b WHERE 1=1` + unreadClause)
		if err != nil {
			return nil, 0, err
		}
		books, err := b.queryBooks(`WHERE 1=1`+unreadClause+` `+orderBy+` LIMIT ? OFFSET ?`, q.Limit, q.Offset)
		return books, total, err
	}

	like := "%" + strings.ToLower(q.Query) + "%"

	total, err := b.countBooks(`
SELECT COUNT(DISTINCT b.id) FROM books b
LEFT JOIN book_authors ba ON ba.book_id = b.id
WHERE (LOWER(b.title) LIKE ? OR LOWER(ba.author_name) LIKE ?)`+unreadClause, like, like)
	if err != nil {
		return nil, 0, err
	}

	books, err := b.queryBooks(`
JOIN (
    SELECT DISTINCT b2.id FROM books b2
    LEFT JOIN book_authors ba2 ON ba2.book_id = b2.id
    WHERE (LOWER(b2.title) LIKE ? OR LOWER(ba2.author_name) LIKE ?)
) AS matched ON b.id = matched.id
WHERE 1=1`+unreadClause+`
`+orderBy+` LIMIT ? OFFSET ?`,
		like, like, q.Limit, q.Offset)
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
	if update.IsRead != nil {
		bk.IsRead = *update.IsRead
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
    updated_at=?, series=?, series_index=?, is_read=?
WHERE id=?`,
		bk.Title, bk.Summary, bk.Language, bk.Publisher,
		bk.UpdatedAt.Unix(), bk.Series, bk.SeriesIndex, boolToInt(bk.IsRead),
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
	return &bk, nil
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
	IsRead       int
	CoverURL     string
	ThumbnailURL string
	FilePath     string
	FileMIME     string
	FileSize     int64
	AuthorsJSON  *string // JSON array of {name,uri} objects, may be NULL
	TagsJSON     *string // JSON array of strings, may be NULL
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
		IsRead:       r.IsRead != 0,
		CoverURL:     r.CoverURL,
		ThumbnailURL: r.ThumbnailURL,
		UpdatedAt:    time.Unix(r.UpdatedAt, 0),
		AddedAt:      time.Unix(r.AddedAt, 0),
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
    b.published_at, b.updated_at, b.added_at, b.series, b.series_index, b.is_read,
    b.cover_url, b.thumbnail_url, b.file_path, b.file_mime, b.file_size,
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
			&r.PublishedAt, &r.UpdatedAt, &r.AddedAt, &r.Series, &r.SeriesIndex, &r.IsRead,
			&r.CoverURL, &r.ThumbnailURL, &r.FilePath, &r.FileMIME, &r.FileSize,
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

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
