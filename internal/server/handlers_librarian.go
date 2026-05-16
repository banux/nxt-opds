package server

import (
	"encoding/json"
	"net/http"
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
