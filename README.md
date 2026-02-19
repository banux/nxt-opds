# nxt-opds

A lightweight OPDS 1.2 catalog server for personal eBook libraries, written in Go.

[OPDS](https://specs.opds.io/opds-1.2) (Open Publication Distribution System) is an Atom-based catalog format for discovering and distributing digital publications. Point any OPDS reader (Kybook, Moon+ Reader, Calibre, etc.) at `/opds` to browse your library.

## Features

- OPDS 1.2 compliant navigation and acquisition feeds
- Vue 3 + Tailwind CSS web UI (no build step) with Feedbooks-style book grid
- Browse by author or genre/tag; full-text search
- EPUB upload with instant metadata extraction (title, author, cover, series, tags)
- Editable book metadata (title, authors, tags, series, read status)
- Password-protected login (session cookie + Basic Auth fallback for OPDS readers)
- Two catalog backends: in-memory (`fs`) or persistent SQLite (`sqlite`)
- Single static binary with embedded frontend

## Quick Start

### Binary

```bash
# Build (Go 1.24+ required)
go build -o nxt-opds .

# Run (books stored in ./books, SQLite backend)
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
  nxt-opds
```

## Configuration

Configuration is loaded in this order (later sources override earlier ones):

1. Built-in defaults
2. YAML config file (see below)
3. Environment variables

### Environment Variables

| Variable         | Default        | Description                                  |
|------------------|----------------|----------------------------------------------|
| `LISTEN_ADDR`    | `:8080`        | TCP address to listen on                     |
| `BOOKS_DIR`      | `./books`      | Directory where EPUB/PDF files are stored    |
| `AUTH_PASSWORD`  | *(none)*       | Login password (leave empty to disable auth) |
| `BACKEND`        | `fs`           | Catalog backend: `fs` (in-memory) or `sqlite`|
| `NXT_OPDS_CONFIG`| *(search path)*| Explicit path to config YAML file            |

### YAML Config File

Searched automatically at `./nxt-opds.yaml` and `~/.config/nxt-opds/config.yaml`.

```yaml
listen_addr: ":8080"
books_dir: "/data/books"
auth_password: "mysecretpassword"
backend: "sqlite"
```

## Catalog Backends

| Backend  | Storage          | Best For              |
|----------|------------------|-----------------------|
| `fs`     | `.metadata.json` | Small libraries       |
| `sqlite` | `.catalog.db`    | Large libraries (fast queries, persistent metadata) |

## API Endpoints

| Path                          | Description                    |
|-------------------------------|--------------------------------|
| `GET /`                       | Web UI                         |
| `GET /opds`                   | Root navigation feed           |
| `GET /opds/books`             | All books (acquisition feed)   |
| `GET /opds/books/{id}`        | Single book entry              |
| `GET /opds/search?q=...`      | Search results                 |
| `GET /opds/authors`           | Author navigation feed         |
| `GET /opds/authors/{author}`  | Books by author                |
| `GET /opds/tags`              | Genre navigation feed          |
| `GET /opds/tags/{tag}`        | Books by genre                 |
| `GET /opds/books/{id}/download` | Download book file           |
| `GET /covers/{id}`            | Book cover image               |
| `GET /api/books`              | Books list (JSON, for Web UI)  |
| `POST /api/upload`            | Upload an EPUB or PDF          |
| `PATCH /api/books/{id}`       | Update book metadata           |
| `GET /health`                 | Health check                   |
| `GET /login`                  | Login page                     |
| `POST /login`                 | Submit login form              |
| `POST /logout`                | Log out                        |

## Project Structure

```
.
├── main.go
├── Dockerfile
├── docker-compose.yml
├── internal/
│   ├── catalog/        # Catalog interface and core data types
│   ├── config/         # YAML config loading
│   ├── epub/           # EPUB/PDF metadata extraction (shared)
│   ├── opds/           # OPDS/Atom feed types and XML serialization
│   ├── server/         # HTTP server, routing, handlers, auth
│   └── backend/
│       ├── fs/         # In-memory filesystem backend
│       └── sqlite/     # SQLite-backed persistent backend
└── web/
    └── index.html      # Vue 3 + Tailwind CSS frontend (embedded)
```

## License

MIT
