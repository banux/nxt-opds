package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/banux/nxt-opds/internal/catalog"
)

const (
	sessionCookieName = "nxt_session"
	sessionDuration   = 30 * 24 * time.Hour // 30 days
)

// contextKey is a private type for context keys in this package.
type contextKey string

// ctxUserID is the context key for the current user's ID.
const ctxUserID contextKey = "userID"

// sessionEntry holds session data: expiry and the ID of the logged-in user.
type sessionEntry struct {
	expiry time.Time
	userID string // empty when no user selected (legacy / no-users mode)
}

// sessionStore holds active session tokens in memory and optionally persists
// them to a catalog.SessionPersistence backend so they survive restarts.
type sessionStore struct {
	mu          sync.RWMutex
	tokens      map[string]sessionEntry    // token -> entry
	persistence catalog.SessionPersistence // optional; nil = in-memory only
}

func newSessionStore() *sessionStore {
	return &sessionStore{tokens: make(map[string]sessionEntry)}
}

// loadFromPersistence loads all non-expired sessions from the persistence
// backend into the in-memory map.  Should be called once after construction.
func (s *sessionStore) loadFromPersistence() {
	if s.persistence == nil {
		return
	}
	sessions, err := s.persistence.LoadSessions()
	if err != nil {
		log.Printf("session store: failed to load sessions: %v", err)
		return
	}
	s.mu.Lock()
	for _, sd := range sessions {
		if time.Now().Before(sd.Expiry) {
			s.tokens[sd.Token] = sessionEntry{expiry: sd.Expiry, userID: sd.UserID}
		}
	}
	s.mu.Unlock()
	log.Printf("session store: loaded %d session(s) from database", len(sessions))
}

// create generates a new random session token bound to userID, stores it,
// and returns the token.  userID may be empty when no users are configured.
func (s *sessionStore) create(userID string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	expiry := time.Now().Add(sessionDuration)
	s.mu.Lock()
	s.tokens[token] = sessionEntry{expiry: expiry, userID: userID}
	s.mu.Unlock()
	if s.persistence != nil {
		if err := s.persistence.SaveSession(token, userID, expiry); err != nil {
			log.Printf("session store: failed to persist session: %v", err)
		}
	}
	return token, nil
}

// valid returns true if token exists and has not expired.
func (s *sessionStore) valid(token string) bool {
	s.mu.RLock()
	entry, ok := s.tokens[token]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(entry.expiry) {
		s.mu.Lock()
		delete(s.tokens, token)
		s.mu.Unlock()
		return false
	}
	return true
}

// userIDForToken returns the userID stored in the session, or empty string if
// the token is invalid or no user was bound to it.
func (s *sessionStore) userIDForToken(token string) string {
	s.mu.RLock()
	entry, ok := s.tokens[token]
	s.mu.RUnlock()
	if !ok || time.Now().After(entry.expiry) {
		return ""
	}
	return entry.userID
}

// delete removes a session token (logout).
func (s *sessionStore) delete(token string) {
	s.mu.Lock()
	delete(s.tokens, token)
	s.mu.Unlock()
	if s.persistence != nil {
		if err := s.persistence.DeleteSession(token); err != nil {
			log.Printf("session store: failed to delete persisted session: %v", err)
		}
	}
}

// authMiddleware returns a middleware that enforces session-cookie authentication.
//
// Authentication methods (in order of precedence):
//  1. Session cookie (browser users after login).
//  2. Per-user token via ?token= query parameter (or Bearer header on /mcp);
//     when a token matches a registered user, that user's ID is injected into
//     the request context so per-user feeds (recommendations, to-read pile,
//     unread) are personalised.
//  3. Shared OPDS token via ?token= query parameter (for OPDS reader clients
//     on OPDS routes).
//  4. HTTP Basic Auth fallback (kept for API clients; only when no opdsToken is set).
//
// If password is empty, auth is disabled (development mode).
// opdsToken is the shared token for OPDS feed access; empty means token auth disabled.
// userManager (optional) enables per-user token authentication; nil disables it.
func authMiddleware(password, opdsToken string, sessions *sessionStore, userManager catalog.UserManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if password == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 1. Check session cookie
			if c, err := r.Cookie(sessionCookieName); err == nil {
				if sessions.valid(c.Value) {
					// Inject the user ID into the request context.
					uid := sessions.userIDForToken(c.Value)
					if uid != "" {
						r = r.WithContext(context.WithValue(r.Context(), ctxUserID, uid))
					}
					next.ServeHTTP(w, r)
					return
				}
			}

			// 2. Token auth: accepted on OPDS routes and cover images via ?token= query param,
			//    and on /mcp via Authorization: Bearer <token> header.
			isOPDS := strings.HasPrefix(r.URL.Path, "/opds/") ||
				r.URL.Path == "/opds" || r.URL.Path == "/opds/"
			isCover := strings.HasPrefix(r.URL.Path, "/covers/")
			isMCP := r.URL.Path == "/mcp"

			// Extract candidate token from query string (OPDS / cover) or
			// Bearer header / query (MCP).
			var presented string
			if isOPDS || isCover {
				presented = r.URL.Query().Get("token")
			} else if isMCP {
				if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
					presented = strings.TrimPrefix(auth, "Bearer ")
				} else if tok := r.URL.Query().Get("token"); tok != "" {
					presented = tok
				}
			}

			if presented != "" {
				// 2a. Per-user token (works on OPDS routes, covers and /mcp).
				if (isOPDS || isCover || isMCP) && userManager != nil {
					if u, err := userManager.UserByToken(presented); err == nil && u != nil {
						r = r.WithContext(context.WithValue(r.Context(), ctxUserID, u.ID))
						next.ServeHTTP(w, r)
						return
					}
				}
				// 2b. Shared OPDS token (back-compat for clients that still use it).
				if opdsToken != "" && (isOPDS || isCover || isMCP) {
					if subtle.ConstantTimeCompare([]byte(presented), []byte(opdsToken)) == 1 {
						next.ServeHTTP(w, r)
						return
					}
				}
			}

			// 3. Fallback: HTTP Basic Auth (for API clients and legacy OPDS readers
			//    when no opdsToken is configured).
			if opdsToken == "" {
				if _, pass, ok := r.BasicAuth(); ok {
					if subtle.ConstantTimeCompare([]byte(pass), []byte(password)) == 1 {
						next.ServeHTTP(w, r)
						return
					}
				}
			}

			// 4. Not authenticated – redirect browser requests to /login,
			//    return 401 for API / OPDS requests.
			accept := r.Header.Get("Accept")
			isAPI := strings.HasPrefix(r.URL.Path, "/api/") || isOPDS
			if !isAPI && (accept == "" || containsHTML(accept)) {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}

			w.Header().Set("WWW-Authenticate", `Bearer realm="nxt-opds"`)
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
