// Package fs implements a filesystem-based catalog backend for nxt-opds.
// It scans a directory recursively for EPUB and PDF files and builds an
// in-memory catalog by extracting metadata from each file.
package fs

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/banux/nxt-opds/internal/catalog"
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
// Safe to call with the lock held or not – it only modifies b.overrides before the lock is set up.
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
// Must be called with b.mu held (at least read-locked for reading overrides).
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
// Returns the merged book (bk is not modified).
func (b *Backend) applyOverride(bk catalog.Book) catalog.Book {
	ov, ok := b.overrides[bk.ID]
	if !ok {
		return bk
	}
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

	// Load existing override for this book (or start fresh)
	ov := b.overrides[id]

	// Merge update fields into override
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

	// Apply override to the in-memory book
	updated := b.applyOverride(*bk)
	*bk = updated

	// Re-add to author/tag indexes with updated values
	for _, a := range bk.Authors {
		b.authors[a.Name] = append(b.authors[a.Name], bk.ID)
	}
	for _, t := range bk.Tags {
		b.tags[t] = append(b.tags[t], bk.ID)
	}

	bk.UpdatedAt = time.Now()

	// Persist overrides
	if err := b.saveOverrides(); err != nil {
		// Log but don't fail – metadata is updated in-memory
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
// Returns an error if no cover exists for that ID.
func (b *Backend) CoverPath(id string) (string, error) {
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp"} {
		p := filepath.Join(b.coversDir, id+ext)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no cover for book %q", id)
}

// Refresh re-scans the root directory and rebuilds the in-memory catalog.
func (b *Backend) Refresh() error {
	var books []catalog.Book

	err := filepath.WalkDir(b.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".epub":
			book, err := bookFromEPUB(path, b.coversDir)
			if err != nil {
				// Skip files that can't be parsed, but don't abort the scan
				return nil
			}
			books = append(books, book)
		case ".pdf":
			book := bookFromPath(path)
			books = append(books, book)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("scanning directory %q: %w", b.root, err)
	}

	// Apply any persisted metadata overrides
	b.mu.RLock()
	overrides := b.overrides
	b.mu.RUnlock()
	for i := range books {
		books[i] = applyOverrideFrom(books[i], overrides)
	}

	// Rebuild indexes
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

// applyOverrideFrom is the package-level equivalent of Backend.applyOverride, used
// before the Backend lock is set up (e.g. during Refresh).
func applyOverrideFrom(bk catalog.Book, overrides map[string]metaOverride) catalog.Book {
	ov, ok := overrides[bk.ID]
	if !ok {
		return bk
	}
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
	if ov.IsRead != nil {
		bk.IsRead = *ov.IsRead
	}
	return bk
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
func (b *Backend) Search(q catalog.SearchQuery) ([]catalog.Book, int, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	qLower := strings.ToLower(q.Query)
	var matched []catalog.Book
	for _, bk := range b.books {
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

// StoreBook writes src to the backend's root directory as filename, then
// parses and indexes it immediately. It implements catalog.Uploader.
// src is closed after reading regardless of success.
func (b *Backend) StoreBook(filename string, src io.ReadCloser) (*catalog.Book, error) {
	defer src.Close()

	// Sanitize filename: strip path components, allow only safe extensions
	filename = filepath.Base(filename)
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".epub", ".pdf":
		// allowed
	default:
		return nil, fmt.Errorf("unsupported file type %q (only .epub and .pdf are accepted)", ext)
	}

	destPath := filepath.Join(b.root, filename)

	// Refuse to overwrite an existing file
	if _, err := os.Stat(destPath); err == nil {
		return nil, fmt.Errorf("file %q already exists in the catalog", filename)
	}

	// Write to a temp file first, then rename (atomic on most filesystems)
	tmp, err := os.CreateTemp(b.root, ".upload-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // clean up temp on failure

	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("write upload: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close temp file: %w", err)
	}

	// Atomic rename to final destination
	if err := os.Rename(tmpPath, destPath); err != nil {
		return nil, fmt.Errorf("rename upload: %w", err)
	}

	// Parse and index the new file
	var book catalog.Book
	switch ext {
	case ".epub":
		book, err = bookFromEPUB(destPath, b.coversDir)
		if err != nil {
			// File was saved but couldn't be parsed; still keep the file, return error
			return nil, fmt.Errorf("parse epub %q: %w", filename, err)
		}
	case ".pdf":
		book = bookFromPath(destPath)
	}

	// Add to indexes under write lock
	b.mu.Lock()
	book = applyOverrideFrom(book, b.overrides)
	b.books = append(b.books, book)
	bk := &b.books[len(b.books)-1]
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

// --- EPUB metadata extraction ---

// opfPackage mirrors the OPF package element we care about.
type opfPackage struct {
	Metadata opfMetadata `xml:"metadata"`
	Manifest opfManifest `xml:"manifest"`
}

type opfMetadata struct {
	Titles      []string    `xml:"title"`
	Creators    []opfAuthor `xml:"creator"`
	Subjects    []string    `xml:"subject"`
	Description string      `xml:"description"`
	Language    string      `xml:"language"`
	Publisher   string      `xml:"publisher"`
	Date        string      `xml:"date"`
	// EPUB2 cover reference: <meta name="cover" content="cover-image-id"/>
	Metas []opfMeta `xml:"meta"`
}

type opfAuthor struct {
	Name string `xml:",chardata"`
	Role string `xml:"role,attr"`
}

type opfMeta struct {
	Name    string `xml:"name,attr"`
	Content string `xml:"content,attr"`
}

type opfManifest struct {
	Items []opfItem `xml:"item"`
}

type opfItem struct {
	ID         string `xml:"id,attr"`
	Href       string `xml:"href,attr"`
	MediaType  string `xml:"media-type,attr"`
	Properties string `xml:"properties,attr"`
}

// containerXML is used to locate the OPF file inside the EPUB.
type containerXML struct {
	Rootfile struct {
		FullPath string `xml:"full-path,attr"`
	} `xml:"rootfiles>rootfile"`
}

// bookFromEPUB opens an EPUB file, extracts OPF metadata and cover image, and returns a Book.
// coversDir is the directory where extracted cover images are cached.
func bookFromEPUB(path, coversDir string) (catalog.Book, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return catalog.Book{}, fmt.Errorf("open epub %q: %w", path, err)
	}
	defer zr.Close()

	// Step 1: read META-INF/container.xml to find the OPF path
	opfPath, err := readContainerXML(&zr.Reader)
	if err != nil {
		return catalog.Book{}, fmt.Errorf("epub container %q: %w", path, err)
	}

	// Step 2: parse the OPF file (metadata + manifest)
	pkg, err := readOPFPackage(&zr.Reader, opfPath)
	if err != nil {
		return catalog.Book{}, fmt.Errorf("epub opf %q: %w", path, err)
	}
	meta := pkg.Metadata

	info, _ := os.Stat(path)
	size := int64(0)
	if info != nil {
		size = info.Size()
	}

	id := pathToID(path)

	book := catalog.Book{
		ID:        id,
		Title:     firstOrFilename(meta.Titles, path),
		Summary:   meta.Description,
		Language:  meta.Language,
		Publisher: meta.Publisher,
		UpdatedAt: time.Now(),
		Tags:      meta.Subjects,
		Files: []catalog.File{
			{MIMEType: "application/epub+zip", Path: path, Size: size},
		},
	}

	for _, c := range meta.Creators {
		book.Authors = append(book.Authors, catalog.Author{Name: c.Name})
	}

	if meta.Date != "" {
		if t, err := time.Parse("2006-01-02", meta.Date[:min(10, len(meta.Date))]); err == nil {
			book.PublishedAt = t
		}
	}

	// Step 3: extract cover image (best-effort; failure doesn't prevent indexing)
	if coverPath := extractCoverFromPkg(&zr.Reader, opfPath, pkg, id, coversDir); coverPath != "" {
		book.CoverURL = "/covers/" + id
		book.ThumbnailURL = "/covers/" + id
	}

	return book, nil
}

// bookFromPath creates a minimal Book entry for a non-EPUB file (e.g. PDF).
func bookFromPath(path string) catalog.Book {
	info, _ := os.Stat(path)
	size := int64(0)
	if info != nil {
		size = info.Size()
	}

	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	mime := "application/pdf"
	if strings.ToLower(filepath.Ext(path)) != ".pdf" {
		mime = "application/octet-stream"
	}

	return catalog.Book{
		ID:    pathToID(path),
		Title: name,
		Files: []catalog.File{
			{MIMEType: mime, Path: path, Size: size},
		},
		UpdatedAt: time.Now(),
	}
}

// readContainerXML reads META-INF/container.xml and returns the OPF file path.
func readContainerXML(zr *zip.Reader) (string, error) {
	for _, f := range zr.File {
		if f.Name == "META-INF/container.xml" {
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			defer rc.Close()

			var c containerXML
			if err := xml.NewDecoder(rc).Decode(&c); err != nil {
				return "", err
			}
			if c.Rootfile.FullPath == "" {
				return "", fmt.Errorf("no rootfile found in container.xml")
			}
			return c.Rootfile.FullPath, nil
		}
	}
	return "", fmt.Errorf("META-INF/container.xml not found")
}

// readOPFPackage opens and parses the OPF file at opfPath inside the ZIP.
func readOPFPackage(zr *zip.Reader, opfPath string) (opfPackage, error) {
	for _, f := range zr.File {
		if f.Name == opfPath {
			rc, err := f.Open()
			if err != nil {
				return opfPackage{}, err
			}
			defer rc.Close()

			data, err := io.ReadAll(rc)
			if err != nil {
				return opfPackage{}, err
			}

			var pkg opfPackage
			if err := xml.Unmarshal(data, &pkg); err != nil {
				return opfPackage{}, err
			}
			return pkg, nil
		}
	}
	return opfPackage{}, fmt.Errorf("OPF file %q not found in epub", opfPath)
}

// extractCoverFromPkg tries to find and cache a cover image from an EPUB ZIP.
// pkg is the already-parsed OPF package. Returns the filesystem path of the
// saved cover, or "" if none was found or extraction failed.
func extractCoverFromPkg(zr *zip.Reader, opfPath string, pkg opfPackage, bookID, coversDir string) string {
	opfDir := filepath.ToSlash(filepath.Dir(opfPath))
	if opfDir == "." {
		opfDir = ""
	}

	// Find cover item: EPUB3 uses properties="cover-image"; EPUB2 uses <meta name="cover" content="id">
	coverItemID := ""
	for _, m := range pkg.Metadata.Metas {
		if strings.EqualFold(m.Name, "cover") && m.Content != "" {
			coverItemID = m.Content
			break
		}
	}

	var coverHref, coverMIME string
	for _, item := range pkg.Manifest.Items {
		if strings.Contains(item.Properties, "cover-image") {
			coverHref = item.Href
			coverMIME = item.MediaType
			break
		}
		if coverItemID != "" && item.ID == coverItemID {
			coverHref = item.Href
			coverMIME = item.MediaType
			// Don't break; prefer the properties="cover-image" item if found later
		}
	}

	if coverHref == "" {
		return ""
	}

	// Resolve relative href against OPF directory
	var fullHref string
	if opfDir != "" {
		fullHref = opfDir + "/" + coverHref
	} else {
		fullHref = coverHref
	}

	// Find the cover file in the ZIP
	var coverFile *zip.File
	for _, f := range zr.File {
		if f.Name == fullHref {
			coverFile = f
			break
		}
	}
	if coverFile == nil {
		return ""
	}

	// Determine file extension from MIME type
	ext := mimeToExt(coverMIME)
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(coverHref))
	}
	if ext == "" {
		ext = ".jpg"
	}

	destPath := filepath.Join(coversDir, bookID+ext)

	// Skip extraction if already cached
	if _, err := os.Stat(destPath); err == nil {
		return destPath
	}

	rc, err := coverFile.Open()
	if err != nil {
		return ""
	}
	defer rc.Close()

	out, err := os.Create(destPath)
	if err != nil {
		return ""
	}
	defer out.Close()

	if _, err := io.Copy(out, rc); err != nil {
		_ = os.Remove(destPath)
		return ""
	}
	return destPath
}

// mimeToExt maps common image MIME types to file extensions.
func mimeToExt(mimeType string) string {
	switch strings.ToLower(mimeType) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	default:
		return ""
	}
}

// pathToID generates a stable string ID from a file path using a short SHA-256 hash.
func pathToID(path string) string {
	sum := sha256.Sum256([]byte(path))
	return fmt.Sprintf("%x", sum[:8])
}

// firstOrFilename returns the first string in a slice, or the base filename without extension.
func firstOrFilename(vals []string, path string) string {
	if len(vals) > 0 && vals[0] != "" {
		return vals[0]
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

