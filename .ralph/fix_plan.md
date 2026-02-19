# Ralph Fix Plan

## High Priority
- [x] Review codebase and understand architecture - **Greenfield project, bootstrapped from scratch**
- [x] Identify and document key components - **Done: opds, catalog, server packages**
- [x] Set up development environment - **go.mod created, build instructions in AGENT.md**
- [x] Implement `go.sum` and fetch dependencies (`go mod tidy`)
- [x] Implement a file-system catalog backend (scan directory for EPUB/PDF files) - **Done: internal/backend/fs/fs.go**
- [x] Connect catalog backend to server handlers (wire up real data) - **Done: server.New() now takes catalog.Catalog**
- [x] Implement EPUB metadata extraction (title, author, cover from .epub files) - **Done: OPF metadata via stdlib archive/zip + encoding/xml**

## Medium Priority
- [x] Add EPUB upload endpoint (POST /api/upload) with file storage + instant catalog indexing - **Done: StoreBook on fs.Backend, handleUpload + handleDownload handlers**
- [x] Add test coverage for opds feed serialization - **Done: internal/opds/feed_test.go (5 tests covering structure, XML serialization, round-trip, date formatting, multiple entries)**
- [x] Add test coverage for HTTP handlers - **Done: internal/server/handlers_test.go (34 tests covering all OPDS routes, API endpoints, pagination helpers, update book); also fixed auth.go slice-bounds panic and missing catch-all route in server.go**
- [x] Add pagination link headers (first/last/next/prev) to feeds - **Done: RelFirst/Last/Next/Previous consts in opds/feed.go; addPaginationLinks helper in handlers.go; applied to all 6 paginated OPDS handlers**
- [x] Add cover image serving endpoint - **Done: EPUB cover extraction (EPUB2+EPUB3), /covers/{id} HTTP endpoint, CoverProvider interface**
- [x] Add password authentication with login form - **Done: session-cookie auth, POST /login form, GET /login page (Tailwind), POST /logout, Basic Auth fallback for OPDS readers, HttpOnly cookie, constant-time comparison, 30-day sessions**
- [x] Vue+Tailwind CSS frontend - **Done: web/index.html (CDN Vue3+Tailwind CSS), embedded via Go embed.FS, served at /, GET /api/books JSON endpoint, Feedbooks-style book grid with covers, search, upload dialog, detail dialog, dark mode, color gradients for missing covers, pagination**
- [x] Add configuration file support (YAML/TOML) - **Done: internal/config/config.go with Config struct, Load() (YAML file + env var override), FindConfigFile() (NXT_OPDS_CONFIG env, ./nxt-opds.yaml, ~/.config/nxt-opds/config.yaml); main.go updated; gopkg.in/yaml.v3 added**
- [x] Book metadata editing (title, authors, tags, series via PATCH /api/books/{id}) - **Done: metaOverride JSON store (.metadata.json), Updater interface, fs.Backend.UpdateBook, PATCH /api/books/{id} handler**
- [x] Series support: add Series/SeriesIndex fields to Book, display in UI - **Done: Book.Series/SeriesIndex fields, shown in detail modal, editable via edit modal**
- [x] "Has been read" mark: toggle via PATCH /api/books/{id}, show indicator in UI - **Done: Book.IsRead field, green badge on card, checkbox in edit modal**

## Low Priority
- [ ] Performance optimization (feed caching, background indexing)
- [ ] Code cleanup and refactoring
- [ ] Docker / container support
- [ ] SQLite index for large collections

## Completed
- [x] Project enabled for Ralph
- [x] Bootstrapped Go module (go.mod, main.go)
- [x] OPDS feed types and XML serialization (internal/opds/feed.go)
- [x] Catalog interface and data types (internal/catalog/catalog.go)
- [x] HTTP server and routing (internal/server/server.go)
- [x] HTTP request handlers skeleton (internal/server/handlers.go)
- [x] README.md with API docs
- [x] Filesystem catalog backend (internal/backend/fs/fs.go)
- [x] EPUB OPF metadata extraction (title, author, subject, language, date)
- [x] Catalog wired into server and all handlers populated
- [x] Tests for fs backend (internal/backend/fs/fs_test.go)

## Architecture Notes

### Packages
- `internal/opds` - Pure OPDS/Atom types and XML serialization, no I/O
- `internal/catalog` - Domain model + Catalog interface (backend-agnostic)
- `internal/server` - HTTP server, routes, handlers
- Future: `internal/backend/fs` - Filesystem catalog backend
- Future: `internal/backend/sqlite` - SQLite-indexed backend

### Key Design Decisions
- Catalog backend is injected via interface (testable, swappable)
- OPDS XML serialization uses stdlib encoding/xml
- gorilla/mux for URL parameter extraction
- Handlers are thin; business logic stays in catalog backends

## Notes
- Focus on MVP functionality first
- Ensure each feature is properly tested
- Update this file after each major milestone
