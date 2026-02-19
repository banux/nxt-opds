# Ralph Agent Configuration

## Build Instructions

```bash
# Build the project (use system Go 1.25 with correct GOROOT)
GOROOT=/usr/lib/go /usr/lib/go/bin/go build ./...
```

## Test Instructions

```bash
# Run tests
GOROOT=/usr/lib/go /usr/lib/go/bin/go test ./...
```

## Run Instructions

```bash
# Start/run the project (fs backend, default)
LISTEN_ADDR=:8080 GOROOT=/usr/lib/go /usr/lib/go/bin/go run main.go

# Start with SQLite backend
LISTEN_ADDR=:8080 BACKEND=sqlite GOROOT=/usr/lib/go /usr/lib/go/bin/go run main.go
```

## Notes

- Go 1.24+ required (go.mod specifies 1.24.0); system Go at /usr/lib/go is 1.25.7
- GOROOT must be set to /usr/lib/go (the default GOROOT points to an empty dir)
- Depends on github.com/gorilla/mux for routing, gopkg.in/yaml.v3 for config, modernc.org/sqlite for SQLite backend
- Run `GOROOT=/usr/lib/go /usr/lib/go/bin/go mod tidy` after adding dependencies
- Environment variable LISTEN_ADDR controls listen address (default :8080)
- OPDS endpoint root: /opds
- Backend selection: BACKEND=fs (default, in-memory) or BACKEND=sqlite (persistent SQLite)
