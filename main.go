package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/banux/nxt-opds/internal/config"

	fsbackend "github.com/banux/nxt-opds/internal/backend/fs"
	sqlitebackend "github.com/banux/nxt-opds/internal/backend/sqlite"
	"github.com/banux/nxt-opds/internal/catalog"
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

	// Ensure the books directory exists.
	if err := os.MkdirAll(cfg.BooksDir, 0755); err != nil {
		log.Fatalf("cannot create books directory %q: %v", cfg.BooksDir, err)
	}

	var cat catalog.Catalog
	switch cfg.Backend {
	case "sqlite":
		b, err := sqlitebackend.New(cfg.BooksDir)
		if err != nil {
			log.Fatalf("sqlite catalog backend error: %v", err)
		}
		cat = b
		log.Printf("using SQLite catalog backend (%s/.catalog.db)", cfg.BooksDir)
	default: // "fs" or unset
		b, err := fsbackend.New(cfg.BooksDir)
		if err != nil {
			log.Fatalf("catalog backend error: %v", err)
		}
		cat = b
		log.Printf("using in-memory (fs) catalog backend")
	}
	log.Printf("catalog loaded from %q", cfg.BooksDir)

	// Start background catalog refresh if the backend supports it and an
	// interval is configured (> 0).
	if r, ok := cat.(catalog.Refresher); ok && cfg.RefreshInterval > 0 {
		log.Printf("background catalog refresh enabled (interval: %s)", cfg.RefreshInterval)
		go func() {
			ticker := time.NewTicker(cfg.RefreshInterval)
			defer ticker.Stop()
			for range ticker.C {
				if err := r.Refresh(); err != nil {
					log.Printf("background catalog refresh error: %v", err)
				} else {
					log.Printf("catalog refreshed")
				}
			}
		}()
	}

	// Start nightly backup goroutine if the backend supports it.
	if bu, ok := cat.(catalog.Backupper); ok {
		backupDir := cfg.BackupDir
		if backupDir == "" {
			backupDir = filepath.Join(cfg.BooksDir, ".backups")
		}
		keep := cfg.BackupKeep
		log.Printf("nightly database backup enabled (dir: %s, keep: %d)", backupDir, keep)
		go runNightlyBackup(bu, backupDir, keep)
	}

	opts := server.Options{
		Password:  cfg.Password,
		OPDSToken: cfg.OPDSToken,
		StaticFS:  web.FS,
	}
	srv := server.New(cat, opts)

	log.Printf("nxt-opds starting on %s", cfg.ListenAddr)
	log.Printf("Web UI available at http://localhost%s/", cfg.ListenAddr)
	if cfg.OPDSToken != "" {
		log.Printf("OPDS feed URL (for reader apps): http://localhost%s/opds?token=%s", cfg.ListenAddr, cfg.OPDSToken)
	}
	if err := http.ListenAndServe(cfg.ListenAddr, srv); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// runNightlyBackup sleeps until the next local midnight, then calls
// bu.Backup every 24 hours.  It is intended to run in a goroutine.
func runNightlyBackup(bu catalog.Backupper, backupDir string, keep int) {
	for {
		now := time.Now()
		// Next midnight in local time.
		next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
		time.Sleep(time.Until(next))

		path, err := bu.Backup(backupDir, keep)
		if err != nil {
			log.Printf("nightly backup error: %v", err)
		} else {
			log.Printf("nightly backup created: %s", path)
		}
	}
}
