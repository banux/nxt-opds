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
	"strconv"
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
	Collection      *string    `json:"collection"`
	CollectionIndex *string    `json:"collectionIndex"`
	IsRead          *bool      `json:"isRead"`
	Rating          *int       `json:"rating"`
	AgeRating       *int       `json:"ageRating"`
	SpiceRating     *int       `json:"spiceRating"`
	LastMaintenanceAt *int64   `json:"lastMaintenanceAt"` // Unix seconds, 0 = cleared
}

// Backend is a filesystem-based catalog backend.
// It scans a root directory for EPUB/PDF files on creation (or on Refresh).
type Backend struct {
	root          string
	coversDir     string // {root}/.covers – extracted cover images
	metadataPath  string // {root}/.metadata.json – user metadata overrides
	librarianPath string // {root}/.librarian.json – librarian association singleton

	mu         sync.RWMutex
	books      []catalog.Book
	byID       map[string]*catalog.Book
	authors    map[string][]string // author name -> book IDs
	tags       map[string][]string // tag -> book IDs
	publishers map[string][]string // publisher name -> book IDs
	overrides  map[string]metaOverride // book ID -> user-edited metadata
}

// New creates a new filesystem backend rooted at dir and performs an initial scan.
func New(dir string) (*Backend, error) {
	coversDir := filepath.Join(dir, ".covers")
	if err := os.MkdirAll(coversDir, 0755); err != nil {
		return nil, fmt.Errorf("create covers dir: %w", err)
	}
	b := &Backend{
		root:          dir,
		coversDir:     coversDir,
		metadataPath:  filepath.Join(dir, ".metadata.json"),
		librarianPath: filepath.Join(dir, ".librarian.json"),
		byID:          make(map[string]*catalog.Book),
		authors:       make(map[string][]string),
		tags:          make(map[string][]string),
		publishers:    make(map[string][]string),
		overrides:     make(map[string]metaOverride),
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
	if ov.Collection != nil {
		bk.Collection = *ov.Collection
	}
	if ov.CollectionIndex != nil {
		bk.CollectionIndex = *ov.CollectionIndex
	}
	if ov.IsRead != nil {
		bk.IsRead = *ov.IsRead
	}
	if ov.Rating != nil {
		bk.Rating = *ov.Rating
	}
	if ov.AgeRating != nil {
		bk.AgeRating = *ov.AgeRating
	}
	if ov.SpiceRating != nil {
		bk.SpiceRating = *ov.SpiceRating
	}
	if ov.LastMaintenanceAt != nil {
		if *ov.LastMaintenanceAt == 0 {
			bk.LastMaintenanceAt = time.Time{}
		} else {
			bk.LastMaintenanceAt = time.Unix(*ov.LastMaintenanceAt, 0)
		}
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
	if update.Collection != nil {
		ov.Collection = update.Collection
	}
	if update.CollectionIndex != nil {
		ov.CollectionIndex = update.CollectionIndex
	}
	if update.IsRead != nil {
		ov.IsRead = update.IsRead
	}
	if update.Rating != nil {
		ov.Rating = update.Rating
	}
	if update.AgeRating != nil {
		ov.AgeRating = update.AgeRating
	}
	if update.SpiceRating != nil {
		ov.SpiceRating = update.SpiceRating
	}
	if update.LastMaintenanceAt != nil {
		ts := update.LastMaintenanceAt.Unix()
		if update.LastMaintenanceAt.IsZero() {
			ts = 0
		}
		ov.LastMaintenanceAt = &ts
	}

	b.overrides[id] = ov

	// Rebuild indexes: remove old author/tag/publisher entries for this book
	for name, ids := range b.authors {
		b.authors[name] = removeID(ids, id)
	}
	for tag, ids := range b.tags {
		b.tags[tag] = removeID(ids, id)
	}
	for pub, ids := range b.publishers {
		b.publishers[pub] = removeID(ids, id)
	}

	updated := b.applyOverride(*bk)
	*bk = updated

	for _, a := range bk.Authors {
		b.authors[a.Name] = append(b.authors[a.Name], bk.ID)
	}
	for _, t := range bk.Tags {
		b.tags[t] = append(b.tags[t], bk.ID)
	}
	if bk.Publisher != "" {
		b.publishers[bk.Publisher] = append(b.publishers[bk.Publisher], bk.ID)
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

// DeleteTag removes the given tag from all books that have it.
// It implements catalog.TagDeleter.
func (b *Backend) DeleteTag(tag string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	bookIDs := b.tags[tag]
	for _, id := range bookIDs {
		bk, ok := b.byID[id]
		if !ok {
			continue
		}
		// Build new tag list without the deleted tag.
		newTags := make([]string, 0, len(bk.Tags))
		for _, t := range bk.Tags {
			if t != tag {
				newTags = append(newTags, t)
			}
		}
		bk.Tags = newTags

		// Persist: set override tags so the removal survives a Refresh.
		ov := b.overrides[id]
		ov.Tags = newTags
		b.overrides[id] = ov
	}

	delete(b.tags, tag)

	return b.saveOverrides()
}

// CoverPath returns the filesystem path to the cached cover image for a book ID.
func (b *Backend) CoverPath(id string) (string, error) {
	return epub.CoverPath(b.coversDir, id)
}

// UpdateCover replaces the cover image for the given book ID with the data
// from src. It removes any previously cached cover image files for that ID
// and updates the in-memory book record's CoverURL/ThumbnailURL fields.
// It implements catalog.CoverUpdater.
func (b *Backend) UpdateCover(id string, src io.ReadCloser, ext string) error {
	defer src.Close()

	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.byID[id]; !ok {
		return fmt.Errorf("book %q not found", id)
	}

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

	// Append a version parameter so that browsers/service-workers treat the
	// updated cover as a different resource and don't serve a stale cached copy.
	coverURL := fmt.Sprintf("/covers/%s?v=%d", id, time.Now().UnixMilli())

	// Update in-memory record so subsequent API responses reflect the new cover.
	bk := b.byID[id]
	bk.CoverURL = coverURL
	bk.ThumbnailURL = coverURL
	// Mirror into the main slice (byID points into books slice, but update to be safe).
	for i := range b.books {
		if b.books[i].ID == id {
			b.books[i].CoverURL = coverURL
			b.books[i].ThumbnailURL = coverURL
			break
		}
	}
	return nil
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
	publishers := make(map[string][]string)

	for i := range books {
		bk := &books[i]
		byID[bk.ID] = bk
		for _, a := range bk.Authors {
			authors[a.Name] = append(authors[a.Name], bk.ID)
		}
		for _, t := range bk.Tags {
			tags[t] = append(tags[t], bk.ID)
		}
		if bk.Publisher != "" {
			publishers[bk.Publisher] = append(publishers[bk.Publisher], bk.ID)
		}
	}

	b.mu.Lock()
	b.books = books
	b.byID = byID
	b.authors = authors
	b.tags = tags
	b.publishers = publishers
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

	// Pre-compute series sizes when a series-size filter is requested.
	var seriesSizes map[string]int
	if q.SeriesSize != "" && q.SeriesSize != "standalone" {
		seriesSizes = map[string]int{}
		for _, bk := range b.books {
			if bk.Series != "" {
				seriesSizes[bk.Series]++
			}
		}
	}

	var matched []catalog.Book
	for _, bk := range b.books {
		if q.UnreadOnly && bk.IsRead {
			continue
		}
		if q.Series != "" && bk.Series != q.Series {
			continue
		}
		if q.SeriesSize != "" {
			if !matchSeriesSize(q.SeriesSize, bk.Series, seriesSizes) {
				continue
			}
		}
		if q.Author != "" {
			authorMatch := false
			for _, a := range bk.Authors {
				if strings.EqualFold(a.Name, q.Author) {
					authorMatch = true
					break
				}
			}
			if !authorMatch {
				continue
			}
		}
		if q.Tag != "" {
			tagMatch := false
			for _, t := range bk.Tags {
				if strings.EqualFold(t, q.Tag) {
					tagMatch = true
					break
				}
			}
			if !tagMatch {
				continue
			}
		}
		if q.Publisher != "" && !strings.EqualFold(bk.Publisher, q.Publisher) {
			continue
		}
		if q.Collection != "" && !strings.EqualFold(bk.Collection, q.Collection) {
			continue
		}
		if q.MaxAgeRating > 0 && bk.AgeRating > q.MaxAgeRating {
			continue
		}
		if len(q.AgeRatings) > 0 {
			dbVal := bk.AgeRating
			matched := false
			for _, v := range q.AgeRatings {
				want := v
				if v == -1 {
					want = 0
				}
				if dbVal == want {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		if q.SpiceMax != nil && bk.AgeRating >= 16 {
			v := *q.SpiceMax
			if v < 0 {
				v = 0
			}
			if v > 5 {
				v = 5
			}
			if bk.SpiceRating > v {
				continue
			}
		}
		if q.NotIndexed && !bk.LastMaintenanceAt.IsZero() {
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
		if bk.Series != "" && strings.Contains(strings.ToLower(bk.Series), qLower) {
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

	// Apply sort.
	seriesIndexFloat := func(idx string) float64 {
		f, err := strconv.ParseFloat(strings.TrimSpace(idx), 64)
		if err != nil {
			return 0
		}
		return f
	}

	switch q.SortBy {
	case "series_index":
		sort.Slice(matched, func(i, j int) bool {
			fi := seriesIndexFloat(matched[i].SeriesIndex)
			fj := seriesIndexFloat(matched[j].SeriesIndex)
			if fi != fj {
				return fi < fj
			}
			return strings.ToLower(matched[i].Title) < strings.ToLower(matched[j].Title)
		})
	case "title":
		if q.SortOrder == "asc" {
			sort.Slice(matched, func(i, j int) bool {
				return strings.ToLower(matched[i].Title) < strings.ToLower(matched[j].Title)
			})
		} else {
			sort.Slice(matched, func(i, j int) bool {
				return strings.ToLower(matched[i].Title) > strings.ToLower(matched[j].Title)
			})
		}
	case "added":
		if q.SortOrder == "asc" {
			sort.Slice(matched, func(i, j int) bool {
				return matched[i].AddedAt.Before(matched[j].AddedAt)
			})
		}
		// desc is already natural order from b.books
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

// Publishers returns all distinct non-empty publisher names sorted alphabetically with pagination.
func (b *Backend) Publishers(offset, limit int) ([]string, int, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	pubList := make([]string, 0, len(b.publishers))
	for p := range b.publishers {
		pubList = append(pubList, p)
	}
	sort.Strings(pubList)

	total := len(pubList)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total || limit == 0 {
		end = total
	}
	return pubList[offset:end], total, nil
}

// BooksByPublisher returns books by a specific publisher with pagination.
func (b *Backend) BooksByPublisher(publisher string, offset, limit int) ([]catalog.Book, int, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	ids := b.publishers[publisher]
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

// matchSeriesSize reports whether a book whose series field is `series` matches
// the supplied SeriesSize filter value.  Counts is the pre-computed map of
// series name → number of books in that series (nil for "standalone" which
// does not need counts).
func matchSeriesSize(size, series string, counts map[string]int) bool {
	switch size {
	case "standalone":
		return series == ""
	case "short":
		if series == "" {
			return false
		}
		c := counts[series]
		return c >= 2 && c <= 3
	case "medium":
		if series == "" {
			return false
		}
		c := counts[series]
		return c >= 4 && c <= 7
	case "long":
		if series == "" {
			return false
		}
		return counts[series] >= 8
	}
	return true
}

// Series returns all distinct non-empty series names sorted alphabetically
// with the number of books in each. It implements catalog.SeriesLister.
func (b *Backend) Series() ([]catalog.SeriesEntry, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	counts := make(map[string]int)
	for _, bk := range b.books {
		if bk.Series != "" {
			counts[bk.Series]++
		}
	}
	entries := make([]catalog.SeriesEntry, 0, len(counts))
	for name, count := range counts {
		entries = append(entries, catalog.SeriesEntry{Name: name, Count: count})
	}
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, nil
}

// Collections returns all distinct non-empty editorial collection names sorted
// alphabetically. It implements catalog.CollectionLister.
func (b *Backend) Collections() ([]string, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	seen := make(map[string]struct{})
	for _, bk := range b.books {
		if bk.Collection != "" {
			seen[bk.Collection] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
	return names, nil
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
	for pub, ids := range b.publishers {
		b.publishers[pub] = removeID(ids, id)
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
	// Rebuild byID across the entire slice: appending re-allocates the
	// underlying array, so any pointers previously stored in b.byID for older
	// books are now stale and would silently mutate orphaned copies on update.
	for i := range b.books {
		b.byID[b.books[i].ID] = &b.books[i]
	}
	bk := &b.books[0]
	for _, a := range bk.Authors {
		b.authors[a.Name] = append(b.authors[a.Name], bk.ID)
	}
	for _, t := range bk.Tags {
		b.tags[t] = append(b.tags[t], bk.ID)
	}
	if bk.Publisher != "" {
		b.publishers[bk.Publisher] = append(b.publishers[bk.Publisher], bk.ID)
	}
	b.mu.Unlock()

	return bk, nil
}

// librarianFile is the on-disk shape of the librarian association singleton.
// Timestamps are stored as Unix seconds so the file is portable and easy to
// inspect with `cat`.  Zero values are treated as "absent".
type librarianFile struct {
	LibrarianURL      string `json:"librarianUrl"`
	LibrarianInstance string `json:"librarianInstance"`
	ChatSecret        string `json:"chatSecret"`
	WebhookSecret     string `json:"webhookSecret"`
	CreatedAt         int64  `json:"createdAt"`
	UpdatedAt         int64  `json:"updatedAt"`
	LastSeenAt        int64  `json:"lastSeenAt,omitempty"`
}

// Get returns the current librarian association, or (nil, nil) when the file
// does not exist yet.  Implements catalog.LibrarianAssociation.
func (b *Backend) Get() (*catalog.LibrarianAssociationData, error) {
	data, err := os.ReadFile(b.librarianPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read librarian association: %w", err)
	}
	var f librarianFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse librarian association: %w", err)
	}
	// A file that exists but only contains zero values means "cleared" — keep
	// returning the record so callers can detect a partial state, but allow an
	// empty URL to round-trip cleanly.
	var lastSeen time.Time
	if f.LastSeenAt > 0 {
		lastSeen = time.Unix(f.LastSeenAt, 0)
	}
	return &catalog.LibrarianAssociationData{
		LibrarianURL:      f.LibrarianURL,
		LibrarianInstance: f.LibrarianInstance,
		ChatSecret:        f.ChatSecret,
		WebhookSecret:     f.WebhookSecret,
		CreatedAt:         time.Unix(f.CreatedAt, 0),
		UpdatedAt:         time.Unix(f.UpdatedAt, 0),
		LastSeenAt:        lastSeen,
	}, nil
}

// Set upserts the librarian association.  CreatedAt is preserved from the
// existing file when present; UpdatedAt is always stamped to time.Now().
// Implements catalog.LibrarianAssociation.
func (b *Backend) Set(data catalog.LibrarianAssociationData) error {
	now := time.Now().Unix()
	createdAt := now
	var lastSeenAt int64
	// Preserve the original creation timestamp and last_seen_at on update —
	// mutations (rotate/announce) must not reset the heartbeat clock.
	if existing, err := b.Get(); err == nil && existing != nil {
		if !existing.CreatedAt.IsZero() && existing.CreatedAt.Unix() > 0 {
			createdAt = existing.CreatedAt.Unix()
		}
		if !existing.LastSeenAt.IsZero() && existing.LastSeenAt.Unix() > 0 {
			lastSeenAt = existing.LastSeenAt.Unix()
		}
	} else if !data.CreatedAt.IsZero() && data.CreatedAt.Unix() > 0 {
		createdAt = data.CreatedAt.Unix()
	}
	f := librarianFile{
		LibrarianURL:      data.LibrarianURL,
		LibrarianInstance: data.LibrarianInstance,
		ChatSecret:        data.ChatSecret,
		WebhookSecret:     data.WebhookSecret,
		CreatedAt:         createdAt,
		UpdatedAt:         now,
		LastSeenAt:        lastSeenAt,
	}
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal librarian association: %w", err)
	}
	// Secrets live in this file; restrict perms to 0600.
	if err := os.WriteFile(b.librarianPath, out, 0600); err != nil {
		return fmt.Errorf("write librarian association: %w", err)
	}
	return nil
}

// Clear deletes the librarian association file.  Idempotent — a missing file
// is not an error.  Implements catalog.LibrarianAssociation.
func (b *Backend) Clear() error {
	if err := os.Remove(b.librarianPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove librarian association: %w", err)
	}
	return nil
}

// Touch updates the LastSeenAt field on the existing association without
// advancing UpdatedAt (a heartbeat is not a mutation).  Returns nil and
// does nothing when no association file exists.  Implements
// catalog.LibrarianAssociation.
func (b *Backend) Touch(at time.Time) error {
	data, err := os.ReadFile(b.librarianPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read librarian association: %w", err)
	}
	var f librarianFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("parse librarian association: %w", err)
	}
	f.LastSeenAt = at.Unix()
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal librarian association: %w", err)
	}
	if err := os.WriteFile(b.librarianPath, out, 0600); err != nil {
		return fmt.Errorf("write librarian association: %w", err)
	}
	return nil
}
