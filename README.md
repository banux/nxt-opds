# nxt-opds

A lightweight, fast OPDS 1.2 catalog server written in Go.

[OPDS](https://specs.opds.io/opds-1.2) (Open Publication Distribution System) is an Atom-based catalog format for discovering and distributing digital publications (eBooks, comics, etc.).

## Features

- OPDS 1.2 compliant navigation and acquisition feeds
- Browse by author, genre/tag
- Full-text search via OpenSearch
- Health-check endpoint
- Pluggable catalog backend

## Quick Start

```bash
go build ./...
LISTEN_ADDR=:8080 ./nxt-opds
```

Then point any OPDS-compatible reader at `http://localhost:8080/opds`.

## Project Structure

```
.
├── main.go                     # Entry point
├── internal/
│   ├── opds/
│   │   └── feed.go             # OPDS/Atom feed types and XML serialization
│   ├── catalog/
│   │   └── catalog.go          # Catalog interface and core data types
│   └── server/
│       ├── server.go           # HTTP server and route registration
│       └── handlers.go         # HTTP request handlers
└── go.mod
```

## API Endpoints

| Path | Description |
|------|-------------|
| `GET /opds` | Root navigation feed |
| `GET /opds/books` | All books (acquisition feed) |
| `GET /opds/books/{id}` | Single book entry |
| `GET /opds/search?q=...` | Search results |
| `GET /opds/authors` | Author navigation feed |
| `GET /opds/authors/{author}` | Books by author |
| `GET /opds/tags` | Genre navigation feed |
| `GET /opds/tags/{tag}` | Books by genre |
| `GET /opds/opensearch.xml` | OpenSearch description |
| `GET /health` | Health check |

## Configuration

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `LISTEN_ADDR` | `:8080` | TCP address to listen on |

## License

MIT
