// Package config handles loading application configuration from a YAML file
// with environment variable overrides.
//
// Config file format (nxt-opds.yaml):
//
//	listen_addr: ":8080"
//	books_dir: "./books"
//	auth_password: "mysecretpassword"
//
// Configuration sources, in increasing priority order:
//  1. Built-in defaults
//  2. YAML config file (located by FindConfigFile or explicit path)
//  3. Environment variables (LISTEN_ADDR, BOOKS_DIR, AUTH_PASSWORD)
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all application configuration.
type Config struct {
	// ListenAddr is the TCP address for the HTTP server (e.g. ":8080").
	ListenAddr string `yaml:"listen_addr"`

	// BooksDir is the path to the directory where EPUB/PDF files are stored.
	BooksDir string `yaml:"books_dir"`

	// Password is the shared password for form-based authentication.
	// Leave empty to disable authentication (development/trusted-network use only).
	Password string `yaml:"auth_password"`

	// Backend selects the catalog backend implementation.
	// "fs"     – in-memory index, metadata stored in .metadata.json (default)
	// "sqlite" – SQLite-indexed backend, metadata stored in .catalog.db
	Backend string `yaml:"backend"`

	// RefreshInterval is how often the catalog automatically rescans the books
	// directory for new or removed files.  Stored as a duration string in YAML
	// (e.g. "5m", "30s", "1h").  Set to "0" to disable background refresh.
	// Parsed into RefreshInterval by Load().
	RefreshIntervalStr string `yaml:"refresh_interval"`

	// RefreshInterval is the parsed form of RefreshIntervalStr.
	// Not marshalled to/from YAML directly.
	RefreshInterval time.Duration `yaml:"-"`
}

// Default returns a Config populated with sensible defaults.
func Default() Config {
	return Config{
		ListenAddr:         ":8080",
		BooksDir:           "./books",
		Backend:            "fs",
		RefreshIntervalStr: "5m",
		RefreshInterval:    5 * time.Minute,
	}
}

// Load reads configuration from the YAML file at path (if non-empty), then
// applies environment variable overrides on top. Returns the merged Config.
// If path is empty, only defaults and environment variables are applied.
func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("read config %q: %w", path, err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config %q: %w", path, err)
		}
	}

	// Environment variables always override file values so that Docker /
	// systemd overrides still work even when a config file is present.
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("BOOKS_DIR"); v != "" {
		cfg.BooksDir = v
	}
	if v := os.Getenv("AUTH_PASSWORD"); v != "" {
		cfg.Password = v
	}
	if v := os.Getenv("BACKEND"); v != "" {
		cfg.Backend = v
	}
	if v := os.Getenv("REFRESH_INTERVAL"); v != "" {
		cfg.RefreshIntervalStr = v
	}

	// Parse the refresh interval string into a Duration.
	// An empty string or "0" disables background refresh.
	if cfg.RefreshIntervalStr != "" && cfg.RefreshIntervalStr != "0" {
		if d, err := time.ParseDuration(cfg.RefreshIntervalStr); err == nil {
			cfg.RefreshInterval = d
		}
		// Invalid strings are silently ignored; the default (5m) is preserved
		// unless the YAML or env explicitly set a valid value.
	} else {
		cfg.RefreshInterval = 0
	}

	return cfg, nil
}

// FindConfigFile returns the path to the first config file found in the
// standard search order, or "" if none is found.
//
// Search order:
//  1. NXT_OPDS_CONFIG environment variable (explicit override)
//  2. ./nxt-opds.yaml (current working directory)
//  3. ~/.config/nxt-opds/config.yaml (XDG user config)
func FindConfigFile() string {
	// 1. Explicit path via environment variable.
	if p := os.Getenv("NXT_OPDS_CONFIG"); p != "" {
		return p
	}

	// 2. Config file in the current working directory.
	if _, err := os.Stat("nxt-opds.yaml"); err == nil {
		return "nxt-opds.yaml"
	}

	// 3. XDG user config directory.
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".config", "nxt-opds", "config.yaml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	return ""
}
