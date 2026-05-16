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

	// Collection is the editorial collection name (e.g. "Folio SF", "J'ai Lu").
	// This is distinct from Series: a collection groups books by publisher brand,
	// not by narrative order (corresponds to EPUB3 belongs-to-collection with type="set").
	Collection string

	// CollectionIndex is the position within the editorial collection (e.g. "42").
	// Corresponds to the EPUB3 group-position refine on belongs-to-collection.
	CollectionIndex string

	// IsRead indicates the user has marked this book as read.
	IsRead bool

	// Rating is the user's star rating (0 = not rated, 1–5 stars).
	Rating int

	// AddedAt is when this book was first added to the catalog.
	AddedAt time.Time

	// AgeRating is the minimum recommended age for this book.
	// 0 = non classifié, 3 = petite enfance, 6 = enfants, 10 = jeunesse,
	// 12 = adolescent, 18 = adulte.
	AgeRating int

	// SpiceRating grades the intensity of sexual content on a 0-5 scale.
	// Only meaningful for AgeRating >= 16. 0 = none/unrated, 1 = suggestive,
	// 2 = sensual romance, 3 = occasional explicit scenes, 4 = recurrent
	// explicit, 5 = very explicit / erotica focus.
	SpiceRating int

	// LastMaintenanceAt is when this book's metadata was last extracted/indexed.
	LastMaintenanceAt time.Time
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

	// Publisher filters by exact publisher name.
	Publisher string

	// Collection filters by exact editorial collection name.
	Collection string

	// Language filters by BCP 47 language tag.
	Language string

	// MaxAgeRating restricts results to books with AgeRating <= MaxAgeRating.
	// 0 means no age filter is applied.
	MaxAgeRating int

	// AgeRatings filters by age classification (multiple selection).
	// -1 means unclassified books (age_rating = 0 in DB).
	// Positive values match exactly (e.g. [3, 6] shows only 3+ and 6+ books).
	// Empty slice means no filter is applied.
	AgeRatings []int

	// SpiceMax, when non-nil, restricts results to books whose SpiceRating
	// <= *SpiceMax.  Books with age_rating < 16 are unaffected (their spice
	// is irrelevant by definition).  Valid pointed-to values: 0 to 5.
	SpiceMax *int

	// NotIndexed restricts results to books that have never been processed
	// (LastMaintenanceAt is zero / last_maintenance_at = 0).
	NotIndexed bool

	// UnreadOnly restricts results to books not yet marked as read.
	// When UserID is non-empty, "unread" is per-user; otherwise it uses the
	// global is_read flag.
	UnreadOnly bool

	// UserID is the current user's ID for per-user read-status filtering.
	// When empty, the global is_read column is used for UnreadOnly queries.
	UserID string

	// Series filters by exact series name (empty = no filter).
	Series string

	// SeriesSize filters books by the size of the series they belong to.
	// Values:
	//   ""           — no filter
	//   "standalone" — books not in a series (Series field empty)
	//   "short"      — part of a series with 2 or 3 books
	//   "medium"     — part of a series with 4 to 7 books
	//   "long"       — part of a series with 8 or more books
	SeriesSize string

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

// User represents a library user with a personalised colour for read-status display.
type User struct {
	// ID is a UUID-style unique identifier.
	ID string

	// Name is the display name chosen by the user.
	Name string

	// Color is a CSS hex colour code (e.g. "#3B82F6") used to colour the
	// "read" indicator for this user's books.
	Color string

	// IsAdmin indicates whether this user has administrative privileges.
	IsAdmin bool

	// IsChild indicates this is a child profile (age-restricted view).
	// When true, the server will automatically apply a MaxAgeRating filter
	// to hide adult/teen content from this user.
	IsChild bool

	// MaxAge is the maximum AgeRating shown to this user when IsChild is true.
	// Allowed values mirror the AgeRating field: 3, 6, 10, 12, 16.
	// A value of 0 is treated as 10 (default child restriction).
	MaxAge int

	// Token is a per-user authentication token used to access OPDS feeds
	// and the MCP endpoint as that specific user (so per-user views like
	// recommendations, the to-read pile and unread filter work).
	Token string
}

// UserManager is an optional interface for catalog backends that support
// multi-user management.  Users share a single password but each has a
// distinct name and colour.
type UserManager interface {
	// Users returns all registered users sorted by name.
	Users() ([]User, error)

	// UserByID returns the user with the given ID, or an error if not found.
	UserByID(id string) (*User, error)

	// CreateUser creates a new user with the given name and hex colour.
	// If isAdmin is true the user has administrative privileges.
	// If isChild is true the user is a child profile (age-restricted).
	// maxAge is the maximum AgeRating for child profiles (0 = use default 10).
	// Returns the newly created User.
	CreateUser(name, color string, isAdmin, isChild bool, maxAge int) (*User, error)

	// DeleteUser removes the user with the given ID.
	DeleteUser(id string) error

	// UpdateUser updates the name, color, admin, child status and max age of an existing user.
	// maxAge is the maximum AgeRating for child profiles (0 = use default 10).
	UpdateUser(id, name, color string, isAdmin, isChild bool, maxAge int) (*User, error)

	// UserByToken returns the user that owns the given per-user token.
	// Returns an error if no user matches.
	UserByToken(token string) (*User, error)

	// RegenerateUserToken assigns a fresh token to the user with the given ID
	// and returns the updated User (with the new Token populated).
	RegenerateUserToken(id string) (*User, error)
}

// UserReadManager is an optional interface for catalog backends that support
// per-user read-status tracking.
type UserReadManager interface {
	// SetUserRead marks (isRead=true) or clears (isRead=false) a book as
	// read for the specified user.
	SetUserRead(userID, bookID string, isRead bool) error

	// UserReadStatuses returns a map of bookID → isRead for the given user
	// and the supplied list of book IDs.  Missing keys mean "not read".
	UserReadStatuses(userID string, bookIDs []string) (map[string]bool, error)

	// BookReadColors returns for each supplied bookID the hex colour codes
	// of all users who have marked that book as read.
	// Missing keys mean "no one has read it yet".
	BookReadColors(bookIDs []string) (map[string][]string, error)
}

// LabelCount is a generic "label with associated count" row used for stats aggregates.
type LabelCount struct {
	Label string
	Count int
}

// MonthCount is a "year-month with count" row used for time-series stats.
type MonthCount struct {
	// Month is the ISO-style year-month key in YYYY-MM form (e.g. "2026-04").
	Month string
	Count int
}

// ReadStats aggregates reading statistics for a single user.
type ReadStats struct {
	// UserID is the user these stats belong to (empty = whole library / single-user mode).
	UserID string

	// TotalBooks is the total number of books in the library at query time.
	TotalBooks int

	// BooksRead is the number of books the user has marked as read.
	BooksRead int

	// BooksReadThisYear is the number of books marked read in the current calendar year.
	// Only meaningful when the backend stores read timestamps; otherwise 0.
	BooksReadThisYear int

	// AverageRating is the average star rating the user has given (0 if no rated book).
	// Ratings of 0 (unrated) are excluded from the average.
	AverageRating float64

	// RatedBooks is the number of books with a rating > 0 among those read by the user.
	RatedBooks int

	// TopAuthors is the top authors by number of books read, sorted desc (max 10).
	TopAuthors []LabelCount

	// TopTags is the top tags by number of books read, sorted desc (max 10).
	TopTags []LabelCount

	// TopSeries is the top series by number of books read, sorted desc (max 10).
	TopSeries []LabelCount

	// ByLanguage groups books read by BCP 47 language tag, sorted desc.
	ByLanguage []LabelCount

	// ByMonth groups books read by the calendar month they were marked read.
	// Covers the past 12 months, oldest first.  Empty when the backend does not
	// store read timestamps or no books have been read in that window.
	ByMonth []MonthCount

	// RatingDistribution counts how many books the user has rated 1★…5★.
	// Index 0 = 1 star, index 4 = 5 stars.
	RatingDistribution [5]int
}

// ReadStatsProvider is an optional interface for catalog backends that can
// compute aggregate reading statistics for a given user (or the whole library
// when userID is empty).
type ReadStatsProvider interface {
	// ReadStats returns aggregated statistics for the supplied userID.
	// When userID is empty, stats aggregate across every user / all read books.
	ReadStats(userID string) (*ReadStats, error)
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

	// Publishers returns all distinct publisher names (non-empty), sorted alphabetically.
	Publishers(offset, limit int) ([]string, int, error)

	// BooksByPublisher returns books filtered by exact publisher name.
	BooksByPublisher(publisher string, offset, limit int) ([]Book, int, error)
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
	Collection      *string
	CollectionIndex *string
	IsRead          *bool
	Rating          *int
	AgeRating       *int
	SpiceRating     *int
	// LastMaintenanceAt, if non-nil, sets the maintenance timestamp.
	// Use a zero time.Time to clear it; use time.Now() to mark as "just processed".
	LastMaintenanceAt *time.Time
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

// CollectionLister is an optional interface for catalog backends that support
// listing all distinct editorial collections.
type CollectionLister interface {
	// Collections returns all distinct non-empty editorial collection names
	// sorted alphabetically.
	Collections() ([]string, error)
}

// TagDeleter is an optional interface for catalog backends that support
// removing a tag from all books that have it.
type TagDeleter interface {
	// DeleteTag removes the given tag from all books in the catalog.
	DeleteTag(tag string) error
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

// Recommendation represents a book recommendation from one user to another.
type Recommendation struct {
	// FromUser is the user who sent the recommendation.
	FromUser User

	// ToUser is the user the recommendation is addressed to.
	ToUser User

	// Book is the recommended book.
	Book Book

	// Message is the optional personalised message from the recommender.
	Message string

	// CreatedAt is when the recommendation was created or last updated.
	CreatedAt time.Time
}

// WishlistItem represents a book a user wants to find or acquire.
type WishlistItem struct {
	// ID is a unique identifier for this wishlist entry.
	ID string

	// UserID is the ID of the user who added this item (empty in single-user mode).
	UserID string

	// UserName is the display name of the user (populated for display).
	UserName string

	// UserColor is the CSS hex colour of the user (populated for display).
	UserColor string

	// Title is the title of the book being searched.
	Title string

	// Author is the author of the book being searched.
	Author string

	// ReleaseDate is the expected/known publication date (optional, e.g. "2024" or "2024-03-15").
	ReleaseDate string

	// Notes is any additional notes (e.g. edition, ISBN hint).
	Notes string

	// CreatedAt is when this wishlist item was created.
	CreatedAt time.Time
}

// WishlistManager is an optional interface for catalog backends that support
// per-user wishlists of books the user wants to find or acquire.
type WishlistManager interface {
	// WishlistItems returns all wishlist items. If userID is non-empty, only
	// items belonging to that user are returned; otherwise all items are returned.
	WishlistItems(userID string) ([]WishlistItem, error)

	// AddWishlistItem creates a new wishlist item.
	AddWishlistItem(userID, title, author, releaseDate, notes string) (*WishlistItem, error)

	// UpdateWishlistItem updates the editable fields of an existing item.
	UpdateWishlistItem(id, title, author, releaseDate, notes string) (*WishlistItem, error)

	// DeleteWishlistItem removes the wishlist item with the given ID.
	DeleteWishlistItem(id string) error
}

// SessionData holds the persisted data for a single session token.
type SessionData struct {
	Token  string
	UserID string
	Expiry time.Time
}

// SessionPersistence is an optional interface for catalog backends that support
// persisting session tokens across server restarts.
type SessionPersistence interface {
	// SaveSession upserts a session token with its associated userID and expiry.
	SaveSession(token, userID string, expiry time.Time) error

	// DeleteSession removes a session token (e.g. on logout).
	DeleteSession(token string) error

	// LoadSessions returns all non-expired session tokens.
	LoadSessions() ([]SessionData, error)

	// PruneExpiredSessions deletes all sessions whose expiry is in the past.
	PruneExpiredSessions() error
}

// ToReadItem represents an entry in a user's ordered "to-read" pile.
type ToReadItem struct {
	// UserID is the ID of the user who owns this list entry.
	UserID string

	// Book is the book on the list (fully populated).
	Book Book

	// Position is the 0-based ordering of this entry inside the user's list.
	// Lower values appear first.
	Position int

	// AddedAt is when this entry was added to the to-read pile.
	AddedAt time.Time
}

// ToReadManager is an optional interface for catalog backends that support
// per-user ordered to-read lists.  Books are added to the bottom of the list
// and can be re-ordered.  When a book is marked as read for the user, the
// catalog implementation should remove it from that user's to-read list.
type ToReadManager interface {
	// ToReadList returns the user's ordered to-read list.
	ToReadList(userID string) ([]ToReadItem, error)

	// AddToReadList appends bookID to the bottom of userID's to-read list.
	// If the book is already in the list this is a no-op (returns nil).
	AddToReadList(userID, bookID string) error

	// RemoveFromToReadList removes bookID from userID's to-read list.
	// If the book is not in the list this is a no-op (returns nil).
	RemoveFromToReadList(userID, bookID string) error

	// ReorderToReadList replaces the user's to-read list with bookIDs in the
	// supplied order.  bookIDs not currently on the user's list are ignored;
	// existing entries missing from bookIDs are left at the end in their
	// previous relative order.
	ReorderToReadList(userID string, bookIDs []string) error
}

// Recommender is an optional interface for catalog backends that support
// per-user book recommendations.
type Recommender interface {
	// RecommendBook creates or replaces a recommendation from fromUserID to
	// toUserID for bookID.  message may be empty.
	RecommendBook(fromUserID, toUserID, bookID, message string) error

	// RemoveRecommendation deletes the recommendation (if it exists) from
	// fromUserID to toUserID for bookID.
	RemoveRecommendation(fromUserID, toUserID, bookID string) error

	// RecommendationsForUser returns all recommendations addressed to toUserID,
	// newest first.
	RecommendationsForUser(toUserID string) ([]Recommendation, error)

	// RecommendationsByUser returns all recommendations sent by fromUserID,
	// newest first.
	RecommendationsByUser(fromUserID string) ([]Recommendation, error)

	// BookRecipients returns the IDs of users to whom fromUserID has
	// recommended bookID.
	BookRecipients(fromUserID, bookID string) ([]string, error)
}

// AllRecommendationsLister is an optional interface implemented by Recommender
// backends that can return every recommendation in a single query.  It exists
// to avoid the N+1 pattern of calling RecommendationsForUser once per user
// when the caller wants a deduplicated cross-user view (e.g. an admin's
// global "what has been recommended to anyone" feed).
type AllRecommendationsLister interface {
	// AllRecommendations returns every recommendation in the catalog,
	// newest first.
	AllRecommendations() ([]Recommendation, error)
}

// Webhook event names.  Servers fire these when the corresponding action
// happens on the catalog.  Admins subscribe to one or more of these events
// when registering a webhook.
const (
	WebhookEventBookCreated = "book.created"
	WebhookEventBookUpdated = "book.updated"
	WebhookEventBookDeleted = "book.deleted"
	WebhookEventBookRead    = "book.read"
)

// AllWebhookEvents is the canonical list of events that can fire a webhook.
// Used by the admin UI for the multi-select form.
var AllWebhookEvents = []string{
	WebhookEventBookCreated,
	WebhookEventBookUpdated,
	WebhookEventBookDeleted,
	WebhookEventBookRead,
}

// Webhook is an admin-configured HTTP callback that is notified whenever one
// of its subscribed events occurs.  Secret (when set) is used to compute an
// HMAC-SHA256 signature over the JSON payload, sent in the X-Signature
// header so receivers can verify the call.
type Webhook struct {
	// ID is a unique identifier (UUID-style hex string).
	ID string

	// Name is a short label shown in the admin UI (free-text).
	Name string

	// URL is the absolute HTTP(S) URL the JSON payload is POSTed to.
	URL string

	// Events is the list of event names this webhook is subscribed to.
	Events []string

	// Secret, when non-empty, is used to compute an HMAC-SHA256 signature
	// of the request body and send it in the X-Signature header.
	Secret string

	// Enabled controls whether the dispatcher will fire this webhook.
	// Disabled webhooks are kept in the catalog so the admin can re-enable
	// them without having to re-enter the URL/secret.
	Enabled bool

	// CreatedAt is when the webhook was registered.
	CreatedAt time.Time

	// LastFiredAt is the most recent time the dispatcher attempted to call
	// this webhook (zero value = never fired).
	LastFiredAt time.Time

	// LastStatus is a short string describing the outcome of the last call
	// (e.g. "200 OK", "timeout", "connection refused").
	LastStatus string
}

// WebhookUpdate carries the editable fields for a webhook update.
// Nil pointer fields are left unchanged; non-nil fields replace the value.
type WebhookUpdate struct {
	Name    *string
	URL     *string
	Events  []string // nil = unchanged, empty = clear
	Secret  *string
	Enabled *bool
}

// WebhookManager is an optional interface for catalog backends that support
// managing user-defined webhooks.
type WebhookManager interface {
	// Webhooks returns every registered webhook, newest first.
	Webhooks() ([]Webhook, error)

	// WebhookByID returns the webhook with the given ID or an error if missing.
	WebhookByID(id string) (*Webhook, error)

	// CreateWebhook registers a new webhook and returns the stored value
	// (with ID + CreatedAt populated).
	CreateWebhook(name, url string, events []string, secret string, enabled bool) (*Webhook, error)

	// UpdateWebhook applies the patch to the webhook with the given ID and
	// returns the resulting record.
	UpdateWebhook(id string, update WebhookUpdate) (*Webhook, error)

	// DeleteWebhook removes the webhook with the given ID.
	DeleteWebhook(id string) error

	// RecordWebhookFire stores the outcome of an HTTP delivery attempt.
	RecordWebhookFire(id, status string, at time.Time) error
}

// LibrarianAssociationData represents the persisted pairing with a remote
// "librarian" service.  Only one association can exist at a time per
// nxt-opds instance; storing a new one replaces the previous record.
type LibrarianAssociationData struct {
	// LibrarianURL is the base HTTP(S) URL of the librarian service
	// (e.g. "https://librarian.example.com"), no trailing slash.
	LibrarianURL string

	// LibrarianInstance is the identifier the librarian assigned to this
	// nxt-opds instance during the pairing handshake.
	LibrarianInstance string

	// ChatSecret is the bearer token nxt-opds sends in the Authorization
	// header when relaying chat requests to the librarian.
	ChatSecret string

	// WebhookSecret is the HMAC-SHA256 key nxt-opds uses to sign the
	// X-Signature header on outgoing book-event webhooks fired toward the
	// librarian.
	WebhookSecret string

	// CreatedAt is when the association was first paired.
	CreatedAt time.Time

	// UpdatedAt is when the association was last modified (e.g. secret rotation).
	UpdatedAt time.Time

	// LastSeenAt is the most recent time the remote librarian sent a heartbeat
	// (POST /api/librarian/heartbeat).  Zero means a heartbeat has never been
	// received.  It is not advanced by mutations (rotate/announce) — only by
	// the dedicated heartbeat endpoint, so the admin UI can show liveness.
	LastSeenAt time.Time
}

// LibrarianAssociation is an optional interface for catalog backends that
// persist the pairing with a remote librarian service.  The association is a
// singleton — there is at most one record at any time.
type LibrarianAssociation interface {
	// Get returns the current association, or (nil, nil) if no association
	// has been stored yet.
	Get() (*LibrarianAssociationData, error)

	// Set upserts the association.  CreatedAt is preserved when an existing
	// record is replaced; UpdatedAt is set to time.Now() by the
	// implementation.  ChatSecret / WebhookSecret are required.
	Set(data LibrarianAssociationData) error

	// Clear removes the association.  Idempotent — no error when nothing
	// is stored.
	Clear() error

	// Touch updates only the LastSeenAt field on the existing association,
	// without advancing UpdatedAt (a heartbeat is not a mutation).  Returns
	// nil and does nothing when no association exists.
	Touch(at time.Time) error
}
