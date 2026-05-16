package server

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// pairingCodeTTL is how long a freshly generated pairing code remains valid.
const pairingCodeTTL = 10 * time.Minute

// pairingCodeAlphabet is a Crockford-Base32-style alphabet (32 chars, no
// 'I', 'L', 'O', 'U') chosen so an admin can read the code aloud and the
// remote pairer can type it without ambiguity.  8 chars * 5 bits = 40 bits
// of entropy.
const pairingCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// errPairingCodeInvalid is returned by pairingCodeStore.Consume when the
// supplied code is unknown, already redeemed, or expired.
var errPairingCodeInvalid = errors.New("pairing code invalid or expired")

// pairingCodeStore holds short-lived pairing codes in memory.  Codes are
// single-use: a successful Consume removes the entry.  Expired entries are
// reaped lazily on each call.
type pairingCodeStore struct {
	mu    sync.Mutex
	codes map[string]time.Time // code -> expiresAt
}

func newPairingCodeStore() *pairingCodeStore {
	return &pairingCodeStore{codes: make(map[string]time.Time)}
}

// Generate produces a new pairing code in the XXXX-XXXX form, stores it
// with a 10-minute TTL and returns the code along with its expiry time.
func (s *pairingCodeStore) Generate() (string, time.Time, error) {
	code, err := randomPairingCode()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().Add(pairingCodeTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	s.codes[code] = expiresAt
	return code, expiresAt, nil
}

// Consume validates and atomically removes a pairing code.  Returns nil if
// the code was valid; errPairingCodeInvalid otherwise.  Comparison is
// case-insensitive and tolerant of the optional `-` separator so a user
// pasting "abcd1234" or "abcd-1234" works the same.
func (s *pairingCodeStore) Consume(code string) error {
	normalised := normalisePairingCode(code)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	expiresAt, ok := s.codes[normalised]
	if !ok {
		return errPairingCodeInvalid
	}
	delete(s.codes, normalised)
	if time.Now().After(expiresAt) {
		return errPairingCodeInvalid
	}
	return nil
}

// prune removes any expired entries.  Caller holds s.mu.
func (s *pairingCodeStore) prune() {
	now := time.Now()
	for c, exp := range s.codes {
		if now.After(exp) {
			delete(s.codes, c)
		}
	}
}

// randomPairingCode returns an 8-character XXXX-XXXX code drawn uniformly
// from pairingCodeAlphabet.  Uses crypto/rand for entropy.
func randomPairingCode() (string, error) {
	const length = 8
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("pairing code rand: %w", err)
	}
	out := make([]byte, 0, length+1)
	for i, b := range buf {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, pairingCodeAlphabet[int(b)%len(pairingCodeAlphabet)])
	}
	return string(out), nil
}

// normalisePairingCode upper-cases and re-inserts the canonical XXXX-XXXX
// dash so the store lookup is forgiving of how a user pastes the code.
// Returns the raw input unchanged when it doesn't fit the expected shape;
// callers will then get errPairingCodeInvalid from Consume.
func normalisePairingCode(code string) string {
	cleaned := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	if len(cleaned) != 8 {
		return cleaned
	}
	return cleaned[:4] + "-" + cleaned[4:]
}
