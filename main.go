package main

import (
	"log"
	"net/http"
	"os"

	"github.com/banux/nxt-opds/internal/config"

	fsbackend "github.com/banux/nxt-opds/internal/backend/fs"
	"github.com/banux/nxt-opds/internal/server"
	"github.com/banux/nxt-opds/web"
)

func main() {
	// Load configuration: YAML file (if found) merged with env var overrides.
	cfgPath := config.FindConfigFile()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}
	if cfgPath != "" {
		log.Printf("loaded configuration from %q", cfgPath)
	}

	if cfg.Password == "" {
		log.Printf("WARNING: auth_password is not set – authentication is disabled")
	}

	// Ensure the books directory exists
	if err := os.MkdirAll(cfg.BooksDir, 0755); err != nil {
		log.Fatalf("cannot create books directory %q: %v", cfg.BooksDir, err)
	}

	cat, err := fsbackend.New(cfg.BooksDir)
	if err != nil {
		log.Fatalf("catalog backend error: %v", err)
	}
	log.Printf("catalog loaded from %q", cfg.BooksDir)

	opts := server.Options{
		Password: cfg.Password,
		StaticFS: web.FS,
	}
	srv := server.New(cat, opts)

	log.Printf("nxt-opds starting on %s", cfg.ListenAddr)
	log.Printf("Web UI available at http://localhost%s/", cfg.ListenAddr)
	if err := http.ListenAndServe(cfg.ListenAddr, srv); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
