package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookieName = "nxt_session"
	sessionDuration   = 30 * 24 * time.Hour // 30 days
)

// sessionStore holds active session tokens in memory.
// For a personal single-user server this is perfectly sufficient.
type sessionStore struct {
	mu      sync.RWMutex
	tokens  map[string]time.Time // token -> expiry
}

func newSessionStore() *sessionStore {
	return &sessionStore{tokens: make(map[string]time.Time)}
}

// create generates a new random session token, stores it, and returns it.
func (s *sessionStore) create() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	expiry := time.Now().Add(sessionDuration)

	s.mu.Lock()
	s.tokens[token] = expiry
	s.mu.Unlock()
	return token, nil
}

// valid returns true if token exists and has not expired.
func (s *sessionStore) valid(token string) bool {
	s.mu.RLock()
	exp, ok := s.tokens[token]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		s.mu.Lock()
		delete(s.tokens, token)
		s.mu.Unlock()
		return false
	}
	return true
}

// delete removes a session token (logout).
func (s *sessionStore) delete(token string) {
	s.mu.Lock()
	delete(s.tokens, token)
	s.mu.Unlock()
}

// authMiddleware returns a middleware that enforces session-cookie authentication.
// For OPDS clients that send HTTP Basic Auth, Basic Auth is also accepted as a fallback.
// If password is empty, auth is disabled (development mode).
func authMiddleware(password string, sessions *sessionStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if password == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 1. Check session cookie
			if c, err := r.Cookie(sessionCookieName); err == nil {
				if sessions.valid(c.Value) {
					next.ServeHTTP(w, r)
					return
				}
			}

			// 2. Fallback: HTTP Basic Auth (for OPDS readers / API clients)
			if _, pass, ok := r.BasicAuth(); ok {
				if subtle.ConstantTimeCompare([]byte(pass), []byte(password)) == 1 {
					next.ServeHTTP(w, r)
					return
				}
			}

			// 3. Not authenticated – redirect browser requests to /login,
			//    return 401 for API / OPDS requests.
			accept := r.Header.Get("Accept")
			isAPI := strings.HasPrefix(r.URL.Path, "/api/") ||
				strings.HasPrefix(r.URL.Path, "/opds/") ||
				r.URL.Path == "/opds" || r.URL.Path == "/opds/"
			if !isAPI && (accept == "" || containsHTML(accept)) {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}

			w.Header().Set("WWW-Authenticate", `Basic realm="nxt-opds"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
}

// containsHTML reports whether an Accept header value includes text/html.
func containsHTML(accept string) bool {
	for _, part := range splitAccept(accept) {
		if part == "text/html" || part == "text/*" || part == "*/*" {
			return true
		}
	}
	return false
}

// splitAccept splits a comma-separated Accept header into media-type tokens
// (without quality values).
func splitAccept(accept string) []string {
	var out []string
	for _, seg := range splitComma(accept) {
		// strip quality value: "text/html;q=0.9" → "text/html"
		if idx := indexOf(seg, ';'); idx >= 0 {
			seg = seg[:idx]
		}
		if s := trimSpace(seg); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func indexOf(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
