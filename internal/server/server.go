// Package server implements the HTTP server and routing for nxt-opds.
package server

import (
	"io/fs"
	"net/http"

	"github.com/gorilla/mux"

	"github.com/banux/nxt-opds/internal/catalog"
)

// Options holds optional configuration for the Server.
type Options struct {
	// Password is the shared password for form-based session authentication.
	// If empty, authentication is disabled (useful for development).
	Password string

	// StaticFS is the filesystem containing the frontend static assets.
	// If nil, the frontend is not served.
	StaticFS fs.FS
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
	seriesLister  catalog.SeriesLister  // optional; nil if backend doesn't support series listing
	sessions      *sessionStore
	opts          Options
}

// New creates and configures a new Server with the given catalog backend and options.
// If the backend also implements catalog.Uploader, the upload endpoint is enabled.
// If the backend also implements catalog.CoverProvider, the cover endpoint is enabled.
// If opts.Password is non-empty, session-cookie auth is required on all endpoints except /health and /login.
// If opts.StaticFS is non-nil, the frontend is served at /.
func New(cat catalog.Catalog, opts Options) *Server {
	s := &Server{
		router:   mux.NewRouter(),
		catalog:  cat,
		sessions: newSessionStore(),
		opts:     opts,
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
	s.registerRoutes()
	return s
}

// ServeHTTP implements http.Handler, delegating to the mux router.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// registerRoutes sets up all endpoint routes.
func (s *Server) registerRoutes() {
	r := s.router
	auth := authMiddleware(s.opts.Password, s.sessions)

	// Always-public endpoints (no auth required)
	r.HandleFunc("/health", s.handleHealth).Methods(http.MethodGet)
	r.HandleFunc("/login", s.handleLoginPage).Methods(http.MethodGet)
	r.HandleFunc("/login", s.handleLoginPost).Methods(http.MethodPost)
	r.HandleFunc("/logout", s.handleLogout).Methods(http.MethodPost, http.MethodGet)

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

	// OpenSearch description document
	protected.HandleFunc("/opds/opensearch.xml", s.handleOpenSearch).Methods(http.MethodGet)

	// API: JSON books list for the web frontend
	protected.HandleFunc("/api/books", s.handleAPIBooks).Methods(http.MethodGet)

	// API: get single book by ID
	protected.HandleFunc("/api/books/{id}", s.handleAPIBook).Methods(http.MethodGet)

	// API: update book metadata (enabled when backend supports it)
	protected.HandleFunc("/api/books/{id}", s.handleAPIUpdateBook).Methods(http.MethodPatch)

	// API: delete a book (enabled when backend supports it)
	protected.HandleFunc("/api/books/{id}", s.handleAPIDeleteBook).Methods(http.MethodDelete)

	// API: update cover image for a book (enabled when backend supports it)
	protected.HandleFunc("/api/books/{id}/cover", s.handleAPIUpdateCover).Methods(http.MethodPost)

	// API: upload a new book (enabled when backend supports it)
	protected.HandleFunc("/api/upload", s.handleUpload).Methods(http.MethodPost)

	// API: list all distinct series
	protected.HandleFunc("/api/series", s.handleAPISeries).Methods(http.MethodGet)

	// API: trigger a manual catalog refresh (enabled when backend supports it)
	protected.HandleFunc("/api/refresh", s.handleAPIRefresh).Methods(http.MethodPost)

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
