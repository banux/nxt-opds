// Package catalog provides the book catalog abstraction for nxt-opds.
// It defines the core data types and the Catalog interface that backends implement.
package catalog

import (
	"io"
	"time"
)

// Book represents a publication in the catalog.
type Book struct {
	// ID is a unique identifier for this book (e.g. UUID or file path hash).
	ID string

	// Title is the display title of the publication.
	Title string

	// Authors is the list of authors.
	Authors []Author

	// Summary is a short description of the publication.
	Summary string

	// Language is the BCP 47 language tag (e.g. "en", "fr").
	Language string

	// Publisher is the publisher name.
	Publisher string

	// PublishedAt is the original publication date.
	PublishedAt time.Time

	// UpdatedAt is when this catalog entry was last updated.
	UpdatedAt time.Time

	// Tags are genre/subject tags.
	Tags []string

	// Files lists the available acquisition files for this book.
	Files []File

	// CoverURL is the URL path to the cover image (if available).
	CoverURL string

	// ThumbnailURL is the URL path to the thumbnail image (if available).
	ThumbnailURL string

	// Series is the series name this book belongs to (optional).
	Series string

	// SeriesIndex is the position within the series (e.g. "1", "2.5").
	SeriesIndex string

	// SeriesTotal is the total number of books in the series (e.g. "5").
	SeriesTotal string

	// IsRead indicates the user has marked this book as read.
	IsRead bool

	// Rating is the user's star rating (0 = not rated, 1–5 stars).
	Rating int

	// AddedAt is when this book was first added to the catalog.
	AddedAt time.Time
}

// Author represents a publication author.
type Author struct {
	Name string
	URI  string
}

// File represents a downloadable file associated with a book.
type File struct {
	// MIMEType is the media type (e.g. "application/epub+zip").
	MIMEType string

	// Path is the file system path to the file.
	Path string

	// Size is the file size in bytes (0 if unknown).
	Size int64
}

// SearchQuery carries parameters for catalog search.
type SearchQuery struct {
	// Query is the full-text search term.
	Query string

	// Author filters by author name substring.
	Author string

	// Tag filters by a specific tag/genre.
	Tag string

	// Language filters by BCP 47 language tag.
	Language string

	// UnreadOnly restricts results to books not yet marked as read.
	UnreadOnly bool

	// Series filters by exact series name (empty = no filter).
	Series string

	// SortBy is the sort field: "" or "added" for added date, "title" for alphabetical,
	// "series_index" for numeric series position.
	SortBy string

	// SortOrder is the sort direction: "" or "desc" for descending, "asc" for ascending.
	SortOrder string

	// Offset is the pagination offset (0-based).
	Offset int

	// Limit is the maximum number of results to return (0 = server default).
	Limit int
}

// Catalog is the interface that backend implementations must satisfy.
// A Catalog provides read-only access to the book collection.
type Catalog interface {
	// Root returns the top-level navigation entries (e.g. "By Author", "By Title").
	Root() ([]NavEntry, error)

	// AllBooks returns all books, optionally paginated.
	AllBooks(offset, limit int) ([]Book, int, error)

	// BookByID returns a single book by its unique ID.
	BookByID(id string) (*Book, error)

	// Search performs a full-text/filtered search and returns matching books.
	Search(q SearchQuery) ([]Book, int, error)

	// BooksByAuthor returns books filtered by author name.
	BooksByAuthor(author string, offset, limit int) ([]Book, int, error)

	// BooksByTag returns books filtered by tag/genre.
	BooksByTag(tag string, offset, limit int) ([]Book, int, error)

	// Authors returns all distinct authors.
	Authors(offset, limit int) ([]string, int, error)

	// Tags returns all distinct tags/genres.
	Tags(offset, limit int) ([]string, int, error)
}

// NavEntry is a navigation item pointing to a sub-feed.
type NavEntry struct {
	ID      string
	Title   string
	Content string
	Href    string
	Rel     string
}

// Uploader is an optional interface that catalog backends may implement
// to support adding books via file upload.
type Uploader interface {
	// StoreBook saves src as filename inside the catalog's root directory,
	// indexes it immediately, and returns the resulting Book entry.
	// src is consumed and closed by the implementation.
	StoreBook(filename string, src io.ReadCloser) (*Book, error)
}

// CoverProvider is an optional interface that catalog backends may implement
// to serve cached cover images by book ID.
type CoverProvider interface {
	// CoverPath returns the filesystem path to the cached cover image for the
	// given book ID. Returns an error if no cover exists for that ID.
	CoverPath(id string) (string, error)
}

// BookUpdate carries the editable fields for a book metadata update.
// Nil pointer fields are left unchanged; non-nil fields replace the current value.
// Nil slice fields are left unchanged; non-nil (including empty) slices replace the current value.
type BookUpdate struct {
	Title       *string
	Authors     []string // nil = unchanged, empty = clear
	Tags        []string // nil = unchanged, empty = clear
	Summary     *string
	Publisher   *string
	Language    *string
	Series      *string
	SeriesIndex *string
	SeriesTotal *string
	IsRead      *bool
	Rating      *int
}

// Updater is an optional interface for catalog backends that support book metadata editing.
type Updater interface {
	// UpdateBook applies the given update to the book with the given ID and returns
	// the updated Book. Returns an error if the book is not found or the update fails.
	UpdateBook(id string, update BookUpdate) (*Book, error)
}

// Refresher is an optional interface for catalog backends that support
// rescanning the books directory to pick up files added or removed externally.
type Refresher interface {
	// Refresh rescans the underlying store and updates the in-memory or
	// database index to reflect the current state of the books directory.
	Refresh() error
}

// SeriesEntry holds a series name and the number of books in it.
type SeriesEntry struct {
	Name  string
	Count int
}

// SeriesLister is an optional interface for catalog backends that support
// listing all distinct series with book counts.
type SeriesLister interface {
	// Series returns all distinct non-empty series names sorted alphabetically,
	// each paired with the number of books belonging to that series.
	Series() ([]SeriesEntry, error)
}

// Deleter is an optional interface for catalog backends that support deleting
// a book and its associated files from the catalog.
type Deleter interface {
	// DeleteBook removes the book with the given ID from the catalog and
	// deletes its file(s) and cover image from disk.
	DeleteBook(id string) error
}

// CoverUpdater is an optional interface for catalog backends that support
// replacing a book's cover image with a user-supplied image.
type CoverUpdater interface {
	// UpdateCover replaces the cover image for the book with the given ID.
	// src is the image data (consumed and closed by the implementation).
	// ext is the file extension including the dot (e.g. ".jpg", ".png").
	UpdateCover(id string, src io.ReadCloser, ext string) error
}

// Backupper is an optional interface for catalog backends that support
// creating a consistent point-in-time backup of their persistent store.
type Backupper interface {
	// Backup writes a self-contained backup file named
	// "catalog-YYYYMMDD-HHMMSS.db" into destDir and then prunes the
	// oldest files in destDir so that at most keep backups are retained
	// (keep ≤ 0 means unlimited).
	// Returns the path of the newly created backup file.
	Backup(destDir string, keep int) (string, error)
}
