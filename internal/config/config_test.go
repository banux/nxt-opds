package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/banux/nxt-opds/internal/config"
)

func TestDefault_Values(t *testing.T) {
	cfg := config.Default()
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr: got %q, want :8080", cfg.ListenAddr)
	}
	if cfg.BooksDir != "./books" {
		t.Errorf("BooksDir: got %q, want ./books", cfg.BooksDir)
	}
	if cfg.Password != "" {
		t.Errorf("Password: got %q, want empty", cfg.Password)
	}
}

func TestLoad_EmptyPath_UsesDefaults(t *testing.T) {
	// Ensure relevant env vars are unset for this test.
	t.Setenv("LISTEN_ADDR", "")
	t.Setenv("BOOKS_DIR", "")
	t.Setenv("AUTH_PASSWORD", "")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load(\"\") error: %v", err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr: got %q, want :8080", cfg.ListenAddr)
	}
	if cfg.BooksDir != "./books" {
		t.Errorf("BooksDir: got %q, want ./books", cfg.BooksDir)
	}
}

func TestLoad_FromYAMLFile(t *testing.T) {
	yaml := `
listen_addr: ":9090"
books_dir: "/var/lib/books"
auth_password: "topsecret"
`
	path := writeTemp(t, "config.yaml", yaml)

	t.Setenv("LISTEN_ADDR", "")
	t.Setenv("BOOKS_DIR", "")
	t.Setenv("AUTH_PASSWORD", "")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.ListenAddr != ":9090" {
		t.Errorf("ListenAddr: got %q, want :9090", cfg.ListenAddr)
	}
	if cfg.BooksDir != "/var/lib/books" {
		t.Errorf("BooksDir: got %q, want /var/lib/books", cfg.BooksDir)
	}
	if cfg.Password != "topsecret" {
		t.Errorf("Password: got %q, want topsecret", cfg.Password)
	}
}

func TestLoad_PartialYAML_UsesDefaults(t *testing.T) {
	// Only override one field; the others should stay at defaults.
	yaml := `listen_addr: ":7777"`
	path := writeTemp(t, "partial.yaml", yaml)

	t.Setenv("LISTEN_ADDR", "")
	t.Setenv("BOOKS_DIR", "")
	t.Setenv("AUTH_PASSWORD", "")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.ListenAddr != ":7777" {
		t.Errorf("ListenAddr: got %q, want :7777", cfg.ListenAddr)
	}
	if cfg.BooksDir != "./books" {
		t.Errorf("BooksDir: got %q, want ./books (default)", cfg.BooksDir)
	}
}

func TestLoad_EnvVarsOverrideFile(t *testing.T) {
	yaml := `
listen_addr: ":9090"
books_dir: "/file/books"
auth_password: "filepass"
`
	path := writeTemp(t, "config.yaml", yaml)

	// Environment variables should win over file values.
	t.Setenv("LISTEN_ADDR", ":5555")
	t.Setenv("BOOKS_DIR", "/env/books")
	t.Setenv("AUTH_PASSWORD", "envpass")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.ListenAddr != ":5555" {
		t.Errorf("ListenAddr: got %q, want :5555 (from env)", cfg.ListenAddr)
	}
	if cfg.BooksDir != "/env/books" {
		t.Errorf("BooksDir: got %q, want /env/books (from env)", cfg.BooksDir)
	}
	if cfg.Password != "envpass" {
		t.Errorf("Password: got %q, want envpass (from env)", cfg.Password)
	}
}

func TestLoad_EnvVarsOverrideDefaults(t *testing.T) {
	t.Setenv("LISTEN_ADDR", ":3000")
	t.Setenv("BOOKS_DIR", "/custom/books")
	t.Setenv("AUTH_PASSWORD", "mypass")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.ListenAddr != ":3000" {
		t.Errorf("ListenAddr: got %q, want :3000", cfg.ListenAddr)
	}
	if cfg.BooksDir != "/custom/books" {
		t.Errorf("BooksDir: got %q, want /custom/books", cfg.BooksDir)
	}
	if cfg.Password != "mypass" {
		t.Errorf("Password: got %q, want mypass", cfg.Password)
	}
}

func TestLoad_NonexistentFile_ReturnsError(t *testing.T) {
	_, err := config.Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Error("expected error for nonexistent config file, got nil")
	}
}

func TestLoad_InvalidYAML_ReturnsError(t *testing.T) {
	path := writeTemp(t, "bad.yaml", "{ invalid yaml: [")
	_, err := config.Load(path)
	if err == nil {
		t.Error("expected error for invalid YAML, got nil")
	}
}

func TestFindConfigFile_EnvVar(t *testing.T) {
	path := writeTemp(t, "explicit.yaml", "listen_addr: \":1234\"")
	t.Setenv("NXT_OPDS_CONFIG", path)
	t.Setenv("NXT_OPDS_CONFIG", path) // ensure it's set

	found := config.FindConfigFile()
	if found != path {
		t.Errorf("FindConfigFile: got %q, want %q", found, path)
	}
}

func TestFindConfigFile_NoFile_ReturnsEmpty(t *testing.T) {
	// Ensure no env var and no local file interferes.
	t.Setenv("NXT_OPDS_CONFIG", "")

	// Run from a fresh temp directory so there's no nxt-opds.yaml nearby.
	orig, _ := os.Getwd()
	dir := t.TempDir()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(orig) }()

	found := config.FindConfigFile()
	// We can't guarantee there's no ~/.config/nxt-opds/config.yaml on the
	// test machine, so only verify the env-var and local-file cases don't fire.
	if found == "nxt-opds.yaml" {
		t.Error("should not return local nxt-opds.yaml from temp dir")
	}
}

// ---- refresh_interval config ----

func TestDefault_RefreshInterval(t *testing.T) {
	cfg := config.Default()
	if cfg.RefreshInterval != 5*time.Minute {
		t.Errorf("default RefreshInterval: got %v, want 5m", cfg.RefreshInterval)
	}
	if cfg.RefreshIntervalStr != "5m" {
		t.Errorf("default RefreshIntervalStr: got %q, want 5m", cfg.RefreshIntervalStr)
	}
}

func TestLoad_RefreshInterval_FromYAML(t *testing.T) {
	yaml := `refresh_interval: "10m"`
	path := writeTemp(t, "refresh.yaml", yaml)
	t.Setenv("REFRESH_INTERVAL", "")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.RefreshInterval != 10*time.Minute {
		t.Errorf("RefreshInterval: got %v, want 10m", cfg.RefreshInterval)
	}
}

func TestLoad_RefreshInterval_DisabledWithZero(t *testing.T) {
	yaml := `refresh_interval: "0"`
	path := writeTemp(t, "refresh_zero.yaml", yaml)
	t.Setenv("REFRESH_INTERVAL", "")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.RefreshInterval != 0 {
		t.Errorf("RefreshInterval with '0' string: got %v, want 0 (disabled)", cfg.RefreshInterval)
	}
}

func TestLoad_RefreshInterval_FromEnv(t *testing.T) {
	t.Setenv("REFRESH_INTERVAL", "30s")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.RefreshInterval != 30*time.Second {
		t.Errorf("RefreshInterval from env: got %v, want 30s", cfg.RefreshInterval)
	}
}

func TestLoad_RefreshInterval_EnvDisable(t *testing.T) {
	t.Setenv("REFRESH_INTERVAL", "0")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.RefreshInterval != 0 {
		t.Errorf("RefreshInterval with REFRESH_INTERVAL=0: got %v, want 0 (disabled)", cfg.RefreshInterval)
	}
}

func TestLoad_RefreshInterval_InvalidString_KeepsDefault(t *testing.T) {
	yaml := `refresh_interval: "not-a-duration"`
	path := writeTemp(t, "refresh_bad.yaml", yaml)
	t.Setenv("REFRESH_INTERVAL", "")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	// Invalid duration string is silently ignored; default (5m) is preserved.
	if cfg.RefreshInterval != 5*time.Minute {
		t.Errorf("RefreshInterval with invalid string: got %v, want 5m (preserved default)", cfg.RefreshInterval)
	}
}

// writeTemp creates a temporary file with the given content and returns its path.
func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writeTemp: %v", err)
	}
	return path
}
