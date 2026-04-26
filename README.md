# nxt-opds

A lightweight personal eBook library server written in Go, with an OPDS catalog and a Vue 3 web UI.

[OPDS](https://specs.opds.io/opds-1.2) (Open Publication Distribution System) is a catalog format for discovering and distributing digital publications. Point any OPDS reader (Kybook, Moon+ Reader, Calibre, etc.) at `/opds` to browse your library.

## Features

- **OPDS 1.2 and OPDS 2.0** compliant catalog feeds (navigation + acquisition)
- **Vue 3 + Tailwind CSS** web UI (no build step) with Feedbooks-style book grid
- **EPUB upload** with instant metadata extraction (title, authors, cover, series, tags, publisher, language)
- **Editable metadata** — title, authors, tags, series, collection, publisher, language, age rating, cover
- **Age classification** — multi-select filter chips (?, 3+, 6+, 10+, 12+, 16+, 18+); per-child-profile max-age enforcement
- **Multi-user support** — per-user read status, child profiles, user-coloured read banners
- **To-read pile** — personal ordered reading queue, exposed in OPDS feeds and the MCP server
- **Reading statistics** — per-user dashboard (totals, ratings, top authors/tags/series, 12-month chart)
- **Wishlist** — personal reading wish list, exposed in OPDS feeds
- **Recommendations** — send a book recommendation to another user
- **Integrated EPUB reader** with prev/next book navigation and swipe/keyboard support
- **AI assistant** (Ollama) — metadata enrichment via chat UI
- **MCP server** — AI agent access to the catalog over the Model Context Protocol
- **Auto-update** — download and apply a new binary from GitHub releases in one click
- **PWA** — installable as a web app with offline service worker
- **Password-protected** login (session cookie + OPDS bearer token)
- **Two catalog backends**: in-memory (`fs`) or persistent SQLite (`sqlite`)
- **Background refresh** — automatic rescan of the books directory
- **Nightly SQLite backups** with configurable retention
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
| `OLLAMA_URL`        | `http://localhost:11434` | Ollama instance URL for the AI assistant                  |
| `OLLAMA_MODEL`      | `qwen2.5:7b`        | Ollama model to use for the AI assistant                       |
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
ollama_url: "http://localhost:11434"
ollama_model: "qwen2.5:7b"
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
| `search_books`         | Search the catalog with filters (incl. `not_indexed`)            |
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
| `POST /api/ai/chat`                         | AI assistant chat (Ollama)         |
| `POST /mcp`                                 | MCP server endpoint                |
| `GET /health`                               | Health check                       |
| `GET /covers/{id}`                          | Book cover image                   |

## Project Structure

```
.
├── main.go
├── Dockerfile
├── docker-compose.yml
├── nxt-opds.service          # systemd unit file
├── internal/
│   ├── ai/                   # Ollama AI assistant agent
│   ├── catalog/              # Catalog interface and core data types
│   ├── config/               # YAML config loading
│   ├── epub/                 # EPUB/PDF metadata extraction
│   ├── mcp/                  # MCP server (AI agent access)
│   ├── opds/                 # OPDS/Atom feed types and XML serialisation
│   ├── server/               # HTTP server, routing, handlers, auth
│   ├── updater/              # Auto-update (GitHub releases)
│   └── backend/
│       ├── fs/               # In-memory filesystem backend
│       └── sqlite/           # SQLite-backed persistent backend
└── web/
    ├── index.html            # Vue 3 + Tailwind CSS frontend (embedded)
    └── embed.go              # go:embed directive
```

## License

MIT
