# Ralph Fix Plan

## High Priority
- [x] Review codebase and understand architecture - **Greenfield project, bootstrapped from scratch**
- [x] Identify and document key components - **Done: opds, catalog, server packages**
- [x] Set up development environment - **go.mod created, build instructions in AGENT.md**
- [x] Implement `go.sum` and fetch dependencies (`go mod tidy`)
- [x] Implement a file-system catalog backend (scan directory for EPUB/PDF files) - **Done: internal/backend/fs/fs.go**
- [x] Connect catalog backend to server handlers (wire up real data) - **Done: server.New() now takes catalog.Catalog**
- [x] Implement EPUB metadata extraction (title, author, cover from .epub files) - **Done: OPF metadata via stdlib archive/zip + encoding/xml**
- [x] Implement book page instead of dialog - **Done: hash-based routing in Vue SPA (#/books/{id}), dedicated book detail page with cover/metadata/download/edit; GET /api/books/{id} endpoint; detail modal removed**
- [x] Implement frontend in french - **Done: all UI text translated to French (labels, placeholders, toasts, empty states, modal titles, metadata fields, pagination, upload/edit dialogs)**
- [x] The 'has been read' must be a button in book page and not in the edit form - **Done: dedicated "Marquer comme lu / Marquer comme non lu" toggle button in book page action area; isRead checkbox removed from edit modal; toggleRead() calls PATCH /api/books/{id}**
- [x] Add a filter on grid page to have only not readed book - **Done: "Non lus seulement" toggle pill in grid filter bar; ?unread=1 API param; UnreadOnly field in SearchQuery; both fs and sqlite backends filter by is_read=0**
- [x] Sort Book by added date descending by default, add possibility to sort by name, added date - **Done: AddedAt field on Book (file mod time); SortBy/SortOrder on SearchQuery; fs backend sorts by AddedAt desc on Refresh and re-sorts matched slice; sqlite adds added_at column (migration-safe) + sortClause() helper; handleAPIBooks always uses Search with parsed ?sort= param (added_desc/added_asc/title_asc/title_desc); Vue sort selector in filter bar with localStorage persistence**
- [x] Gérer les versions de schema de la base de données et quand une mise a jour de schéma est effectué faire une migration pour ne pas devoir remettre la base à zéro - **Done: PRAGMA user_version–based migration system in sqlite.go (schemaMigrations slice, migrateSchema(), currentSchemaVersion const); migration1 handles both fresh and pre-migration DBs; 3 tests (fresh, idempotent, legacy DB upgrade)**
- [x] Backup database every night - **Done: catalog.Backupper interface; sqlite.Backend.Backup() uses VACUUM INTO for a live consistent copy; pruneBackups() keeps most recent N files; BackupDir/BackupKeep fields in Config (env: BACKUP_DIR/BACKUP_KEEP, default keep=7); runNightlyBackup() goroutine in main.go sleeps until next local midnight then repeats every 24h; 2 tests (file created, prune)**

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
- [x] Pagination on front page grid - **Done: PAGE_SIZE=48 constant, page/totalPages/pageNumbers computed refs, goPage() navigator, offset+limit params on /api/books, prev/next/ellipsis pagination widget in grid view**
- [x] Add an OPDS 2.0 feed that follow https://drafts.opds.io/opds-2.0 - **Done: internal/opds2/feed.go (types + JSON serialization); 7 handlers in handlers.go (root, publications, search, authors, author-books, tags, tag-books); routes registered in server.go at /opds/v2/**
- [x] Add a github action that build the docker and push it to docker hub - **Done: .github/workflows/docker.yml – triggers on push to main and version tags; multi-platform (amd64+arm64); uses DOCKERHUB_USERNAME/DOCKERHUB_TOKEN secrets; semantic version tags + sha tags; GHA cache**
- [x] Add a github action that build binary and release it on github repository - **Done: .github/workflows/release.yml – triggers on version tags; matrix: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64; CGO_ENABLED=0; SHA256SUMS.txt; uses softprops/action-gh-release@v2**
- [x] Add a button delete on book page - **Done: catalog.Deleter interface; DeleteBook on fs+sqlite backends (removes file + cover); DELETE /api/books/{id} handler; red "Supprimer" button with trash icon + confirmation dialog in book detail page; navigates back to library on success**
- [x] ajust size cover to the frame - **Done: CSS book-cover uses absolute-positioned img with object-fit:cover for correct frame fill**
- [x] Add "series title - #number/#totalnumber" to the frontpage between the title and the author - **Done: amber series line shown in grid card meta between title and author**
- [x] Add "series title - #number/#totalnumber" to the book page between the title and the author instead of badge - **Done: inline amber text below title, SeriesTotal field added throughout stack**
- [x] Add total series to the book metadata editing - **Done: SeriesTotal field in catalog.Book/BookUpdate, fs/sqlite backends, handlers.go, edit modal**
- [x] Add series page sort by number - **Done: catalog.SeriesLister interface; Series() on fs+sqlite backends; GET /api/series endpoint; ?series= + ?sort=series_index params on /api/books; series page view (#/series/<name>) in Vue SPA; clickable series links on grid card and book detail; "Tome N/Total" badge in series page; books sorted numerically by series_index**
- [x] Add star rating - **Done: Rating int field (0=unrated, 1-5) throughout stack (catalog, fs, sqlite, handlers); interactive 5-star widget on book detail page (click same star to clear); small read-only stars on grid cards**

## Low Priority
- [x] Performance optimization (background indexing) - **Done: catalog.Refresher interface; background ticker goroutine in main.go (REFRESH_INTERVAL env / refresh_interval config, default 5m); POST /api/refresh manual endpoint; refresh button with spinner in Vue UI header**
- [x] Code cleanup and refactoring - **Done: tests for POST /api/refresh (success, 501, 500 paths); 6 tests for refresh_interval config parsing; updated package doc in config.go; updated Architecture Notes to reflect current package layout**
- [x] Docker / container support - **Done: Dockerfile (multi-stage, debian-slim runtime, CGO_ENABLED=0), .dockerignore, docker-compose.yml, README.md updated with full docs**
- [x] SQLite index for large collections - **Done: internal/backend/sqlite/sqlite.go, selected via backend: "sqlite" in config or BACKEND=sqlite env var; epub metadata extraction refactored into internal/epub/epub.go shared package; 9 tests in sqlite_test.go**
- [x] Make the "has been read" mark on cover more visible - **Done: replaced tiny top-right ✓ pill with a prominent bottom-of-cover green gradient overlay strip showing a bold checkmark + "Lu" text**
- [x] Passer la marque "lu" avec un bandeau en dessous de dégradé de vert - **Done: replaced in-cover overlay strip with a separate green gradient banner (from-green-600 to-emerald-500) rendered below the cover, with rounded bottom corners; cover uses rounded-t-lg when book is read so the pair forms one visual unit**
- [x] Sur la page du livre les étoiles de score ne marche pas le choix précédent n'est pas affiché mais est bien sauvegardé. - **Done: handleAPIBook and handleAPIUpdateBook were missing Rating: bk.Rating in their bookJSON responses; added to both handlers**
- [x] refresh button use basic auth instead of session cookie - **Done: added apiFetch() helper in Vue SPA that checks every API response for HTTP 401 and immediately redirects to /login; replaces all 10 raw fetch('/api/…') calls – loadBooks, loadBook, loadSeries, toggleRead, setRating, saveEdits, deleteBook, onCoverFileChange, doRefresh, doUpload – so a stale/expired session is always handled cleanly (redirect to login) instead of showing a cryptic error toast or triggering a browser Basic-Auth dialog**
- [x] Some cover are not extract from epub because of bad metadata, search the image on first html page - **Done: findCoverInSpine() walks OPF spine in order, opens first HTML/XHTML item, uses findFirstImgSrc() to locate first <img src="…">, saves image as book cover; 9 unit tests in epub_test.go**
- [x] allow update cover - **Done: catalog.CoverUpdater interface; UpdateCover() on fs+sqlite backends (removes old cover files, writes new image, updates DB/in-memory records); POST /api/books/{id}/cover handler (20 MB limit, auto-detects ext from MIME/filename); "Changer la couverture" button in book detail left column (file input ref, cache-busts URL with ?t=timestamp after upload); handleCover now uses actual file mod-time for proper browser cache invalidation**

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
- `internal/server` - HTTP server, routes, handlers, auth
- `internal/epub` - Shared EPUB/PDF metadata extraction (archive/zip + encoding/xml)
- `internal/config` - YAML config loading with env-var overrides
- `internal/backend/fs` - In-memory filesystem backend (.metadata.json overrides)
- `internal/backend/sqlite` - SQLite-indexed backend (.catalog.db)
- `web/` - Embedded Vue 3 + Tailwind CSS frontend (go:embed)

### Key Design Decisions
- Catalog backend is injected via interface (testable, swappable)
- Optional interfaces (Uploader, CoverProvider, Updater, Refresher) let backends
  opt-in to additional capabilities; server detects each via type assertion
- OPDS XML serialization uses stdlib encoding/xml
- gorilla/mux for URL parameter extraction
- Handlers are thin; business logic stays in catalog backends
- modernc.org/sqlite is pure Go (CGO_ENABLED=0 works, Docker scratch/slim OK)

## Notes
- Focus on MVP functionality first
- Ensure each feature is properly tested
- Update this file after each major milestone
- Create or increment a release version and tag it on git commit with format v[number].[number]
