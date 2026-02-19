# Ralph Agent Configuration

## Build Instructions

```bash
# Build the project
go build ./...
```

## Test Instructions

```bash
# Run tests
go test ./...
```

## Run Instructions

```bash
# Start/run the project
LISTEN_ADDR=:8080 go run main.go
```

## Notes

- Go 1.22+ required
- Depends on github.com/gorilla/mux for routing
- Run `go mod tidy` after adding dependencies
- Environment variable LISTEN_ADDR controls listen address (default :8080)
- OPDS endpoint root: /opds
