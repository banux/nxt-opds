// Package updater handles automatic binary updates by fetching new releases
// from GitHub and replacing the running executable.
package updater

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const githubRepo = "banux/nxt-opds"

// Release represents a GitHub release.
type Release struct {
	TagName string  `json:"tag_name"`
	HTMLURL string  `json:"html_url"`
	Body    string  `json:"body"`
	Assets  []Asset `json:"assets"`
}

// Asset is a file attached to a GitHub release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// CheckLatest fetches the latest release from GitHub and returns it.
// Returns an error if the GitHub API call fails.
func CheckLatest() (*Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("check update: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("check update: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("check update: GitHub API returned %d", resp.StatusCode)
	}
	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("check update: parse response: %w", err)
	}
	return &release, nil
}

// AssetName returns the expected binary asset name for the current platform
// and the given release tag.
func AssetName(tag string) string {
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	return fmt.Sprintf("nxt-opds-%s-%s-%s%s", tag, runtime.GOOS, runtime.GOARCH, suffix)
}

// Apply downloads the binary from the given release and atomically replaces
// the current executable. The process must be restarted by the caller after
// a successful apply.
func Apply(release *Release) error {
	name := AssetName(release.TagName)
	var downloadURL string
	for _, asset := range release.Assets {
		if asset.Name == name {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		return fmt.Errorf("no asset found for platform %s/%s (expected %s)", runtime.GOOS, runtime.GOARCH, name)
	}

	// Resolve the current executable's real path (follow symlinks).
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("resolve executable symlink: %w", err)
	}

	// Download to a temp file in the same directory as the binary so that
	// os.Rename is guaranteed to be an atomic same-filesystem move.
	dir := filepath.Dir(exePath)
	tmp, err := os.CreateTemp(dir, ".nxt-opds-update-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	// Ensure cleanup on error (the defer below closes the file; removal only
	// happens if we return early, so we track success via a flag).
	success := false
	defer func() {
		tmp.Close()
		if !success {
			os.Remove(tmpPath)
		}
	}()

	// Download the new binary.
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("download update: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download update: server returned %d", resp.StatusCode)
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		return fmt.Errorf("write update: %w", err)
	}
	tmp.Close()

	// Make the new binary executable.
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return fmt.Errorf("chmod update: %w", err)
	}

	// Atomically replace the running binary.
	if err := os.Rename(tmpPath, exePath); err != nil {
		return fmt.Errorf("replace binary: %w", err)
	}

	success = true
	return nil
}
