package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/banux/nxt-opds/internal/catalog"
)

// requireSessionAdmin wraps a handler so it only runs for callers who
// authenticated with the session cookie AND whose user record has IsAdmin =
// true.  Per-user OPDS/MCP tokens and the shared OPDS token are explicitly
// rejected (this protects pairing-related endpoints from token replay).
//
// In single-user mode (no UserManager or no users registered), or when
// password auth is disabled, the inner handler runs without further checks
// — the surrounding authMiddleware has already vetted the request.
func (s *Server) requireSessionAdmin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Single-user mode / auth disabled: defer to authMiddleware.
		if s.userManager == nil || !s.hasMultipleUsers() {
			h(w, r)
			return
		}

		// Reject anything that didn't present a valid session cookie.
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || !s.sessions.valid(cookie.Value) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		uid := s.sessions.userIDForToken(cookie.Value)
		if uid == "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		me, err := s.userManager.UserByID(uid)
		if err != nil || me == nil || !me.IsAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

// handleAPILibrarianPairingCode handles POST /api/librarian/pairing-code.
// It mints a short-lived single-use code that an operator pastes into the
// `librarian pair` CLI to bind a remote librarian instance to this server.
//
// Restricted to admin session callers by requireSessionAdmin.
func (s *Server) handleAPILibrarianPairingCode(w http.ResponseWriter, r *http.Request) {
	if s.librarianAssoc == nil {
		http.Error(w,
			`{"error":"le backend de catalogue ne supporte pas l'appairage librarian"}`,
			http.StatusNotImplemented)
		return
	}

	code, expiresAt, err := s.pairingCodes.Generate()
	if err != nil {
		http.Error(w, `{"error":"generation du code echouee"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":      code,
		"expiresAt": expiresAt.UnixMilli(),
	})
}

// handleAPILibrarianPair handles POST /api/librarian/pair.
//
// This endpoint is the back half of the pairing handshake started by
// /api/librarian/pairing-code.  It is intentionally PUBLIC (not behind
// authMiddleware) — the one-time code in the body is the authentication.
//
// Body: {"code":"XXXX-XXXX","librarian_url":"https://…","instance":"…","label":"…","force":false}
//
// On success: generates a 32-byte hex chat_secret and a 32-byte hex
// webhook_secret, persists the association, invalidates the code, and
// returns {mcp_url, mcp_token, chat_secret, webhook_secret, instance, label}.
//
// Failure modes:
//   - 400 missing/empty required fields, malformed JSON
//   - 401 unknown/expired/already-consumed code
//   - 409 association already exists and force != true
//   - 500 backend write failure
//   - 501 catalog backend has no LibrarianAssociation impl
//   - 503 OPDS token not configured (mcp_token would be empty)
func (s *Server) handleAPILibrarianPair(w http.ResponseWriter, r *http.Request) {
	if s.librarianAssoc == nil {
		writeJSONError(w, http.StatusNotImplemented,
			"le backend de catalogue ne supporte pas l'appairage librarian")
		return
	}
	if s.opdsToken == "" {
		writeJSONError(w, http.StatusServiceUnavailable,
			"OPDS token non configuré — l'appairage librarian nécessite un mot de passe configuré")
		return
	}

	var req struct {
		Code         string `json:"code"`
		LibrarianURL string `json:"librarian_url"`
		Instance     string `json:"instance"`
		Label        string `json:"label"`
		Force        bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "JSON invalide")
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	req.LibrarianURL = strings.TrimRight(strings.TrimSpace(req.LibrarianURL), "/")
	req.Instance = strings.TrimSpace(req.Instance)
	req.Label = strings.TrimSpace(req.Label)
	if req.Code == "" || req.LibrarianURL == "" || req.Instance == "" {
		writeJSONError(w, http.StatusBadRequest, "champs requis : code, librarian_url, instance")
		return
	}

	// Short-circuit obviously wrong codes before touching the DB.
	if err := s.pairingCodes.Validate(req.Code); err != nil {
		writeJSONError(w, http.StatusUnauthorized, "code d'appairage invalide ou expiré")
		return
	}

	// Refuse if an association already exists and the caller didn't say `force`.
	existing, err := s.librarianAssoc.Get()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError,
			"lecture de l'association : "+err.Error())
		return
	}
	if existing != nil && existing.LibrarianURL != "" && !req.Force {
		writeJSONError(w, http.StatusConflict,
			"un librarian est déjà appairé — utilisez force=true pour remplacer")
		return
	}

	// Generate the two 32-byte secrets exposed to the librarian.
	chatSecret, err := randomHexSecret(32)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "génération du secret : "+err.Error())
		return
	}
	webhookSecret, err := randomHexSecret(32)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "génération du secret : "+err.Error())
		return
	}

	if err := s.librarianAssoc.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      req.LibrarianURL,
		LibrarianInstance: req.Instance,
		ChatSecret:        chatSecret,
		WebhookSecret:     webhookSecret,
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError,
			"écriture de l'association : "+err.Error())
		return
	}

	// Invalidate the code so it can't be replayed.  We swallow the error: if
	// Consume fails here the code will be auto-pruned by TTL anyway.
	_ = s.pairingCodes.Consume(req.Code)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"mcp_url":        externalURL(r) + "/mcp",
		"mcp_token":      s.opdsToken,
		"chat_secret":    chatSecret,
		"webhook_secret": webhookSecret,
		"instance":       req.Instance,
		"label":          req.Label,
	})
}

// randomHexSecret returns a hex-encoded crypto-random secret of length
// n*2 hex characters (i.e. n raw bytes of entropy).
func randomHexSecret(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", errors.New("rand: " + err.Error())
	}
	return hex.EncodeToString(buf), nil
}

// externalURL reconstructs the public-facing base URL the client used to
// reach the server, taking the X-Forwarded-Proto / X-Forwarded-Host headers
// into account so reverse-proxy setups (Caddy/Traefik/Nginx) produce the
// right public mcp_url.  No trailing slash.
func externalURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
		// Sometimes proxies stuff "https, https" — take the first token.
		if i := strings.IndexByte(v, ','); i > 0 {
			v = v[:i]
		}
		scheme = strings.TrimSpace(v)
	}
	host := r.Host
	if v := r.Header.Get("X-Forwarded-Host"); v != "" {
		if i := strings.IndexByte(v, ','); i > 0 {
			v = v[:i]
		}
		host = strings.TrimSpace(v)
	}
	return scheme + "://" + host
}

// writeJSONError writes a JSON {error: msg} body with the given status code.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
