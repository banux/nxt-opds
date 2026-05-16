package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/banux/nxt-opds/internal/config"

	fsbackend "github.com/banux/nxt-opds/internal/backend/fs"
	sqlitebackend "github.com/banux/nxt-opds/internal/backend/sqlite"
	"github.com/banux/nxt-opds/internal/catalog"
	"github.com/banux/nxt-opds/internal/server"
	"github.com/banux/nxt-opds/web"
)

// version is set at build time via -ldflags "-X main.version=v1.x.y".
// It defaults to "dev" when the binary is not built with a version tag.
var version = "dev"

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

	// When auth is enabled but no explicit OPDS token is configured, load (or
	// generate and persist) a random token from {books_dir}/.opds_token.
	// Replaces the previous SHA-256(password) derivation, which leaked the
	// password to anyone who observed a token.
	if cfg.OPDSToken == "" && cfg.Password != "" {
		tok, err := config.LoadOrCreateOPDSToken(cfg.BooksDir)
		if err != nil {
			log.Fatalf("OPDS token error: %v", err)
		}
		cfg.OPDSToken = tok
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

	opts := server.Options{
		Password:  cfg.Password,
		OPDSToken: cfg.OPDSToken,
		StaticFS:  web.FS,
		Version:   version,
		Debug:     cfg.Debug,
	}
	if cfg.Debug {
		log.Printf("DEBUG mode enabled — verbose auth and /mcp logging")
	}
	srv := server.New(cat, opts)

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

	log.Printf("nxt-opds %s starting on %s", version, cfg.ListenAddr)
	log.Printf("Web UI available at http://localhost%s/", cfg.ListenAddr)
	if cfg.OPDSToken != "" {
		// Never log the bearer token in plaintext: an operator with shell or
		// log access should already know it from the .opds_token file or env.
		// We log only a short fingerprint so two log lines can be correlated.
		fp := config.TokenFingerprint(cfg.OPDSToken)
		log.Printf("OPDS feed URL ready at http://localhost%s/opds (token fingerprint: %s — see %s/.opds_token)",
			cfg.ListenAddr, fp, cfg.BooksDir)
		log.Printf("MCP endpoint ready at http://localhost%s/mcp (Bearer token fingerprint: %s)",
			cfg.ListenAddr, fp)
	}
	httpServer := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: srv,
		// ReadHeaderTimeout caps how long the server will wait for the request
		// headers to arrive — primary defence against slowloris connections
		// that dribble bytes to keep the goroutine alive.
		ReadHeaderTimeout: 10 * time.Second,
		// IdleTimeout is the maximum time keep-alive connections sit idle.
		IdleTimeout: 120 * time.Second,
		// No ReadTimeout / WriteTimeout: book uploads and downloads can take
		// minutes on slow connections.  ReadHeaderTimeout already protects the
		// header-parse phase.
	}

	// Catch SIGINT / SIGTERM and trigger a graceful shutdown so in-flight
	// uploads, downloads and SQL writes get a chance to finish.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	case <-ctx.Done():
		log.Printf("shutdown signal received, draining for up to 30s...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("graceful shutdown failed: %v", err)
		} else {
			log.Printf("server stopped cleanly")
		}
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
