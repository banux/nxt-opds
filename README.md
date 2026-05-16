# nxt-opds

A lightweight personal eBook library server written in Go, with an OPDS catalog and a Vue 3 web UI.

[OPDS](https://specs.opds.io/opds-1.2) (Open Publication Distribution System) is a catalog format for discovering and distributing digital publications. Point any OPDS reader (Kybook, Moon+ Reader, Calibre, etc.) at `/opds` to browse your library.

## Features

- **OPDS 1.2 and OPDS 2.0** compliant catalog feeds (navigation + acquisition)
- **Vue 3 + Tailwind CSS** web UI (no build step) with Feedbooks-style book grid
- **EPUB upload** with instant metadata extraction (title, authors, cover, series, tags, publisher, language)
- **Editable metadata** — title, authors, tags, series, collection, publisher, language, age rating, spice rating (0-5 for 16+/18+ titles), cover
- **Age classification** — multi-select filter chips (?, 3+, 6+, 10+, 12+, 16+, 18+); per-child-profile max-age enforcement
- **Multi-user support** — per-user read status, child profiles, user-coloured read banners
- **To-read pile** — personal ordered reading queue, exposed in OPDS feeds and the MCP server
- **Reading statistics** — per-user dashboard (totals, ratings, top authors/tags/series, 12-month chart)
- **Wishlist** — personal reading wish list, exposed in OPDS feeds
- **Recommendations** — send a book recommendation to another user
- **Integrated EPUB reader** with prev/next book navigation and swipe/keyboard support
- **Librarian pairing** — pair an external "librarian" service to enable a chat assistant (uses the MCP server under the hood)
- **MCP server** — AI agent access to the catalog over the Model Context Protocol
- **Auto-update** — download and apply a new binary from GitHub releases in one click
- **PWA** — installable as a web app with offline service worker
- **Password-protected** login (session cookie + OPDS bearer token)
- **Two catalog backends**: in-memory (`fs`) or persistent SQLite (`sqlite`)
- **Background refresh** — automatic rescan of the books directory
- **Nightly SQLite backups** with configurable retention
- **Webhooks** — admin-configured HTTP callbacks fired on book lifecycle events
- **Single static binary** with embedded frontend; version shown in the UI footer

## Quick Start

### Binary

```bash
# Build (Go 1.24+ required)
go build -o nxt-opds .

# Run with SQLite backend (recommended)
AUTH_PASSWORD=secret BACKEND=sqlite ./nxt-opds
```

Open `http://localhost:8080/` in a browser. Point OPDS readers at `http://localhost:8080/opds`.

### Docker

```bash
# Build and start with Docker Compose
AUTH_PASSWORD=secret docker compose up -d
```

The `books/` directory in the current folder is mounted at `/data/books` inside the container.

Or build and run manually:

```bash
docker build -t nxt-opds .
docker run -d \
  -p 8080:8080 \
  -v /path/to/books:/data/books \
  -e AUTH_PASSWORD=secret \
  -e BACKEND=sqlite \
  nxt-opds
```

## Configuration

Configuration is loaded in this order (later sources override earlier ones):

1. Built-in defaults
2. YAML config file (see below)
3. Environment variables

### Environment Variables

| Variable            | Default             | Description                                                    |
|---------------------|---------------------|----------------------------------------------------------------|
| `LISTEN_ADDR`       | `:8080`             | TCP address to listen on                                       |
| `BOOKS_DIR`         | `./books`           | Directory where EPUB/PDF files are stored                      |
| `AUTH_PASSWORD`     | *(none)*            | Login password (leave empty to disable auth)                   |
| `BACKEND`           | `fs`                | Catalog backend: `fs` (in-memory) or `sqlite` (persistent)     |
| `REFRESH_INTERVAL`  | `5m`                | How often to rescan the books directory (`0` to disable)       |
| `BACKUP_DIR`        | `{books_dir}/.backups` | Directory for nightly SQLite backups                        |
| `BACKUP_KEEP`       | `7`                 | Number of backup files to retain                               |
| `OPDS_TOKEN`        | *(derived)*         | Bearer token for OPDS reader auth (derived from password if unset) |
| `NXT_OPDS_CONFIG`   | *(search path)*     | Explicit path to config YAML file                              |

### YAML Config File

Searched automatically at `./nxt-opds.yaml` and `~/.config/nxt-opds/config.yaml`.

```yaml
listen_addr: ":8080"
books_dir: "/data/books"
auth_password: "mysecretpassword"
backend: "sqlite"
refresh_interval: "5m"
backup_keep: 7
```

## Catalog Backends

| Backend  | Storage             | Best For                                              |
|----------|---------------------|-------------------------------------------------------|
| `fs`     | `.metadata.json`    | Small libraries, no persistence required              |
| `sqlite` | `.catalog.db`       | Large libraries (fast queries, persistent metadata, backups) |

## MCP Server

nxt-opds exposes a [Model Context Protocol](https://modelcontextprotocol.io) endpoint at `/mcp` for AI agent access. Available tools:

| Tool              | Description                                    |
|-------------------|------------------------------------------------|
| `search_books`         | Search the catalog with filters (`query/author/tag/series/publisher/collection`, `unread_only`, `not_indexed`, `age_rating`/`age_rating_min`, `spice_rating` exact match, `spice_min`/`spice_max` advanced ranges) |
| `get_book`             | Get full metadata for a book                                     |
| `update_book`          | Update book metadata (tags, summary, age rating, last indexed, etc.) |
| `upload_book`          | Upload an EPUB file (base64-encoded)                             |
| `update_cover`         | Replace a book's cover image                                     |
| `list_tags`            | List all tags in the catalog (default 50, max 200)               |
| `list_authors`         | List all authors (default 100, max 500)                          |
| `list_series`          | List all series                                                  |
| `list_publishers`      | List all publishers (default 100, max 500)                       |
| `list_wishlist`        | List wishlist items                                              |
| `add_wishlist_item`    | Add an entry to the wishlist                                     |
| `delete_wishlist_item` | Remove a wishlist entry                                          |
| `list_recommendations` | List pending recommendations                                     |
| `list_to_read`         | List the current user's ordered to-read pile                     |
| `add_to_read`          | Append a book to the to-read pile                                |
| `remove_to_read`       | Remove a book from the to-read pile                              |
| `reorder_to_read`      | Reorder the to-read pile by book IDs                             |

Authentication uses the same OPDS bearer token (`?token=<value>` or `Authorization: Bearer` header).

## Librarian

nxt-opds has no embedded LLM. The chat assistant in the UI is a thin
proxy in front of a **librarian** — a separate service you run somewhere
(typically next to a model provider). Once paired, the librarian gets
authenticated access to this server's MCP endpoint and can manipulate the
catalog on your behalf.

### Topology

```
┌──────────────┐   POST /api/ai/chat       ┌──────────────┐
│ Vue chat box │ ────────────────────────► │   nxt-opds   │
│              │ ◄──── {reply,error} JSON ─│  (this app)  │
└──────────────┘                           └──────┬───────┘
                                                  │
                  Bearer chat_secret              │  Bearer chat_secret
                  POST ${librarian_url}/chat ◄────┤
                                                  │
                  X-Signature HMAC(webhook_secret)│  X-NxtOpds-Event book.*
                  POST ${librarian_url}/webhooks/ │
                       ${instance}/book-event ◄───┘
```

Two parallel channels: the chat relay is request-driven (a user types →
the librarian runs its agent loop → it returns a single JSON
`{"reply":"...","error":"..."}` once the loop terminates; nxt-opds
forwards the body bytes verbatim, but injects a `user_token` field
holding the connected user's per-user OPDS/MCP token so the librarian's
MCP calls — `list_to_read`, `list_wishlist`, `list_recommendations`,
`search_books(unread:true)` — are scoped to the active user instead of
falling back to the instance token); the webhook fan-out is push-driven
(every `book.created` / `book.updated` / `book.deleted` / `book.read`
event also hits the librarian alongside any admin-configured webhooks).

### Pairing flow

1. An admin opens **Administration → Librarian → Associer un librarian** in
   the web UI. nxt-opds mints a single-use code (`XXXX-XXXX`, ~40 bits
   entropy, 10-minute TTL) and shows it in a modal.
2. On the librarian host, run the bundled CLI:
   ```bash
   librarian pair --nxt-opds https://books.example --code ABCD-1234
   ```
3. The CLI hits `POST /api/librarian/pair` with the code. nxt-opds mints
   two 32-byte hex secrets, persists the association, invalidates the
   code, and returns `{mcp_url, mcp_token, chat_secret, webhook_secret,
   instance, label}`. The librarian stores those.
4. After pairing, `/api/config` flips `librarianEnabled` to `true` and the
   chat box appears in the SPA. Book lifecycle events start fanning out
   to the librarian automatically.

To rotate the secrets the librarian POSTs `/api/librarian/rotate` with
its current `X-Librarian-Chat-Secret`; to unpair from the librarian side
it POSTs `/api/librarian/forget`; from the admin UI a **Désappairer**
button hits `DELETE /api/librarian/association`, which also best-effort
notifies `${librarian_url}/instances/{instance}/forget` so both sides
clean up.

When the librarian process restarts (and especially when it has moved
hostnames or ports — e.g. a container relocation), it POSTs
`/api/librarian/announce` with `{"librarian_url":"http://..."}` and its
`X-Librarian-Chat-Secret`. nxt-opds updates the stored `LibrarianURL`
(preserving the instance and both secrets) so subsequent webhook fan-out
and `/api/ai/chat` relays target the new address without any operator
intervention.

While `librarian serve` is running it POSTs `/api/librarian/heartbeat`
roughly once a minute. nxt-opds stamps a `LastSeenAt` field on the
association without advancing `UpdatedAt` (a heartbeat is not a
mutation). The admin UI surfaces this as a coloured dot on the Librarian
card — green under 3 minutes since the last beat, amber 3–15 min, red
past 15 min, grey when no heartbeat has ever been received.

### Librarian-related endpoints

| Path                                | Auth                              | Purpose                                              |
|-------------------------------------|-----------------------------------|------------------------------------------------------|
| `POST /api/librarian/pairing-code`  | admin session cookie              | Mint a one-time `XXXX-XXXX` pairing code (10-min TTL) |
| `POST /api/librarian/pair`          | body `code`                       | Exchange the code for secrets + mcp_url + mcp_token  |
| `GET  /api/librarian/association`   | admin session cookie              | View paired librarian (URL + instance only — no secrets); 204 when unpaired |
| `DELETE /api/librarian/association` | admin session cookie              | Local unpair + best-effort POST `…/instances/{instance}/forget` |
| `POST /api/librarian/rotate`        | `X-Librarian-Chat-Secret` header  | Roll both secrets without unpair/re-pair             |
| `POST /api/librarian/forget`        | `X-Librarian-Chat-Secret` header  | Inbound unpair from the librarian side; idempotent   |
| `POST /api/librarian/announce`      | `X-Librarian-Chat-Secret` header  | Realign `LibrarianURL` when the librarian moves host/port; preserves instance + secrets + `CreatedAt` |
| `POST /api/librarian/heartbeat`     | `X-Librarian-Chat-Secret` header  | Liveness ping (~60 s); stamps `LastSeenAt` only, does not advance `UpdatedAt` |
| `POST /api/ai/chat`                 | session cookie                    | Body-relay to `${librarian_url}/chat` (JSON `{reply,error}` upstream); 404 when unpaired |

The pairing-code mint and association view/delete deliberately require a
real **session cookie** so a leaked OPDS reader URL or shared token
cannot start, view or break a pairing.

## API Endpoints

### Web UI
| Path       | Description |
|------------|-------------|
| `GET /`    | Web UI (Vue 3 frontend) |

### Authentication
| Path          | Description                  |
|---------------|------------------------------|
| `GET /login`  | Login page                   |
| `POST /login` | Submit login form             |
| `POST /logout`| Log out                      |

### OPDS 1.2
| Path                               | Description                    |
|------------------------------------|--------------------------------|
| `GET /opds`                        | Root navigation feed           |
| `GET /opds/books`                  | All books (acquisition feed)   |
| `GET /opds/books/{id}`             | Single book entry              |
| `GET /opds/books/{id}/download`    | Download book file             |
| `GET /opds/search?q=...`           | Search results                 |
| `GET /opds/authors`                | Author navigation feed         |
| `GET /opds/authors/{author}`       | Books by author                |
| `GET /opds/tags`                   | Genre/tag navigation feed      |
| `GET /opds/tags/{tag}`             | Books by tag                   |
| `GET /opds/publishers`             | Publisher navigation feed      |
| `GET /opds/publishers/{publisher}` | Books by publisher             |
| `GET /opds/unread`                 | Unread books feed              |
| `GET /opds/to-read`                | To-read pile feed (per user; multi-user clients pass `?user=<id>`) |
| `GET /opds/wishlist`               | Wishlist feed                  |
| `GET /opds/recommendations`        | Recommendations feed           |
| `GET /opds/spice`                  | Spice-level navigation feed (6 exact buckets 0–5 → `/opds/books?spice=N`; hidden for child profiles) |

Most book-listing feeds accept `?spice=N` (0–5, **exact** spice rating scoped to 16+/18+ titles); the older `?spice_max=N` parameter (≤N range) is still honoured for one release but will be removed. Both filters automatically exclude sub-16 titles since the spice axis is undefined for them. The OPDS v2 counterparts honour the same query parameters.

### OPDS 2.0
Same paths under `/opds/v2` (JSON format).

### REST API (Web UI)
| Path                                        | Description                        |
|---------------------------------------------|------------------------------------|
| `GET /api/books`                            | Books list (JSON, with filters)    |
| `GET /api/books/{id}`                       | Single book (JSON)                 |
| `PATCH /api/books/{id}`                     | Update book metadata               |
| `DELETE /api/books/{id}`                    | Delete a book                      |
| `POST /api/books/{id}/cover`                | Replace book cover                 |
| `PUT /api/books/{id}/read`                  | Toggle read status                 |
| `POST /api/books/{id}/recommend`            | Recommend a book to a user         |
| `DELETE /api/books/{id}/recommend/{userID}` | Remove a recommendation            |
| `POST /api/upload`                          | Upload an EPUB or PDF              |
| `GET /api/authors`                          | List all authors                   |
| `GET /api/tags`                             | List all tags                      |
| `DELETE /api/tags/{tag}`                    | Delete a tag                       |
| `GET /api/publishers`                       | List all publishers                |
| `GET /api/series`                           | List all series                    |
| `GET /api/collections`                      | List all collections               |
| `GET /api/recommendations`                  | List recommendations for current user |
| `GET /api/wishlist`                         | List wishlist items                |
| `POST /api/wishlist`                        | Add wishlist item                  |
| `PATCH /api/wishlist/{id}`                  | Update wishlist item               |
| `DELETE /api/wishlist/{id}`                 | Remove wishlist item               |
| `GET /api/to-read`                          | Current user's to-read pile        |
| `POST /api/to-read`                         | Add a book to the to-read pile     |
| `PUT /api/to-read/reorder`                  | Reorder the to-read pile           |
| `DELETE /api/to-read/{bookId}`              | Remove a book from the to-read pile |
| `GET /api/stats`                            | Reading statistics (per user)      |
| `GET /api/users`                            | List users (admin)                 |
| `POST /api/users`                           | Create a user (admin)              |
| `PATCH /api/users/{id}`                     | Update a user (admin)              |
| `DELETE /api/users/{id}`                    | Delete a user (admin)              |
| `GET /api/config`                           | App config for the frontend        |
| `POST /api/refresh`                         | Trigger catalog rescan             |
| `GET /api/update/check`                     | Check for a new release on GitHub  |
| `POST /api/update/apply`                    | Download and apply the new binary  |
| `POST /api/restart`                         | Restart the server process         |
| `POST /mcp`                                 | MCP server endpoint                |
| `POST /api/librarian/pairing-code`          | Mint a one-time pairing code (admin) |
| `POST /api/librarian/pair`                  | Exchange a pairing code for secrets |
| `GET  /api/librarian/association`           | View paired librarian (admin)      |
| `DELETE /api/librarian/association`         | Unpair the librarian (admin)       |
| `POST /api/librarian/rotate`                | Roll librarian secrets (librarian-side) |
| `POST /api/librarian/forget`                | Inbound unpair (librarian-side)    |
| `POST /api/librarian/announce`              | Realign LibrarianURL on librarian restart |
| `POST /api/librarian/heartbeat`             | Liveness ping from `librarian serve` (~60 s) |
| `POST /api/ai/chat`                         | Chat relay to the paired librarian (JSON `{reply,error}`) |
| `GET /api/webhooks`                         | List webhooks (admin)              |
| `POST /api/webhooks`                        | Create a webhook (admin)           |
| `PATCH /api/webhooks/{id}`                  | Update a webhook (admin)           |
| `DELETE /api/webhooks/{id}`                 | Delete a webhook (admin)           |
| `POST /api/webhooks/{id}/test`              | Send a test ping to a webhook      |
| `GET /health`                               | Health check                       |
| `GET /covers/{id}`                          | Book cover image                   |

## Webhooks

Admins can register outbound HTTP callbacks from the admin page (`#/admin`).
Each webhook subscribes to one or more events and receives a JSON POST whenever
the matching event happens on the catalog.

**Supported events**: `book.created`, `book.updated`, `book.deleted`, `book.read`.
Leaving the subscription list empty subscribes the webhook to every event.

**Request headers**

| Header                  | Value                                                    |
|-------------------------|----------------------------------------------------------|
| `Content-Type`          | `application/json`                                       |
| `User-Agent`            | `nxt-opds/webhook`                                       |
| `X-NxtOpds-Event`       | The event name (e.g. `book.created`)                     |
| `X-Signature`           | `sha256=<hex>` HMAC of the body, only if a secret is set |

**Envelope**

```json
{
  "event": "book.created",
  "timestamp": "2026-05-15T18:42:01Z",
  "data": { ... event-specific payload ... }
}
```

`book.created` / `book.updated` / `book.deleted` payloads carry the public book
fields:

```json
{
  "id": "a1b2c3d4...",
  "title": "Le Petit Prince",
  "authors": ["Antoine de Saint-Exupéry"],
  "tags": ["jeunesse", "classique"],
  "summary": "...",
  "language": "fr",
  "publisher": "Gallimard",
  "series": "",
  "seriesIndex": "",
  "seriesTotal": "",
  "collection": "Folio",
  "collectionIndex": "",
  "rating": 5,
  "ageRating": 6,
  "spiceRating": 0,
  "isRead": false,
  "coverUrl": "/covers/a1b2c3d4",
  "downloadUrl": "/opds/books/a1b2c3d4/download",
  "fileType": "application/epub+zip",
  "fileSize": 1843200
}
```

`book.read` payload:

```json
{
  "bookId": "a1b2c3d4...",
  "title": "Le Petit Prince",
  "isRead": true,
  "userId": "u-uuid",
  "userName": "Alice"
}
```

Test deliveries (via the **Tester** button) use `event: "test"` with a simple
`{ "message": "..." }` payload, bypassing the enabled flag and subscription
list so admins can validate a fresh receiver. The last delivery status is
recorded on the webhook row and shown back in the admin UI.

### Librarian fan-out

When a librarian is paired, the same envelope is also POSTed to
`${librarian_url}/webhooks/${librarian_instance}/book-event` with
`X-NxtOpds-Event` and an `X-Signature` HMAC computed using the
`webhook_secret` minted at pairing time. This target is **not** part of
the admin webhooks list — it is wired by the pairing handshake and is
hidden from the admin UI on purpose. To stop it, unpair the librarian.

## Project Structure

```
.
├── main.go
├── Dockerfile
├── docker-compose.yml
├── nxt-opds.service          # systemd unit file
├── internal/
│   ├── catalog/              # Catalog interface and core data types
│   ├── config/               # YAML config loading
│   ├── epub/                 # EPUB/PDF metadata extraction
│   ├── mcp/                  # MCP server (AI agent access)
│   ├── opds/                 # OPDS/Atom feed types and XML serialisation
│   ├── server/               # HTTP server, routing, handlers, auth
│   ├── updater/              # Auto-update (GitHub releases)
│   ├── webhooks/             # Async outbound webhook dispatcher (HMAC)
│   └── backend/
│       ├── fs/               # In-memory filesystem backend
│       └── sqlite/           # SQLite-backed persistent backend
└── web/
    ├── index.html            # Vue 3 + Tailwind CSS frontend (embedded)
    └── embed.go              # go:embed directive
```

## License

MIT
