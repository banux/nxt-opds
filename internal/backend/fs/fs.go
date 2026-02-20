// Package fs implements a filesystem-based catalog backend for nxt-opds.
// It scans a directory recursively for EPUB and PDF files and builds an
// in-memory catalog by extracting metadata from each file.
package fs

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/banux/nxt-opds/internal/catalog"
	"github.com/banux/nxt-opds/internal/epub"
)

// metaOverride stores user-edited metadata for a single book.
// Pointer fields: nil = not overridden; non-nil = override active (even if empty string).
// Slice fields: nil = not overridden; non-nil (including empty) = override active.
type metaOverride struct {
	Title       *string  `json:"title"`
	Authors     []string `json:"authors"`
	Tags        []string `json:"tags"`
	Summary     *string  `json:"summary"`
	Publisher   *string  `json:"publisher"`
	Language    *string  `json:"language"`
	Series      *string  `json:"series"`
	SeriesIndex *string  `json:"seriesIndex"`
	SeriesTotal *string  `json:"seriesTotal"`
	IsRead      *bool    `json:"isRead"`
}

// Backend is a filesystem-based catalog backend.
// It scans a root directory for EPUB/PDF files on creation (or on Refresh).
type Backend struct {
	root         string
	coversDir    string // {root}/.covers – extracted cover images
	metadataPath string // {root}/.metadata.json – user metadata overrides

	mu        sync.RWMutex
	books     []catalog.Book
	byID      map[string]*catalog.Book
	authors   map[string][]string // author name -> book IDs
	tags      map[string][]string // tag -> book IDs
	overrides map[string]metaOverride // book ID -> user-edited metadata
}

// New creates a new filesystem backend rooted at dir and performs an initial scan.
func New(dir string) (*Backend, error) {
	coversDir := filepath.Join(dir, ".covers")
	if err := os.MkdirAll(coversDir, 0755); err != nil {
		return nil, fmt.Errorf("create covers dir: %w", err)
	}
	b := &Backend{
		root:         dir,
		coversDir:    coversDir,
		metadataPath: filepath.Join(dir, ".metadata.json"),
		byID:         make(map[string]*catalog.Book),
		authors:      make(map[string][]string),
		tags:         make(map[string][]string),
		overrides:    make(map[string]metaOverride),
	}
	// Load persisted metadata overrides (ignore error if file doesn't exist yet)
	_ = b.loadOverrides()
	if err := b.Refresh(); err != nil {
		return nil, err
	}
	return b, nil
}

// loadOverrides reads the .metadata.json file into b.overrides.
func (b *Backend) loadOverrides() error {
	data, err := os.ReadFile(b.metadataPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read metadata: %w", err)
	}
	return json.Unmarshal(data, &b.overrides)
}

// saveOverrides persists b.overrides to .metadata.json.
func (b *Backend) saveOverrides() error {
	data, err := json.MarshalIndent(b.overrides, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	if err := os.WriteFile(b.metadataPath, data, 0644); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}
	return nil
}

// applyOverride merges any stored override for bk.ID on top of bk.
func (b *Backend) applyOverride(bk catalog.Book) catalog.Book {
	ov, ok := b.overrides[bk.ID]
	if !ok {
		return bk
	}
	return mergeOverride(bk, ov)
}

// mergeOverride applies an override to a book copy and returns it.
func mergeOverride(bk catalog.Book, ov metaOverride) catalog.Book {
	if ov.Title != nil {
		bk.Title = *ov.Title
	}
	if ov.Authors != nil {
		bk.Authors = make([]catalog.Author, 0, len(ov.Authors))
		for _, name := range ov.Authors {
			bk.Authors = append(bk.Authors, catalog.Author{Name: name})
		}
	}
	if ov.Tags != nil {
		bk.Tags = ov.Tags
	}
	if ov.Summary != nil {
		bk.Summary = *ov.Summary
	}
	if ov.Publisher != nil {
		bk.Publisher = *ov.Publisher
	}
	if ov.Language != nil {
		bk.Language = *ov.Language
	}
	if ov.Series != nil {
		bk.Series = *ov.Series
	}
	if ov.SeriesIndex != nil {
		bk.SeriesIndex = *ov.SeriesIndex
	}
	if ov.SeriesTotal != nil {
		bk.SeriesTotal = *ov.SeriesTotal
	}
	if ov.IsRead != nil {
		bk.IsRead = *ov.IsRead
	}
	return bk
}

// UpdateBook applies the given update to the book with the given ID, persists
// the override to .metadata.json, and returns the updated Book.
// It implements catalog.Updater.
func (b *Backend) UpdateBook(id string, update catalog.BookUpdate) (*catalog.Book, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	bk, ok := b.byID[id]
	if !ok {
		return nil, fmt.Errorf("book %q not found", id)
	}

	ov := b.overrides[id]

	if update.Title != nil {
		ov.Title = update.Title
	}
	if update.Authors != nil {
		ov.Authors = update.Authors
	}
	if update.Tags != nil {
		ov.Tags = update.Tags
	}
	if update.Summary != nil {
		ov.Summary = update.Summary
	}
	if update.Publisher != nil {
		ov.Publisher = update.Publisher
	}
	if update.Language != nil {
		ov.Language = update.Language
	}
	if update.Series != nil {
		ov.Series = update.Series
	}
	if update.SeriesIndex != nil {
		ov.SeriesIndex = update.SeriesIndex
	}
	if update.SeriesTotal != nil {
		ov.SeriesTotal = update.SeriesTotal
	}
	if update.IsRead != nil {
		ov.IsRead = update.IsRead
	}

	b.overrides[id] = ov

	// Rebuild indexes: remove old author/tag entries for this book
	for name, ids := range b.authors {
		b.authors[name] = removeID(ids, id)
	}
	for tag, ids := range b.tags {
		b.tags[tag] = removeID(ids, id)
	}

	updated := b.applyOverride(*bk)
	*bk = updated

	for _, a := range bk.Authors {
		b.authors[a.Name] = append(b.authors[a.Name], bk.ID)
	}
	for _, t := range bk.Tags {
		b.tags[t] = append(b.tags[t], bk.ID)
	}

	bk.UpdatedAt = time.Now()

	if err := b.saveOverrides(); err != nil {
		_ = err
	}

	result := *bk
	return &result, nil
}

// removeID removes the first occurrence of id from ids slice.
func removeID(ids []string, id string) []string {
	for i, v := range ids {
		if v == id {
			return append(ids[:i], ids[i+1:]...)
		}
	}
	return ids
}

// CoverPath returns the filesystem path to the cached cover image for a book ID.
func (b *Backend) CoverPath(id string) (string, error) {
	return epub.CoverPath(b.coversDir, id)
}

// Refresh re-scans the root directory and rebuilds the in-memory catalog.
func (b *Backend) Refresh() error {
	var books []catalog.Book

	err := filepath.WalkDir(b.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".epub":
			book, err := epub.ParseBook(path, b.coversDir)
			if err != nil {
				return nil
			}
			books = append(books, book)
		case ".pdf":
			books = append(books, epub.ParsePath(path))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("scanning directory %q: %w", b.root, err)
	}

	b.mu.RLock()
	overrides := b.overrides
	b.mu.RUnlock()
	for i := range books {
		if ov, ok := overrides[books[i].ID]; ok {
			books[i] = mergeOverride(books[i], ov)
		}
	}

	// Default sort: newest first (by file mod time / AddedAt).
	sort.Slice(books, func(i, j int) bool {
		return books[i].AddedAt.After(books[j].AddedAt)
	})

	byID := make(map[string]*catalog.Book, len(books))
	authors := make(map[string][]string)
	tags := make(map[string][]string)

	for i := range books {
		bk := &books[i]
		byID[bk.ID] = bk
		for _, a := range bk.Authors {
			authors[a.Name] = append(authors[a.Name], bk.ID)
		}
		for _, t := range bk.Tags {
			tags[t] = append(tags[t], bk.ID)
		}
	}

	b.mu.Lock()
	b.books = books
	b.byID = byID
	b.authors = authors
	b.tags = tags
	b.mu.Unlock()
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

// AllBooks returns all books with pagination.
func (b *Backend) AllBooks(offset, limit int) ([]catalog.Book, int, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	total := len(b.books)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return b.books[offset:end], total, nil
}

// BookByID returns a single book by its ID.
func (b *Backend) BookByID(id string) (*catalog.Book, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	bk, ok := b.byID[id]
	if !ok {
		return nil, fmt.Errorf("book %q not found", id)
	}
	return bk, nil
}

// Search performs a basic case-insensitive substring search over title and author.
// If q.Query is empty all books are candidates (filtered only by q.UnreadOnly).
func (b *Backend) Search(q catalog.SearchQuery) ([]catalog.Book, int, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	qLower := strings.ToLower(q.Query)
	var matched []catalog.Book
	for _, bk := range b.books {
		if q.UnreadOnly && bk.IsRead {
			continue
		}
		if q.Query == "" {
			matched = append(matched, bk)
			continue
		}
		if strings.Contains(strings.ToLower(bk.Title), qLower) {
			matched = append(matched, bk)
			continue
		}
		for _, a := range bk.Authors {
			if strings.Contains(strings.ToLower(a.Name), qLower) {
				matched = append(matched, bk)
				break
			}
		}
	}

	// Apply sort (default: added date descending; b.books already sorted that way,
	// but if a different sort is requested we re-sort the matched slice).
	if q.SortBy == "title" {
		if q.SortOrder == "asc" {
			sort.Slice(matched, func(i, j int) bool {
				return strings.ToLower(matched[i].Title) < strings.ToLower(matched[j].Title)
			})
		} else {
			sort.Slice(matched, func(i, j int) bool {
				return strings.ToLower(matched[i].Title) > strings.ToLower(matched[j].Title)
			})
		}
	} else if q.SortBy == "added" && q.SortOrder == "asc" {
		sort.Slice(matched, func(i, j int) bool {
			return matched[i].AddedAt.Before(matched[j].AddedAt)
		})
	}
	// default (added desc) is already the natural order from b.books

	total := len(matched)
	offset := q.Offset
	if offset >= total {
		return nil, total, nil
	}
	end := offset + q.Limit
	if end > total || q.Limit == 0 {
		end = total
	}
	return matched[offset:end], total, nil
}

// BooksByAuthor returns books by a specific author with pagination.
func (b *Backend) BooksByAuthor(author string, offset, limit int) ([]catalog.Book, int, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	ids := b.authors[author]
	total := len(ids)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}

	books := make([]catalog.Book, 0, end-offset)
	for _, id := range ids[offset:end] {
		if bk, ok := b.byID[id]; ok {
			books = append(books, *bk)
		}
	}
	return books, total, nil
}

// BooksByTag returns books with a specific tag with pagination.
func (b *Backend) BooksByTag(tag string, offset, limit int) ([]catalog.Book, int, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	ids := b.tags[tag]
	total := len(ids)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}

	books := make([]catalog.Book, 0, end-offset)
	for _, id := range ids[offset:end] {
		if bk, ok := b.byID[id]; ok {
			books = append(books, *bk)
		}
	}
	return books, total, nil
}

// Authors returns all distinct author names with pagination.
func (b *Backend) Authors(offset, limit int) ([]string, int, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	names := make([]string, 0, len(b.authors))
	for name := range b.authors {
		names = append(names, name)
	}

	total := len(names)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return names[offset:end], total, nil
}

// Tags returns all distinct tags with pagination.
func (b *Backend) Tags(offset, limit int) ([]string, int, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	tagList := make([]string, 0, len(b.tags))
	for t := range b.tags {
		tagList = append(tagList, t)
	}

	total := len(tagList)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return tagList[offset:end], total, nil
}

// DeleteBook removes the book with the given ID from the catalog and deletes
// its file(s) and cover image from disk. It implements catalog.Deleter.
func (b *Backend) DeleteBook(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	bk, ok := b.byID[id]
	if !ok {
		return fmt.Errorf("book %q not found", id)
	}

	// Delete each associated file.
	for _, f := range bk.Files {
		_ = os.Remove(f.Path)
	}

	// Delete the cached cover image if it exists.
	coverPath := filepath.Join(b.coversDir, id+".jpg")
	_ = os.Remove(coverPath)

	// Remove from in-memory indexes.
	for name, ids := range b.authors {
		b.authors[name] = removeID(ids, id)
	}
	for tag, ids := range b.tags {
		b.tags[tag] = removeID(ids, id)
	}
	delete(b.byID, id)
	for i, bk := range b.books {
		if bk.ID == id {
			b.books = append(b.books[:i], b.books[i+1:]...)
			break
		}
	}

	// Remove override entry and persist.
	delete(b.overrides, id)
	_ = b.saveOverrides()

	return nil
}

// StoreBook writes src to the backend's root directory as filename, then
// parses and indexes it immediately. It implements catalog.Uploader.
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

	var book catalog.Book
	switch ext {
	case ".epub":
		book, err = epub.ParseBook(destPath, b.coversDir)
		if err != nil {
			return nil, fmt.Errorf("parse epub %q: %w", filename, err)
		}
	case ".pdf":
		book = epub.ParsePath(destPath)
	}

	b.mu.Lock()
	if ov, ok := b.overrides[book.ID]; ok {
		book = mergeOverride(book, ov)
	}
	// Prepend so the new book appears first in the default (newest-first) order.
	b.books = append([]catalog.Book{book}, b.books...)
	bk := &b.books[0]
	b.byID[bk.ID] = bk
	for _, a := range bk.Authors {
		b.authors[a.Name] = append(b.authors[a.Name], bk.ID)
	}
	for _, t := range bk.Tags {
		b.tags[t] = append(b.tags[t], bk.ID)
	}
	b.mu.Unlock()

	return bk, nil
}
