package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/banux/nxt-opds/internal/catalog"
)

// chatSecretHeader is the HTTP header the remote librarian sends to
// authenticate against the inbound rotation and unpair endpoints.  Its
// value must match the chat_secret persisted in the local association.
const chatSecretHeader = "X-Librarian-Chat-Secret"

// forgetTimeout caps the best-effort outgoing call to the librarian on
// DELETE /api/librarian/association.  The local row is already cleared
// by then, so a slow remote must not block the user.
const forgetTimeout = 5 * time.Second

// librarianForgetClient is overridable from tests to intercept the
// best-effort outgoing call without requiring a real HTTP server.  It is
// initialised to http.DefaultClient.Do at startup.
var librarianForgetClient = func(req *http.Request) (*http.Response, error) {
	// httptest servers commonly use a short total timeout, so we instantiate
	// a fresh client for each call rather than mutating http.DefaultClient.
	client := &http.Client{Timeout: forgetTimeout}
	return client.Do(req)
}

// librarianChatClient is overridable from tests to intercept the SSE relay
// without requiring a real librarian.  No timeout — the chat is long-lived;
// the inbound request's context cancels the upstream call on client
// disconnect.
var librarianChatClient = func(req *http.Request) (*http.Response, error) {
	return http.DefaultClient.Do(req)
}

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

// handleAPILibrarianAssociation handles GET /api/librarian/association.
// It returns the persisted pairing record sans secrets so the admin UI can
// display "currently paired with X" without ever showing the chat or webhook
// keys.  Returns 204 No Content when no pairing exists.
//
// Restricted to admin session callers by requireSessionAdmin.
func (s *Server) handleAPILibrarianAssociation(w http.ResponseWriter, r *http.Request) {
	if s.librarianAssoc == nil {
		writeJSONError(w, http.StatusNotImplemented,
			"le backend de catalogue ne supporte pas l'appairage librarian")
		return
	}
	assoc, err := s.librarianAssoc.Get()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError,
			"lecture de l'association : "+err.Error())
		return
	}
	if assoc == nil || assoc.LibrarianURL == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Surface only the non-secret fields.  connected_at_last_ping mirrors
	// UpdatedAt for now (no liveness probe yet — future task will populate
	// it from an actual ping handler).  Timestamps are Unix milliseconds.
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"librarian_url":           assoc.LibrarianURL,
		"librarian_instance":      assoc.LibrarianInstance,
		"created_at":              unixMilliOrZero(assoc.CreatedAt),
		"updated_at":              unixMilliOrZero(assoc.UpdatedAt),
		"connected_at_last_ping":  unixMilliOrZero(assoc.UpdatedAt),
	})
}

// handleAPILibrarianDeleteAssociation handles DELETE /api/librarian/association.
//
// It clears the local row immediately so the UI flips back to "unpaired"
// without waiting on a remote that may be slow or unreachable.  In the
// background, a best-effort POST to
// ${librarian_url}/instances/{instance}/forget is fired with
// Authorization: Bearer ${chat_secret} so the librarian can clean up its
// side too.  Failures of that outgoing call are logged but never surface
// to the operator — the local state is authoritative.
//
// Idempotent: returns 204 even when no association existed.
//
// Restricted to admin session callers by requireSessionAdmin.
func (s *Server) handleAPILibrarianDeleteAssociation(w http.ResponseWriter, r *http.Request) {
	if s.librarianAssoc == nil {
		writeJSONError(w, http.StatusNotImplemented,
			"le backend de catalogue ne supporte pas l'appairage librarian")
		return
	}

	// Snapshot the secrets before we Clear() so the background notification
	// can authenticate against the librarian.  Errors here are surfaced as
	// 500 because they indicate a broken storage layer.
	assoc, err := s.librarianAssoc.Get()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError,
			"lecture de l'association : "+err.Error())
		return
	}
	if err := s.librarianAssoc.Clear(); err != nil {
		writeJSONError(w, http.StatusInternalServerError,
			"suppression de l'association : "+err.Error())
		return
	}

	if assoc != nil && assoc.LibrarianURL != "" && assoc.LibrarianInstance != "" {
		// Snapshot the client at request time so tests that swap the global
		// `librarianForgetClient` can restore it (via t.Cleanup / defer)
		// without racing against the goroutine.
		client := librarianForgetClient
		go notifyLibrarianForget(*assoc, client)
	}

	w.WriteHeader(http.StatusNoContent)
}

// notifyLibrarianForget fires a best-effort POST to the librarian's forget
// endpoint so the remote can drop this instance from its own list.  Network
// and HTTP errors are logged only — the caller has already cleared local
// state and considers the unpair successful.
func notifyLibrarianForget(assoc catalog.LibrarianAssociationData, client func(*http.Request) (*http.Response, error)) {
	url := strings.TrimRight(assoc.LibrarianURL, "/") +
		"/instances/" + assoc.LibrarianInstance + "/forget"

	ctx, cancel := context.WithTimeout(context.Background(), forgetTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		log.Printf("librarian forget: build request: %v", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+assoc.ChatSecret)

	resp, err := client(req)
	if err != nil {
		log.Printf("librarian forget %s: %v", url, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		log.Printf("librarian forget %s: HTTP %d", url, resp.StatusCode)
	}
}

// unixMilliOrZero returns t.UnixMilli() when t is non-zero, else 0.
// JSON serialises this as 0 rather than a misleading Unix-epoch timestamp.
func unixMilliOrZero(t time.Time) int64 {
	if t.IsZero() || t.Unix() <= 0 {
		return 0
	}
	return t.UnixMilli()
}

// authenticateLibrarian verifies the X-Librarian-Chat-Secret header against
// the stored chat_secret using constant-time comparison.  Returns the
// matched association on success; on failure it writes a JSON error to w
// and returns nil so the caller should return immediately.
//
// Used by endpoints that the *remote* librarian calls into nxt-opds —
// /api/librarian/rotate and /api/librarian/forget — where the chat secret
// is the only credential the librarian holds.
func (s *Server) authenticateLibrarian(w http.ResponseWriter, r *http.Request) *catalog.LibrarianAssociationData {
	if s.librarianAssoc == nil {
		writeJSONError(w, http.StatusNotImplemented,
			"le backend de catalogue ne supporte pas l'appairage librarian")
		return nil
	}
	assoc, err := s.librarianAssoc.Get()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError,
			"lecture de l'association : "+err.Error())
		return nil
	}
	if assoc == nil || assoc.ChatSecret == "" {
		// No association → no credential to verify against.  Return 404 so
		// the librarian knows the pairing is gone (it can stop retrying).
		writeJSONError(w, http.StatusNotFound, "aucune association librarian")
		return nil
	}
	presented := r.Header.Get(chatSecretHeader)
	if presented == "" {
		writeJSONError(w, http.StatusUnauthorized,
			"en-tête "+chatSecretHeader+" manquant")
		return nil
	}
	if subtle.ConstantTimeCompare([]byte(presented), []byte(assoc.ChatSecret)) != 1 {
		writeJSONError(w, http.StatusUnauthorized,
			"en-tête "+chatSecretHeader+" invalide")
		return nil
	}
	return assoc
}

// handleAPILibrarianRotate handles POST /api/librarian/rotate.
//
// This is an inbound endpoint called BY the remote librarian when it wants
// to roll its credentials without going through a full unpair / re-pair
// cycle.  Authentication is via the X-Librarian-Chat-Secret header (which
// the librarian still holds from the original pairing or from the
// previous rotation).
//
// On success it generates a fresh 32-byte hex chat_secret and a fresh
// 32-byte hex webhook_secret, writes them to the local association
// (preserving URL / instance / created_at), and returns {chat_secret,
// webhook_secret, instance} so the librarian can adopt the new keys.
//
// Failure modes:
//   - 401 missing or wrong X-Librarian-Chat-Secret
//   - 404 no association is currently paired
//   - 500 secret generation or DB write failure
//   - 501 catalog backend has no LibrarianAssociation impl
func (s *Server) handleAPILibrarianRotate(w http.ResponseWriter, r *http.Request) {
	assoc := s.authenticateLibrarian(w, r)
	if assoc == nil {
		return
	}

	newChat, err := randomHexSecret(32)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "génération du chat_secret : "+err.Error())
		return
	}
	newWebhook, err := randomHexSecret(32)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "génération du webhook_secret : "+err.Error())
		return
	}

	if err := s.librarianAssoc.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      assoc.LibrarianURL,
		LibrarianInstance: assoc.LibrarianInstance,
		ChatSecret:        newChat,
		WebhookSecret:     newWebhook,
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError,
			"écriture de l'association : "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"chat_secret":    newChat,
		"webhook_secret": newWebhook,
		"instance":       assoc.LibrarianInstance,
	})
}

// handleAPILibrarianForget handles POST /api/librarian/forget.
//
// Inbound endpoint called BY the remote librarian when it runs
// `librarian unpair`.  The chat secret in the X-Librarian-Chat-Secret
// header authenticates the caller; on success the local association is
// cleared.
//
// Idempotent: a second call after the association is already gone returns
// 204 No Content too — but only when the caller still presents the header
// (so an unauthenticated probe can't differentiate "paired" vs "unpaired"
// without trying actual secrets).
//
// Failure modes:
//   - 401 missing X-Librarian-Chat-Secret header
//   - 401 wrong secret (paired but value doesn't match stored chat_secret)
//   - 500 backend read / clear failure
//   - 501 catalog backend has no LibrarianAssociation impl
func (s *Server) handleAPILibrarianForget(w http.ResponseWriter, r *http.Request) {
	if s.librarianAssoc == nil {
		writeJSONError(w, http.StatusNotImplemented,
			"le backend de catalogue ne supporte pas l'appairage librarian")
		return
	}

	// Always require the header — refuse to act as an "is anyone paired?"
	// probe for unauthenticated callers.
	presented := r.Header.Get(chatSecretHeader)
	if presented == "" {
		writeJSONError(w, http.StatusUnauthorized,
			"en-tête "+chatSecretHeader+" manquant")
		return
	}

	assoc, err := s.librarianAssoc.Get()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError,
			"lecture de l'association : "+err.Error())
		return
	}

	if assoc == nil || assoc.ChatSecret == "" {
		// No association → nothing to do.  Return 204 so the librarian's
		// retry on a previously-handled unpair is a no-op.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Verify the secret in constant time, mirroring authenticateLibrarian.
	if subtle.ConstantTimeCompare([]byte(presented), []byte(assoc.ChatSecret)) != 1 {
		writeJSONError(w, http.StatusUnauthorized,
			"en-tête "+chatSecretHeader+" invalide")
		return
	}

	if err := s.librarianAssoc.Clear(); err != nil {
		writeJSONError(w, http.StatusInternalServerError,
			"suppression de l'association : "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAPILibrarianChat handles POST /api/ai/chat.
//
// It is an SSE-streaming relay to the paired librarian's /chat endpoint.
// The handler reads the persisted association; if none exists it returns
// 404 (so the SPA can hide the chat box).  Otherwise it forwards the
// inbound JSON body (e.g. {message, history}) to
// ${librarian_url}/chat with Authorization: Bearer ${chat_secret} and
// streams the upstream text/event-stream response back to the client.
//
// The inbound request's context is plumbed into the upstream call so a
// client disconnect tears the proxied request down.  No total timeout
// here — chat sessions are intentionally long-lived.
func (s *Server) handleAPILibrarianChat(w http.ResponseWriter, r *http.Request) {
	if s.librarianAssoc == nil {
		writeJSONError(w, http.StatusNotFound, "aucune association librarian")
		return
	}
	assoc, err := s.librarianAssoc.Get()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError,
			"lecture de l'association : "+err.Error())
		return
	}
	if assoc == nil || assoc.LibrarianURL == "" || assoc.ChatSecret == "" {
		writeJSONError(w, http.StatusNotFound, "aucune association librarian")
		return
	}

	upstreamURL := strings.TrimRight(assoc.LibrarianURL, "/") + "/chat"
	upstreamReq, err := http.NewRequestWithContext(r.Context(),
		http.MethodPost, upstreamURL, r.Body)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError,
			"construction requête : "+err.Error())
		return
	}
	// Carry over the inbound content type so the librarian sees the same JSON.
	if ct := r.Header.Get("Content-Type"); ct != "" {
		upstreamReq.Header.Set("Content-Type", ct)
	} else {
		upstreamReq.Header.Set("Content-Type", "application/json")
	}
	upstreamReq.Header.Set("Accept", "text/event-stream")
	upstreamReq.Header.Set("Authorization", "Bearer "+assoc.ChatSecret)

	resp, err := librarianChatClient(upstreamReq)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway,
			"librarian injoignable : "+err.Error())
		return
	}
	defer resp.Body.Close()

	// Mirror the upstream content type (event-stream by default) and disable
	// any intermediate buffering so events surface in real time.
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}
