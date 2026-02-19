package main

import (
	"log"
	"net/http"
	"os"

	fsbackend "github.com/banux/nxt-opds/internal/backend/fs"
	"github.com/banux/nxt-opds/internal/server"
	"github.com/banux/nxt-opds/web"
)

func main() {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	booksDir := os.Getenv("BOOKS_DIR")
	if booksDir == "" {
		booksDir = "./books"
	}

	password := os.Getenv("AUTH_PASSWORD")
	if password == "" {
		log.Printf("WARNING: AUTH_PASSWORD is not set – authentication is disabled")
	}

	// Ensure the books directory exists
	if err := os.MkdirAll(booksDir, 0755); err != nil {
		log.Fatalf("cannot create books directory %q: %v", booksDir, err)
	}

	cat, err := fsbackend.New(booksDir)
	if err != nil {
		log.Fatalf("catalog backend error: %v", err)
	}
	log.Printf("catalog loaded from %q", booksDir)

	opts := server.Options{
		Password: password,
		StaticFS: web.FS,
	}
	srv := server.New(cat, opts)

	log.Printf("nxt-opds starting on %s", addr)
	log.Printf("Web UI available at http://localhost%s/", addr)
	if err := http.ListenAndServe(addr, srv); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
