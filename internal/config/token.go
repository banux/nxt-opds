package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadOrCreateOPDSToken returns the OPDS shared bearer token, persisted in
// {dir}/.opds_token.  If the file does not exist, a fresh 32-byte random token
// is generated, written to the file with 0600 permissions, and returned.
//
// This replaces the previous SHA-256(password)-based derivation which leaked
// the password to anyone who observed a token.
func LoadOrCreateOPDSToken(dir string) (string, error) {
	path := filepath.Join(dir, ".opds_token")
	if data, err := os.ReadFile(path); err == nil {
		tok := strings.TrimSpace(string(data))
		if tok != "" {
			return tok, nil
		}
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	tok := hex.EncodeToString(buf)

	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("ensure token dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(tok+"\n"), 0600); err != nil {
		return "", fmt.Errorf("write token file %q: %w", path, err)
	}
	return tok, nil
}

// TokenFingerprint returns a short, non-reversible fingerprint of a token,
// safe to log: "sha256:abcd1234".  The fingerprint is derived from SHA-256
// truncated to 8 hex characters — enough for operators to correlate two
// log lines without revealing the secret.
func TokenFingerprint(token string) string {
	if token == "" {
		return ""
	}
	h := sha256.Sum256([]byte(token))
	return "sha256:" + hex.EncodeToString(h[:4])
}
