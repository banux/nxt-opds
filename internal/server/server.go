// Package server implements the HTTP server and routing for nxt-opds.
package server

import (
	"io/fs"
	"net/http"
	"time"

	"github.com/gorilla/mux"

	"github.com/banux/nxt-opds/internal/catalog"
	"github.com/banux/nxt-opds/internal/mcp"
	"github.com/banux/nxt-opds/internal/webhooks"
)

// Options holds optional configuration for the Server.
type Options struct {
	// Password is the shared password for form-based session authentication.
	// If empty, authentication is disabled (useful for development).
	Password string

	// OPDSToken is the token accepted in the ?token= query parameter on OPDS
	// routes, allowing OPDS reader clients to authenticate without Basic Auth.
	// If empty, token authentication is disabled for OPDS routes.
	OPDSToken string

	// StaticFS is the filesystem containing the frontend static assets.
	// If nil, the frontend is not served.
	StaticFS fs.FS

	// Version is the current binary version (e.g. "v1.52.0" or "dev").
	// Used by the update-check endpoint.
	Version string

	// Debug enables verbose request logging on the auth middleware and the
	// MCP endpoint.  Useful for diagnosing why an MCP client cannot connect.
	Debug bool
}

// Server is the HTTP server for the OPDS catalog.
type Server struct {
	router        *mux.Router
	catalog       catalog.Catalog
	uploader      catalog.Uploader      // optional; nil if backend doesn't support upload
	coverProvider catalog.CoverProvider // optional; nil if backend doesn't support cover serving
	coverUpdater  catalog.CoverUpdater  // optional; nil if backend doesn't support cover update
	updater       catalog.Updater       // optional; nil if backend doesn't support metadata editing
	refresher     catalog.Refresher     // optional; nil if backend doesn't support manual refresh
	deleter       catalog.Deleter       // optional; nil if backend doesn't support deletion
	seriesLister      catalog.SeriesLister      // optional; nil if backend doesn't support series listing
	collectionLister  catalog.CollectionLister  // optional; nil if backend doesn't support collection listing
	userManager     catalog.UserManager     // optional; nil if backend doesn't support user management
	userReadManager catalog.UserReadManager // optional; nil if backend doesn't support per-user read
	recommender     catalog.Recommender     // optional; nil if backend doesn't support recommendations
	wishlistManager catalog.WishlistManager // optional; nil if backend doesn't support wishlists
	toReadManager   catalog.ToReadManager   // optional; nil if backend doesn't support to-read lists
	readStats       catalog.ReadStatsProvider // optional; nil if backend doesn't expose reading statistics
	webhookManager catalog.WebhookManager // optional; nil if backend doesn't store webhooks
	webhooks       *webhooks.Dispatcher  // dispatches catalog events to webhooks (no-op when webhookManager is nil)
	librarianAssoc catalog.LibrarianAssociation // optional; nil if backend doesn't persist the librarian pairing
	pairingCodes   *pairingCodeStore     // in-memory single-use codes for admin → librarian pairing
	mcpServer     *mcp.Server           // MCP server for AI agent access
	sessions      *sessionStore
	opts          Options
	opdsToken string    // token for OPDS route authentication
	startedAt time.Time // process start time, used by /api/ping for restart detection
}

// New creates and configures a new Server with the given catalog backend and options.
// If the backend also implements catalog.Uploader, the upload endpoint is enabled.
// If the backend also implements catalog.CoverProvider, the cover endpoint is enabled.
// If opts.Password is non-empty, session-cookie auth is required on all endpoints except /health and /login.
// If opts.StaticFS is non-nil, the frontend is served at /.
func New(cat catalog.Catalog, opts Options) *Server {
	s := &Server{
		router:    mux.NewRouter(),
		catalog:   cat,
		sessions:  newSessionStore(),
		opts:      opts,
		opdsToken: opts.OPDSToken,
		startedAt: time.Now(),
	}
	if u, ok := cat.(catalog.Uploader); ok {
		s.uploader = u
	}
	if cp, ok := cat.(catalog.CoverProvider); ok {
		s.coverProvider = cp
	}
	if cu, ok := cat.(catalog.CoverUpdater); ok {
		s.coverUpdater = cu
	}
	if up, ok := cat.(catalog.Updater); ok {
		s.updater = up
	}
	if rf, ok := cat.(catalog.Refresher); ok {
		s.refresher = rf
	}
	if dl, ok := cat.(catalog.Deleter); ok {
		s.deleter = dl
	}
	if sl, ok := cat.(catalog.SeriesLister); ok {
		s.seriesLister = sl
	}
	if cl, ok := cat.(catalog.CollectionLister); ok {
		s.collectionLister = cl
	}
	if um, ok := cat.(catalog.UserManager); ok {
		s.userManager = um
	}
	if urm, ok := cat.(catalog.UserReadManager); ok {
		s.userReadManager = urm
	}
	if rc, ok := cat.(catalog.Recommender); ok {
		s.recommender = rc
	}
	if wm, ok := cat.(catalog.WishlistManager); ok {
		s.wishlistManager = wm
	}
	if trm, ok := cat.(catalog.ToReadManager); ok {
		s.toReadManager = trm
	}
	if rs, ok := cat.(catalog.ReadStatsProvider); ok {
		s.readStats = rs
	}
	if wm, ok := cat.(catalog.WebhookManager); ok {
		s.webhookManager = wm
	}
	if la, ok := cat.(catalog.LibrarianAssociation); ok {
		s.librarianAssoc = la
	}
	s.pairingCodes = newPairingCodeStore()
	s.webhooks = webhooks.New(s.webhookManager)
	// If the catalog backend supports session persistence, wire it up and load
	// any sessions that survived the previous process run.
	if sp, ok := cat.(catalog.SessionPersistence); ok {
		s.sessions.persistence = sp
		s.sessions.loadFromPersistence()
		// Best-effort prune of stale rows at startup.
		if err := sp.PruneExpiredSessions(); err != nil {
			_ = err // non-fatal
		}
	}

	s.mcpServer = mcp.New(cat)
	s.mcpServer.SetDebug(opts.Debug)
	s.registerRoutes()
	return s
}

// ServeHTTP implements http.Handler, delegating to the mux router.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// requireAdmin wraps a handler so it only runs for admin users.  In single-user
// mode (no UserManager or no users registered), or when authentication is
// disabled (no Password), the wrapped handler runs unchanged.  Otherwise the
// caller must be authenticated as a user whose IsAdmin flag is true; anything
// else returns 403 Forbidden.
//
// A shared OPDS token (or a session that resolved to no specific user) does
// not grant admin access — we explicitly require a user identity.
func (s *Server) requireAdmin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.userManager != nil && s.hasMultipleUsers() {
			uid := currentUserID(r)
			if uid == "" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			me, err := s.userManager.UserByID(uid)
			if err != nil || me == nil || !me.IsAdmin {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		h(w, r)
	}
}

// registerRoutes sets up all endpoint routes.
func (s *Server) registerRoutes() {
	r := s.router
	auth := authMiddleware(s.opts.Password, s.opdsToken, s.sessions, s.userManager, s.opts.Debug)

	// Always-public endpoints (no auth required)
	r.HandleFunc("/health", s.handleHealth).Methods(http.MethodGet)
	r.HandleFunc("/api/ping", s.handleAPIPing).Methods(http.MethodGet)
	r.HandleFunc("/login", s.handleLoginPage).Methods(http.MethodGet)
	r.HandleFunc("/login", s.handleLoginPost).Methods(http.MethodPost)
	r.HandleFunc("/logout", s.handleLogout).Methods(http.MethodPost, http.MethodGet)

	// PWA assets must be publicly accessible for browser install flow
	if s.opts.StaticFS != nil {
		staticServer := http.FileServer(http.FS(s.opts.StaticFS))
		r.HandleFunc("/sw.js", s.handleSW).Methods(http.MethodGet)
		r.Handle("/manifest.json", staticServer).Methods(http.MethodGet)
		r.PathPrefix("/icons/").Handler(staticServer).Methods(http.MethodGet)
	}

	// All other routes are wrapped with the auth middleware.
	protected := r.NewRoute().Subrouter()
	protected.Use(auth)

	// Root navigation feed
	protected.HandleFunc("/opds", s.handleRoot).Methods(http.MethodGet)
	protected.HandleFunc("/opds/", s.handleRoot).Methods(http.MethodGet)

	// All books acquisition feed
	protected.HandleFunc("/opds/books", s.handleAllBooks).Methods(http.MethodGet)

	// Single book entry
	protected.HandleFunc("/opds/books/{id}", s.handleBook).Methods(http.MethodGet)

	// File download
	protected.HandleFunc("/opds/books/{id}/download", s.handleDownload).Methods(http.MethodGet)

	// Search
	protected.HandleFunc("/opds/search", s.handleSearch).Methods(http.MethodGet)

	// Browse by author
	protected.HandleFunc("/opds/authors", s.handleAuthors).Methods(http.MethodGet)
	protected.HandleFunc("/opds/authors/{author}", s.handleAuthorBooks).Methods(http.MethodGet)

	// Browse by tag/genre
	protected.HandleFunc("/opds/tags", s.handleTags).Methods(http.MethodGet)
	protected.HandleFunc("/opds/tags/{tag}", s.handleTagBooks).Methods(http.MethodGet)

	// Browse by publisher
	protected.HandleFunc("/opds/publishers", s.handlePublishers).Methods(http.MethodGet)
	protected.HandleFunc("/opds/publishers/{publisher}", s.handlePublisherBooks).Methods(http.MethodGet)

	// Unread books feed
	protected.HandleFunc("/opds/unread", s.handleUnreadBooks).Methods(http.MethodGet)

	// Wishlist feed
	protected.HandleFunc("/opds/wishlist", s.handleOPDSWishlist).Methods(http.MethodGet)

	// Recommendations feed
	protected.HandleFunc("/opds/recommendations", s.handleOPDSRecommendations).Methods(http.MethodGet)

	// To-read list feed (OPDS v1)
	protected.HandleFunc("/opds/to-read", s.handleOPDSToRead).Methods(http.MethodGet)

	// OpenSearch description document
	protected.HandleFunc("/opds/opensearch.xml", s.handleOpenSearch).Methods(http.MethodGet)

	// API: JSON books list for the web frontend
	protected.HandleFunc("/api/books", s.handleAPIBooks).Methods(http.MethodGet)

	// API: get single book by ID
	protected.HandleFunc("/api/books/{id}", s.handleAPIBook).Methods(http.MethodGet)

	// API: update book metadata (enabled when backend supports it)
	protected.HandleFunc("/api/books/{id}", s.handleAPIUpdateBook).Methods(http.MethodPatch)

	// API: delete a book (enabled when backend supports it).  Admin-only in
	// multi-user mode to avoid one user destroying another user's library.
	protected.HandleFunc("/api/books/{id}", s.requireAdmin(s.handleAPIDeleteBook)).Methods(http.MethodDelete)

	// API: update cover image for a book (enabled when backend supports it)
	protected.HandleFunc("/api/books/{id}/cover", s.handleAPIUpdateCover).Methods(http.MethodPost)

	// API: upload a new book (enabled when backend supports it)
	protected.HandleFunc("/api/upload", s.handleUpload).Methods(http.MethodPost)

	// API: list all distinct authors
	protected.HandleFunc("/api/authors", s.handleAPIAuthors).Methods(http.MethodGet)

	// API: list all distinct tags
	protected.HandleFunc("/api/tags", s.handleAPITags).Methods(http.MethodGet)
	// API: delete a tag from all books.  Admin-only.
	protected.HandleFunc("/api/tags/{tag}", s.requireAdmin(s.handleAPIDeleteTag)).Methods(http.MethodDelete)

	// API: list all distinct publishers
	protected.HandleFunc("/api/publishers", s.handleAPIPublishers).Methods(http.MethodGet)

	// API: list all distinct series
	protected.HandleFunc("/api/series", s.handleAPISeries).Methods(http.MethodGet)

	// API: list all distinct editorial collections
	protected.HandleFunc("/api/collections", s.handleAPICollections).Methods(http.MethodGet)

	// API: public server config (opdsToken, etc.) for the web frontend
	protected.HandleFunc("/api/config", s.handleAPIConfig).Methods(http.MethodGet)

	// API: PNG QR code that encodes the user's OPDS or MCP URL with token
	// embedded — used for pairing phones / e-readers without typing it.
	protected.HandleFunc("/api/qr", s.handleAPIQR).Methods(http.MethodGet)

	// API: current logged-in user info
	protected.HandleFunc("/api/me", s.handleAPIMe).Methods(http.MethodGet)

	// API: user management.  All write operations require admin privileges in
	// multi-user mode; listing is allowed for everyone (token field is filtered
	// per-caller in handleAPIUsers).
	protected.HandleFunc("/api/users", s.handleAPIUsers).Methods(http.MethodGet)
	protected.HandleFunc("/api/users", s.requireAdmin(s.handleAPICreateUser)).Methods(http.MethodPost)
	protected.HandleFunc("/api/users/{id}", s.requireAdmin(s.handleAPIUpdateUser)).Methods(http.MethodPatch)
	protected.HandleFunc("/api/users/{id}", s.requireAdmin(s.handleAPIDeleteUser)).Methods(http.MethodDelete)
	protected.HandleFunc("/api/users/{id}/token", s.requireAdmin(s.handleAPIRegenerateUserToken)).Methods(http.MethodPost)

	// API: per-user read toggle
	protected.HandleFunc("/api/books/{id}/read", s.handleAPIToggleRead).Methods(http.MethodPut)

	// API: book recommendations
	protected.HandleFunc("/api/books/{id}/recommend", s.handleAPIRecommend).Methods(http.MethodPost)
	protected.HandleFunc("/api/books/{id}/recommend/{toUserID}", s.handleAPIRemoveRecommend).Methods(http.MethodDelete)
	protected.HandleFunc("/api/books/{id}/recipients", s.handleAPIBookRecipients).Methods(http.MethodGet)
	protected.HandleFunc("/api/recommendations", s.handleAPIRecommendations).Methods(http.MethodGet)

	// API: wishlist management
	protected.HandleFunc("/api/stats", s.handleAPIStats).Methods(http.MethodGet)

	protected.HandleFunc("/api/wishlist", s.handleAPIWishlist).Methods(http.MethodGet)
	protected.HandleFunc("/api/wishlist", s.handleAPIAddWishlistItem).Methods(http.MethodPost)
	protected.HandleFunc("/api/wishlist/{id}", s.handleAPIUpdateWishlistItem).Methods(http.MethodPatch)
	protected.HandleFunc("/api/wishlist/{id}", s.handleAPIDeleteWishlistItem).Methods(http.MethodDelete)

	// API: per-user to-read list
	protected.HandleFunc("/api/to-read", s.handleAPIToRead).Methods(http.MethodGet)
	protected.HandleFunc("/api/to-read", s.handleAPIAddToRead).Methods(http.MethodPost)
	protected.HandleFunc("/api/to-read/reorder", s.handleAPIReorderToRead).Methods(http.MethodPut)
	protected.HandleFunc("/api/to-read/{bookId}", s.handleAPIRemoveToRead).Methods(http.MethodDelete)

	// API: trigger a manual catalog refresh (enabled when backend supports it).
	// Admin-only in multi-user mode (avoid log-in users triggering heavy I/O).
	protected.HandleFunc("/api/refresh", s.requireAdmin(s.handleAPIRefresh)).Methods(http.MethodPost)

	// API: webhook management.  Admin-only in multi-user mode.
	protected.HandleFunc("/api/webhooks", s.requireAdmin(s.handleAPIWebhooks)).Methods(http.MethodGet)
	protected.HandleFunc("/api/webhooks", s.requireAdmin(s.handleAPICreateWebhook)).Methods(http.MethodPost)
	protected.HandleFunc("/api/webhooks/{id}", s.requireAdmin(s.handleAPIUpdateWebhook)).Methods(http.MethodPatch)
	protected.HandleFunc("/api/webhooks/{id}", s.requireAdmin(s.handleAPIDeleteWebhook)).Methods(http.MethodDelete)
	protected.HandleFunc("/api/webhooks/{id}/test", s.requireAdmin(s.handleAPITestWebhook)).Methods(http.MethodPost)

	// API: librarian pairing.  Generating a code is restricted to admins who
	// authenticated with a session cookie so a leaked OPDS/per-user token can
	// never start a pairing handshake.
	protected.HandleFunc("/api/librarian/pairing-code",
		s.requireSessionAdmin(s.handleAPILibrarianPairingCode)).Methods(http.MethodPost)

	// API: auto-update check (read-only) is open to all logged-in users so the
	// SPA can show the "update available" badge.  Apply and restart are admin-only.
	protected.HandleFunc("/api/update/check", s.handleAPIUpdateCheck).Methods(http.MethodGet)
	protected.HandleFunc("/api/update/apply", s.requireAdmin(s.handleAPIUpdateApply)).Methods(http.MethodPost)
	protected.HandleFunc("/api/restart", s.requireAdmin(s.handleAPIRestart)).Methods(http.MethodPost)

	// MCP: Model Context Protocol endpoint for AI agent access.
	// POST handles JSON-RPC requests; GET returns endpoint info so clients
	// (and operators using `curl /mcp`) get a clear hint instead of a 404
	// from the catch-all SPA fallback.
	protected.Handle("/mcp", s.mcpServer).Methods(http.MethodPost)
	protected.HandleFunc("/mcp", s.handleMCPInfo).Methods(http.MethodGet)

	// Cover image endpoint
	protected.HandleFunc("/covers/{id}", s.handleCover).Methods(http.MethodGet)

	// OPDS 2.0 JSON feed (https://drafts.opds.io/opds-2.0)
	protected.HandleFunc("/opds/v2", s.handleOPDS2Root).Methods(http.MethodGet)
	protected.HandleFunc("/opds/v2/publications", s.handleOPDS2Publications).Methods(http.MethodGet)
	protected.HandleFunc("/opds/v2/search", s.handleOPDS2Search).Methods(http.MethodGet)
	protected.HandleFunc("/opds/v2/authors", s.handleOPDS2Authors).Methods(http.MethodGet)
	protected.HandleFunc("/opds/v2/authors/{author}", s.handleOPDS2AuthorBooks).Methods(http.MethodGet)
	protected.HandleFunc("/opds/v2/tags", s.handleOPDS2Tags).Methods(http.MethodGet)
	protected.HandleFunc("/opds/v2/tags/{tag}", s.handleOPDS2TagBooks).Methods(http.MethodGet)
	protected.HandleFunc("/opds/v2/publishers", s.handleOPDS2Publishers).Methods(http.MethodGet)
	protected.HandleFunc("/opds/v2/publishers/{publisher}", s.handleOPDS2PublisherBooks).Methods(http.MethodGet)
	protected.HandleFunc("/opds/v2/unread", s.handleOPDS2Unread).Methods(http.MethodGet)
	protected.HandleFunc("/opds/v2/wishlist", s.handleOPDS2Wishlist).Methods(http.MethodGet)
	protected.HandleFunc("/opds/v2/recommendations", s.handleOPDS2Recommendations).Methods(http.MethodGet)
	protected.HandleFunc("/opds/v2/to-read", s.handleOPDS2ToRead).Methods(http.MethodGet)

	// EPUB internal file serving: some OPDS readers follow links into the EPUB
	// archive (e.g. /opds/books/{id}/META-INF/container.xml).  This route opens
	// the EPUB ZIP and streams the requested inner file.
	// Must be registered after the more-specific /download route.
	protected.HandleFunc("/opds/books/{id}/{filepath:.*}", s.handleEPUBFile).Methods(http.MethodGet)

	// Catch-all for any unmatched /opds/** path – returns a proper XML 404
	// instead of an HTML page so OPDS clients receive parseable XML.
	protected.PathPrefix("/opds/").HandlerFunc(s.handleOPDSNotFound)

	// Frontend static assets – serves index.html at / and any static files.
	// When StaticFS is nil (e.g. in tests), a catch-all 404 handler is
	// registered so that the auth middleware still runs for all paths.
	if s.opts.StaticFS != nil {
		fileServer := http.FileServer(http.FS(s.opts.StaticFS))
		protected.PathPrefix("/").Handler(fileServer)
	} else {
		protected.PathPrefix("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		})
	}
}
