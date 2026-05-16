package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/banux/nxt-opds/internal/catalog"
)

// TestPairingCode_GenerateFormat verifies the generated code matches the
// XXXX-XXXX shape over the Crockford-Base32 alphabet and that successive
// generations are distinct.
func TestPairingCode_GenerateFormat(t *testing.T) {
	st := newPairingCodeStore()
	codeRe := regexp.MustCompile(`^[0-9A-HJKMNPQRSTVWXYZ]{4}-[0-9A-HJKMNPQRSTVWXYZ]{4}$`)
	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		code, exp, err := st.Generate()
		if err != nil {
			t.Fatalf("Generate #%d: %v", i, err)
		}
		if !codeRe.MatchString(code) {
			t.Errorf("Generate #%d: code %q does not match XXXX-XXXX over the expected alphabet", i, code)
		}
		if seen[code] {
			t.Errorf("Generate #%d returned a duplicate code %q (entropy too low?)", i, code)
		}
		seen[code] = true
		// TTL must be close to 10 minutes from now.
		until := time.Until(exp)
		if until < 9*time.Minute || until > 11*time.Minute {
			t.Errorf("Generate #%d: expiry %s is not ~10min from now", i, until)
		}
	}
}

// TestPairingCode_ConsumeSingleUse verifies that a code is removed on its
// first successful Consume and that a second Consume returns the invalid
// error.
func TestPairingCode_ConsumeSingleUse(t *testing.T) {
	st := newPairingCodeStore()
	code, _, err := st.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := st.Consume(code); err != nil {
		t.Fatalf("first Consume should succeed, got %v", err)
	}
	if err := st.Consume(code); err == nil {
		t.Fatalf("second Consume should fail (single-use)")
	}
}

// TestPairingCode_ConsumeNormalisation verifies that the store accepts the
// code without dash and with mixed case, so an operator pasting it from a
// terminal isn't tripped up.
func TestPairingCode_ConsumeNormalisation(t *testing.T) {
	st := newPairingCodeStore()
	code, _, err := st.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Strip the dash and lowercase the result — Consume must still accept it.
	tweaked := strings.ToLower(strings.ReplaceAll(code, "-", ""))
	if err := st.Consume(tweaked); err != nil {
		t.Errorf("Consume %q (normalised from %q) should succeed, got %v", tweaked, code, err)
	}
}

// TestPairingCode_ExpiredCodeRejected verifies that codes past their TTL
// are rejected.  Uses direct map manipulation to avoid waiting 10 minutes.
func TestPairingCode_ExpiredCodeRejected(t *testing.T) {
	st := newPairingCodeStore()
	code, _, err := st.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Backdate the expiry so the code looks expired.
	st.mu.Lock()
	st.codes[code] = time.Now().Add(-1 * time.Second)
	st.mu.Unlock()

	if err := st.Consume(code); err == nil {
		t.Fatalf("expired code should be rejected")
	}
}

// TestPairingCode_UnknownCodeRejected verifies that a code that was never
// generated is rejected.
func TestPairingCode_UnknownCodeRejected(t *testing.T) {
	st := newPairingCodeStore()
	if err := st.Consume("AAAA-BBBB"); err == nil {
		t.Fatalf("unknown code should be rejected")
	}
}

// TestHandleAPILibrarianPairingCode_AdminSessionReturnsCode verifies that
// an admin authenticated via session cookie gets a valid {code, expiresAt}
// payload.
func TestHandleAPILibrarianPairingCode_AdminSessionReturnsCode(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	admin, err := backend.CreateUser("Admin", "#000", true, false, 0)
	if err != nil {
		t.Fatalf("CreateUser admin: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/librarian/pairing-code", nil)
	for _, c := range loginAsHelper(t, srv, admin.ID) {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Code      string `json:"code"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	codeRe := regexp.MustCompile(`^[0-9A-HJKMNPQRSTVWXYZ]{4}-[0-9A-HJKMNPQRSTVWXYZ]{4}$`)
	if !codeRe.MatchString(resp.Code) {
		t.Errorf("code %q does not match expected format", resp.Code)
	}
	// expiresAt must be ~10min in the future (in milliseconds).
	now := time.Now().UnixMilli()
	if resp.ExpiresAt-now < 9*60*1000 || resp.ExpiresAt-now > 11*60*1000 {
		t.Errorf("expiresAt %d is not ~10min from now (delta = %d ms)", resp.ExpiresAt, resp.ExpiresAt-now)
	}

	// The code must also be redeemable through the store exactly once.
	if err := srv.pairingCodes.Consume(resp.Code); err != nil {
		t.Errorf("the generated code should be consumable, got %v", err)
	}
}

// TestHandleAPILibrarianPairingCode_NonAdminForbidden verifies that a
// non-admin authenticated via session cookie cannot generate a code.
func TestHandleAPILibrarianPairingCode_NonAdminForbidden(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if _, err := backend.CreateUser("Admin", "#000", true, false, 0); err != nil {
		t.Fatalf("CreateUser admin: %v", err)
	}
	alice, err := backend.CreateUser("Alice", "#f00", false, false, 0)
	if err != nil {
		t.Fatalf("CreateUser alice: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/librarian/pairing-code", nil)
	for _, c := range loginAsHelper(t, srv, alice.ID) {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleAPILibrarianPairingCode_OPDSTokenForbidden verifies that the
// shared OPDS token cannot mint pairing codes even though admin auth is
// otherwise satisfied — the endpoint requires a real session cookie.
//
// This guards against an OPDS reader (which legitimately holds the shared
// token) being able to start a librarian pairing.
func TestHandleAPILibrarianPairingCode_OPDSTokenForbidden(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw", OPDSToken: "shared-tok"})
	if _, err := backend.CreateUser("Admin", "#000", true, false, 0); err != nil {
		t.Fatalf("CreateUser admin: %v", err)
	}

	// No cookie, but a valid shared OPDS token.  authMiddleware accepts the
	// token on /opds and /covers; on /api/* it should fall through to the
	// password gate, so we expect 401 (not 200, not 403).
	req := httptest.NewRequest(http.MethodPost,
		"/api/librarian/pairing-code?token=shared-tok", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized && rr.Code != http.StatusForbidden {
		t.Fatalf("expected 401/403 with OPDS token only, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleAPILibrarianPairingCode_NoAuth verifies that an unauthenticated
// request is rejected (not 200).
func TestHandleAPILibrarianPairingCode_NoAuth(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if _, err := backend.CreateUser("Admin", "#000", true, false, 0); err != nil {
		t.Fatalf("CreateUser admin: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/librarian/pairing-code", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code == http.StatusOK {
		t.Fatalf("expected non-200 for unauthenticated request, got 200: %s", rr.Body.String())
	}
}

// ---- /api/librarian/pair endpoint ---------------------------------------

// pairBody is a small helper to build a JSON pair request body.
func pairBody(code, url, instance, label string, force bool) *strings.Reader {
	b, _ := json.Marshal(map[string]any{
		"code":          code,
		"librarian_url": url,
		"instance":      instance,
		"label":         label,
		"force":         force,
	})
	return strings.NewReader(string(b))
}

// TestHandleAPILibrarianPair_HappyPath drives a full pairing handshake on
// a single-user server: a code is minted via the store, then exchanged via
// POST /api/librarian/pair which must return both secrets, the mcp URL and
// the existing OPDS token, persist the association in the catalog backend,
// and invalidate the code.
func TestHandleAPILibrarianPair_HappyPath(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw", OPDSToken: "the-opds-tok"})

	code, _, err := srv.pairingCodes.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/librarian/pair",
		pairBody(code, "https://librarian.example/", "inst-7", "Salon", false))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "books.example.test:8080"
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		MCPURL        string `json:"mcp_url"`
		MCPToken      string `json:"mcp_token"`
		ChatSecret    string `json:"chat_secret"`
		WebhookSecret string `json:"webhook_secret"`
		Instance      string `json:"instance"`
		Label         string `json:"label"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.MCPURL != "http://books.example.test:8080/mcp" {
		t.Errorf("mcp_url: got %q", resp.MCPURL)
	}
	if resp.MCPToken != "the-opds-tok" {
		t.Errorf("mcp_token: got %q", resp.MCPToken)
	}
	if len(resp.ChatSecret) != 64 {
		t.Errorf("chat_secret should be 64 hex chars, got %d (%q)", len(resp.ChatSecret), resp.ChatSecret)
	}
	if len(resp.WebhookSecret) != 64 {
		t.Errorf("webhook_secret should be 64 hex chars, got %d (%q)", len(resp.WebhookSecret), resp.WebhookSecret)
	}
	if resp.ChatSecret == resp.WebhookSecret {
		t.Errorf("the two secrets must differ (got same value %q)", resp.ChatSecret)
	}
	if resp.Instance != "inst-7" || resp.Label != "Salon" {
		t.Errorf("instance/label echo: got %+v", resp)
	}

	// Association must be persisted with both secrets and the trimmed URL.
	got, err := backend.Get()
	if err != nil || got == nil {
		t.Fatalf("association not persisted: err=%v got=%v", err, got)
	}
	if got.LibrarianURL != "https://librarian.example" {
		t.Errorf("librarian_url should be trim-right-slashed: got %q", got.LibrarianURL)
	}
	if got.ChatSecret != resp.ChatSecret || got.WebhookSecret != resp.WebhookSecret {
		t.Errorf("persisted secrets diverge from response")
	}
	if got.LibrarianInstance != "inst-7" {
		t.Errorf("instance: got %q", got.LibrarianInstance)
	}

	// Code must be single-use: a second pair attempt with the same code
	// should now fail with 401.
	req2 := httptest.NewRequest(http.MethodPost, "/api/librarian/pair",
		pairBody(code, "https://librarian.example/", "inst-7", "Salon", true))
	req2.Header.Set("Content-Type", "application/json")
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for replayed code, got %d: %s", rr2.Code, rr2.Body.String())
	}
}

// TestHandleAPILibrarianPair_NoAuthMiddleware verifies that the endpoint is
// reachable without any login cookie or OPDS token — only the body code
// authenticates the call.  This is critical: a librarian host pairing for
// the first time has nothing else to present.
func TestHandleAPILibrarianPair_NoAuthMiddleware(t *testing.T) {
	srv, _, _ := newSQLiteTestServer(t, Options{Password: "pw", OPDSToken: "tok"})

	// Bad code → still returns a JSON 401 (proves we reached the handler,
	// the auth middleware did not redirect us to /login).
	req := httptest.NewRequest(http.MethodPost, "/api/librarian/pair",
		pairBody("XXXX-YYYY", "https://librarian.example", "inst", "", false))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid code (handler reached), got %d: %s",
			rr.Code, rr.Body.String())
	}
}

// TestHandleAPILibrarianPair_MissingFields verifies field validation.
func TestHandleAPILibrarianPair_MissingFields(t *testing.T) {
	srv, _, _ := newSQLiteTestServer(t, Options{Password: "pw", OPDSToken: "tok"})

	cases := []struct {
		name string
		body string
	}{
		{"empty code", `{"code":"","librarian_url":"https://x","instance":"i"}`},
		{"empty url", `{"code":"AAAA-BBBB","librarian_url":"","instance":"i"}`},
		{"empty instance", `{"code":"AAAA-BBBB","librarian_url":"https://x","instance":""}`},
		{"malformed json", `not-json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/librarian/pair",
				strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("expected 400 for %s, got %d: %s", tc.name, rr.Code, rr.Body.String())
			}
		})
	}
}

// TestHandleAPILibrarianPair_ConflictWithoutForce verifies that a second
// pairing attempt against a server that already has an association returns
// 409 unless `force:true` is set; force=true must replace the previous
// secrets and URL.
func TestHandleAPILibrarianPair_ConflictWithoutForce(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw", OPDSToken: "tok"})

	// Seed an existing association.
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://old.example",
		LibrarianInstance: "old-inst",
		ChatSecret:        "old-chat",
		WebhookSecret:     "old-webhook",
	}); err != nil {
		t.Fatalf("seed association: %v", err)
	}

	code, _, _ := srv.pairingCodes.Generate()

	// First attempt without force → 409.
	req := httptest.NewRequest(http.MethodPost, "/api/librarian/pair",
		pairBody(code, "https://new.example", "new-inst", "", false))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 without force, got %d: %s", rr.Code, rr.Body.String())
	}

	// Code must NOT have been consumed (since pairing failed before Consume).
	if err := srv.pairingCodes.Validate(code); err != nil {
		t.Errorf("code should still be valid after a 409, got %v", err)
	}

	// Existing association unchanged.
	got, _ := backend.Get()
	if got.ChatSecret != "old-chat" {
		t.Errorf("association mutated despite 409: chat_secret=%q", got.ChatSecret)
	}

	// Second attempt WITH force → 200, association replaced.
	req2 := httptest.NewRequest(http.MethodPost, "/api/librarian/pair",
		pairBody(code, "https://new.example", "new-inst", "", true))
	req2.Header.Set("Content-Type", "application/json")
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected 200 with force, got %d: %s", rr2.Code, rr2.Body.String())
	}
	got, _ = backend.Get()
	if got.LibrarianURL != "https://new.example" || got.LibrarianInstance != "new-inst" {
		t.Errorf("force should replace the association: %+v", got)
	}
	if got.ChatSecret == "old-chat" || got.WebhookSecret == "old-webhook" {
		t.Errorf("force should mint new secrets, but old ones survived")
	}
}

// TestHandleAPILibrarianPair_NoOPDSToken verifies the endpoint refuses to
// hand out an empty mcp_token (which would leave the librarian unable to
// authenticate to /mcp).
func TestHandleAPILibrarianPair_NoOPDSToken(t *testing.T) {
	srv, _, _ := newSQLiteTestServer(t, Options{Password: "pw" /* no OPDSToken */})
	code, _, _ := srv.pairingCodes.Generate()
	req := httptest.NewRequest(http.MethodPost, "/api/librarian/pair",
		pairBody(code, "https://librarian.example", "i", "", false))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 with no OPDSToken, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleAPILibrarianPair_RespectsForwardedHeaders verifies that the
// returned mcp_url honours X-Forwarded-Proto / X-Forwarded-Host so a
// reverse-proxy setup returns the right public URL.
func TestHandleAPILibrarianPair_RespectsForwardedHeaders(t *testing.T) {
	srv, _, _ := newSQLiteTestServer(t, Options{Password: "pw", OPDSToken: "tok"})
	code, _, _ := srv.pairingCodes.Generate()
	req := httptest.NewRequest(http.MethodPost, "/api/librarian/pair",
		pairBody(code, "https://librarian.example", "inst", "", false))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "public.example.com")
	req.Host = "internal:8080"
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		MCPURL string `json:"mcp_url"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.MCPURL != "https://public.example.com/mcp" {
		t.Errorf("X-Forwarded-* not honoured: mcp_url=%q", resp.MCPURL)
	}
}

// ---- GET / DELETE /api/librarian/association ----------------------------

// TestHandleAPILibrarianAssociation_NoneReturns204 verifies the GET endpoint
// returns 204 No Content (no body) when no librarian is paired yet.
func TestHandleAPILibrarianAssociation_NoneReturns204(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	admin, err := backend.CreateUser("Admin", "#000", true, false, 0)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/librarian/association", nil)
	for _, c := range loginAsHelper(t, srv, admin.ID) {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
	if rr.Body.Len() != 0 {
		t.Errorf("expected empty body, got %q", rr.Body.String())
	}
}

// TestHandleAPILibrarianAssociation_ReturnsSafeFields verifies the GET
// endpoint returns the non-secret fields (URL, instance, timestamps) and
// crucially excludes chat_secret / webhook_secret from the response.
func TestHandleAPILibrarianAssociation_ReturnsSafeFields(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	admin, err := backend.CreateUser("Admin", "#000", true, false, 0)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://librarian.example",
		LibrarianInstance: "inst-x",
		ChatSecret:        "chat-secret-should-not-leak",
		WebhookSecret:     "webhook-secret-should-not-leak",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/librarian/association", nil)
	for _, c := range loginAsHelper(t, srv, admin.ID) {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if strings.Contains(body, "chat-secret-should-not-leak") ||
		strings.Contains(body, "webhook-secret-should-not-leak") {
		t.Fatalf("secrets leaked in response body: %s", body)
	}

	var resp struct {
		LibrarianURL      string `json:"librarian_url"`
		LibrarianInstance string `json:"librarian_instance"`
		CreatedAt         int64  `json:"created_at"`
		UpdatedAt         int64  `json:"updated_at"`
		LastSeenAt        int64  `json:"last_seen_at"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.LibrarianURL != "https://librarian.example" || resp.LibrarianInstance != "inst-x" {
		t.Errorf("payload mismatch: %+v", resp)
	}
	if resp.CreatedAt == 0 || resp.UpdatedAt == 0 {
		t.Errorf("timestamps should be populated: %+v", resp)
	}
	// LastSeenAt is 0 until the first heartbeat lands.
	if resp.LastSeenAt != 0 {
		t.Errorf("LastSeenAt should be 0 before any heartbeat: got %d", resp.LastSeenAt)
	}
	// The legacy connected_at_last_ping key must no longer appear in the body.
	if strings.Contains(body, "connected_at_last_ping") {
		t.Errorf("legacy field 'connected_at_last_ping' still present: %s", body)
	}
}

// TestHandleAPILibrarianAssociation_SurfacesLastSeenAt verifies that once a
// heartbeat has been recorded via the new Touch() method on the backend, the
// GET /api/librarian/association response surfaces it under the last_seen_at
// JSON key (Unix milliseconds, not seconds).
func TestHandleAPILibrarianAssociation_SurfacesLastSeenAt(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	admin, err := backend.CreateUser("Admin", "#000", true, false, 0)
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://librarian.example",
		LibrarianInstance: "inst-hb",
		ChatSecret:        "s",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	stamp := time.Now().Truncate(time.Second)
	if err := backend.Touch(stamp); err != nil {
		t.Fatalf("touch: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/librarian/association", nil)
	for _, c := range loginAsHelper(t, srv, admin.ID) {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		LastSeenAt int64 `json:"last_seen_at"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.LastSeenAt != stamp.UnixMilli() {
		t.Errorf("LastSeenAt should be %d (Unix ms), got %d", stamp.UnixMilli(), resp.LastSeenAt)
	}
}

// TestHandleAPILibrarianAssociation_NonAdminForbidden verifies that a
// non-admin session cookie cannot read the association card.
func TestHandleAPILibrarianAssociation_NonAdminForbidden(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if _, err := backend.CreateUser("Admin", "#000", true, false, 0); err != nil {
		t.Fatalf("admin: %v", err)
	}
	alice, err := backend.CreateUser("Alice", "#f00", false, false, 0)
	if err != nil {
		t.Fatalf("alice: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/librarian/association", nil)
	for _, c := range loginAsHelper(t, srv, alice.ID) {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
}

// TestHandleAPILibrarianDeleteAssociation_ClearsRowAndNotifiesRemote
// verifies the full DELETE flow: local row is cleared, a best-effort
// POST hits ${librarian_url}/instances/{instance}/forget with the
// Bearer chat_secret, and the response is 204.
func TestHandleAPILibrarianDeleteAssociation_ClearsRowAndNotifiesRemote(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	admin, err := backend.CreateUser("Admin", "#000", true, false, 0)
	if err != nil {
		t.Fatalf("admin: %v", err)
	}

	// Stand up a fake librarian that records the forget call.
	type capturedCall struct {
		path string
		auth string
	}
	calls := make(chan capturedCall, 1)
	librarian := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls <- capturedCall{path: r.URL.Path, auth: r.Header.Get("Authorization")}
		w.WriteHeader(http.StatusOK)
	}))
	defer librarian.Close()

	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      librarian.URL,
		LibrarianInstance: "inst-42",
		ChatSecret:        "topsecret-chat",
		WebhookSecret:     "topsecret-webhook",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/librarian/association", nil)
	for _, c := range loginAsHelper(t, srv, admin.ID) {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}

	// Local row gone.
	got, err := backend.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil association after DELETE, got %+v", got)
	}

	// Remote was notified.
	select {
	case call := <-calls:
		if call.path != "/instances/inst-42/forget" {
			t.Errorf("forget called with unexpected path: %q", call.path)
		}
		if call.auth != "Bearer topsecret-chat" {
			t.Errorf("forget called with wrong auth header: %q", call.auth)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("librarian forget endpoint was not hit within 3s")
	}
}

// TestHandleAPILibrarianDeleteAssociation_IdempotentWhenAbsent verifies
// DELETE returns 204 even when nothing is paired, with no outgoing call.
func TestHandleAPILibrarianDeleteAssociation_IdempotentWhenAbsent(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	admin, err := backend.CreateUser("Admin", "#000", true, false, 0)
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/librarian/association", nil)
	for _, c := range loginAsHelper(t, srv, admin.ID) {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
}

// TestHandleAPILibrarianDeleteAssociation_RemoteFailureStillSucceeds
// verifies that a slow / failing remote does not bubble up to the caller
// (the local row was cleared, that's authoritative).
func TestHandleAPILibrarianDeleteAssociation_RemoteFailureStillSucceeds(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	admin, err := backend.CreateUser("Admin", "#000", true, false, 0)
	if err != nil {
		t.Fatalf("admin: %v", err)
	}

	// Swap the outgoing client for one that always returns an error.  The
	// stub signals via `done` so we can wait for the goroutine to complete
	// before restoring the global (avoiding a real network call escaping
	// the test).
	prev := librarianForgetClient
	defer func() { librarianForgetClient = prev }()
	done := make(chan struct{}, 1)
	librarianForgetClient = func(req *http.Request) (*http.Response, error) {
		done <- struct{}{}
		return nil, errors.New("synthetic network failure")
	}

	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://unreachable.example",
		LibrarianInstance: "inst",
		ChatSecret:        "tok",
		WebhookSecret:     "hook",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/librarian/association", nil)
	for _, c := range loginAsHelper(t, srv, admin.ID) {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204 despite remote failure, got %d: %s", rr.Code, rr.Body.String())
	}
	got, _ := backend.Get()
	if got != nil {
		t.Errorf("local row should be cleared even when remote fails, got %+v", got)
	}

	// Wait for the goroutine so the synthetic stub stays in scope.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("forget goroutine did not complete in time")
	}
}

// ---- POST /api/librarian/rotate -----------------------------------------

// TestHandleAPILibrarianRotate_HappyPath verifies the rotate endpoint:
// with a matching X-Librarian-Chat-Secret it returns fresh secrets and
// persists them (CreatedAt preserved, URL / instance untouched).
func TestHandleAPILibrarianRotate_HappyPath(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://librarian.example",
		LibrarianInstance: "inst-rot",
		ChatSecret:        "old-chat-secret",
		WebhookSecret:     "old-webhook-secret",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before, _ := backend.Get()
	createdAt := before.CreatedAt.Unix()
	// Sleep so UpdatedAt strictly advances (Unix-second resolution).
	time.Sleep(1100 * time.Millisecond)

	req := httptest.NewRequest(http.MethodPost, "/api/librarian/rotate", nil)
	req.Header.Set("X-Librarian-Chat-Secret", "old-chat-secret")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		ChatSecret    string `json:"chat_secret"`
		WebhookSecret string `json:"webhook_secret"`
		Instance      string `json:"instance"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.ChatSecret) != 64 || len(resp.WebhookSecret) != 64 {
		t.Errorf("secrets should be 64 hex chars: chat=%d webhook=%d",
			len(resp.ChatSecret), len(resp.WebhookSecret))
	}
	if resp.ChatSecret == "old-chat-secret" || resp.WebhookSecret == "old-webhook-secret" {
		t.Errorf("rotation did not actually mint new secrets")
	}
	if resp.ChatSecret == resp.WebhookSecret {
		t.Errorf("the two new secrets must differ")
	}
	if resp.Instance != "inst-rot" {
		t.Errorf("instance should be preserved: got %q", resp.Instance)
	}

	// Persisted state matches the response, and only the two secret fields +
	// UpdatedAt changed; URL / instance / CreatedAt stayed.
	after, _ := backend.Get()
	if after == nil {
		t.Fatal("association should still exist after rotate")
	}
	if after.ChatSecret != resp.ChatSecret || after.WebhookSecret != resp.WebhookSecret {
		t.Errorf("persisted secrets diverge from response")
	}
	if after.LibrarianURL != before.LibrarianURL || after.LibrarianInstance != before.LibrarianInstance {
		t.Errorf("URL/instance should not change: %+v vs %+v", before, after)
	}
	if after.CreatedAt.Unix() != createdAt {
		t.Errorf("CreatedAt should be preserved on rotate: was %d, now %d",
			createdAt, after.CreatedAt.Unix())
	}
	if after.UpdatedAt.Unix() <= createdAt {
		t.Errorf("UpdatedAt should advance after rotate (createdAt=%d updatedAt=%d)",
			createdAt, after.UpdatedAt.Unix())
	}

	// The old chat_secret is now stale — a second rotation with it must 401.
	req2 := httptest.NewRequest(http.MethodPost, "/api/librarian/rotate", nil)
	req2.Header.Set("X-Librarian-Chat-Secret", "old-chat-secret")
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusUnauthorized {
		t.Errorf("old secret should not work after rotation, got %d", rr2.Code)
	}

	// The new chat_secret rotates again successfully.
	req3 := httptest.NewRequest(http.MethodPost, "/api/librarian/rotate", nil)
	req3.Header.Set("X-Librarian-Chat-Secret", resp.ChatSecret)
	rr3 := httptest.NewRecorder()
	srv.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusOK {
		t.Errorf("new secret should work, got %d: %s", rr3.Code, rr3.Body.String())
	}
}

// TestHandleAPILibrarianRotate_MissingHeader verifies absence of the chat
// secret header yields 401.
func TestHandleAPILibrarianRotate_MissingHeader(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://librarian.example",
		LibrarianInstance: "i",
		ChatSecret:        "the-secret",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/librarian/rotate", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no header, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleAPILibrarianRotate_WrongHeader verifies a mismatched chat secret
// yields 401 and does NOT mutate the stored association.
func TestHandleAPILibrarianRotate_WrongHeader(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://librarian.example",
		LibrarianInstance: "i",
		ChatSecret:        "the-real-secret",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/librarian/rotate", nil)
	req.Header.Set("X-Librarian-Chat-Secret", "wrong-secret")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong header, got %d: %s", rr.Code, rr.Body.String())
	}
	after, _ := backend.Get()
	if after == nil || after.ChatSecret != "the-real-secret" {
		t.Errorf("association mutated despite 401: %+v", after)
	}
}

// TestHandleAPILibrarianRotate_NoAssociation verifies 404 when nothing is
// currently paired.
func TestHandleAPILibrarianRotate_NoAssociation(t *testing.T) {
	srv, _, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	req := httptest.NewRequest(http.MethodPost, "/api/librarian/rotate", nil)
	req.Header.Set("X-Librarian-Chat-Secret", "anything")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when unpaired, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleAPILibrarianRotate_PublicRoute verifies the rotate endpoint is
// reachable WITHOUT a session cookie / OPDS token — the librarian holds
// only the chat secret.
func TestHandleAPILibrarianRotate_PublicRoute(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw", OPDSToken: "shared-tok"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://librarian.example",
		LibrarianInstance: "i",
		ChatSecret:        "valid",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/librarian/rotate", nil)
	req.Header.Set("X-Librarian-Chat-Secret", "valid")
	// No cookies, no ?token=, no Basic Auth — only the header.
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("rotate must be reachable without session/OPDS auth, got %d: %s",
			rr.Code, rr.Body.String())
	}
}

// ---- POST /api/ai/chat (librarian SSE relay) -----------------------------

// TestHandleAPILibrarianChat_NoAssociationReturns404 verifies that the
// endpoint short-circuits with 404 (so the SPA hides the chat box) when no
// librarian is currently paired.
func TestHandleAPILibrarianChat_NoAssociationReturns404(t *testing.T) {
	srv, _, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	req := httptest.NewRequest(http.MethodPost, "/api/ai/chat",
		strings.NewReader(`{"message":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("", "pw")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 with no association, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleAPILibrarianChat_RelaysSSEResponse verifies the full relay flow:
// the upstream librarian's /chat endpoint receives the inbound body + Bearer
// chat_secret, and its text/event-stream body is forwarded to the client
// verbatim (including the Content-Type header).
func TestHandleAPILibrarianChat_RelaysSSEResponse(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})

	// Fake librarian: records the inbound body+auth, then emits a
	// multi-frame SSE payload.
	type capturedCall struct {
		method string
		path   string
		auth   string
		body   string
	}
	var got capturedCall
	librarian := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.path = r.URL.Path
		got.auth = r.Header.Get("Authorization")
		bodyBytes, _ := io.ReadAll(r.Body)
		got.body = string(bodyBytes)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: text\ndata: hello\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("event: done\ndata: {}\n\n"))
	}))
	defer librarian.Close()

	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      librarian.URL,
		LibrarianInstance: "inst",
		ChatSecret:        "the-bearer-secret",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/ai/chat",
		strings.NewReader(`{"message":"bonjour","history":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("", "pw")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if got.method != http.MethodPost {
		t.Errorf("upstream method = %q, want POST", got.method)
	}
	if got.path != "/chat" {
		t.Errorf("upstream path = %q, want /chat", got.path)
	}
	if got.auth != "Bearer the-bearer-secret" {
		t.Errorf("upstream Authorization = %q", got.auth)
	}
	if got.body != `{"message":"bonjour","history":[]}` {
		t.Errorf("upstream body = %q", got.body)
	}

	if ct := rr.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("relay Content-Type = %q, want text/event-stream", ct)
	}
	if ab := rr.Header().Get("X-Accel-Buffering"); ab != "no" {
		t.Errorf("X-Accel-Buffering = %q, want \"no\"", ab)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "event: text") || !strings.Contains(body, "data: hello") {
		t.Errorf("relayed body missing the text frame: %q", body)
	}
	if !strings.Contains(body, "event: done") {
		t.Errorf("relayed body missing the done frame: %q", body)
	}
}

// TestHandleAPILibrarianChat_UpstreamFailureBadGateway verifies that a
// transport-level failure surfaces as 502 Bad Gateway (not a generic 500).
func TestHandleAPILibrarianChat_UpstreamFailureBadGateway(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "http://unreachable.example",
		LibrarianInstance: "inst",
		ChatSecret:        "tok",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	prev := librarianChatClient
	defer func() { librarianChatClient = prev }()
	librarianChatClient = func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("synthetic dial failure")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/ai/chat",
		strings.NewReader(`{"message":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("", "pw")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleAPILibrarianChat_PropagatesUpstreamStatus verifies a non-2xx
// upstream status is mirrored back to the client (e.g. librarian returns
// 503 → client sees 503).
func TestHandleAPILibrarianChat_PropagatesUpstreamStatus(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})

	librarian := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"librarian busy"}`))
	}))
	defer librarian.Close()

	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      librarian.URL,
		LibrarianInstance: "inst",
		ChatSecret:        "tok",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/ai/chat",
		strings.NewReader(`{"message":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("", "pw")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected upstream 503 mirrored, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "librarian busy") {
		t.Errorf("upstream body not forwarded: %q", rr.Body.String())
	}
}

// ---- /api/config librarianEnabled --------------------------------------

// TestAPIConfig_LibrarianEnabled_FalseWithoutAssociation verifies that
// /api/config reports librarianEnabled=false on a fresh server, so the SPA
// hides the chat box until pairing happens.
func TestAPIConfig_LibrarianEnabled_FalseWithoutAssociation(t *testing.T) {
	srv, _, _ := newSQLiteTestServer(t, Options{Password: "pw"})

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.SetBasicAuth("", "pw")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		LibrarianEnabled bool `json:"librarianEnabled"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.LibrarianEnabled {
		t.Errorf("librarianEnabled should be false when no association exists")
	}

	// Defence-in-depth: the deprecated key must NOT appear in the response
	// (otherwise an old SPA build could keep showing the chat button).
	if strings.Contains(rr.Body.String(), `"aiEnabled"`) {
		t.Errorf("legacy aiEnabled key should be gone: %s", rr.Body.String())
	}
}

// TestAPIConfig_LibrarianEnabled_TrueAfterPairing verifies that
// /api/config flips librarianEnabled to true once an association is stored.
func TestAPIConfig_LibrarianEnabled_TrueAfterPairing(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://librarian.example",
		LibrarianInstance: "inst",
		ChatSecret:        "c",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.SetBasicAuth("", "pw")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		LibrarianEnabled bool `json:"librarianEnabled"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.LibrarianEnabled {
		t.Errorf("librarianEnabled should be true once paired")
	}
}

// ---- POST /api/librarian/forget (inbound) -------------------------------

// TestHandleAPILibrarianForget_HappyPath verifies that with a matching
// X-Librarian-Chat-Secret header the association is cleared and 204 is
// returned.
func TestHandleAPILibrarianForget_HappyPath(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://librarian.example",
		LibrarianInstance: "inst",
		ChatSecret:        "the-secret",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/librarian/forget", nil)
	req.Header.Set("X-Librarian-Chat-Secret", "the-secret")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
	got, _ := backend.Get()
	if got != nil {
		t.Errorf("expected association cleared, got %+v", got)
	}
}

// TestHandleAPILibrarianForget_IdempotentWhenAlreadyGone verifies the
// librarian can retry forget after a previous success: with the header
// still present, a missing association returns 204 too.
func TestHandleAPILibrarianForget_IdempotentWhenAlreadyGone(t *testing.T) {
	srv, _, _ := newSQLiteTestServer(t, Options{Password: "pw"})

	req := httptest.NewRequest(http.MethodPost, "/api/librarian/forget", nil)
	req.Header.Set("X-Librarian-Chat-Secret", "anything")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204 when nothing to forget, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleAPILibrarianForget_WrongSecret verifies a mismatched secret is
// rejected with 401 and the association is preserved.
func TestHandleAPILibrarianForget_WrongSecret(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://librarian.example",
		LibrarianInstance: "inst",
		ChatSecret:        "the-real-secret",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/librarian/forget", nil)
	req.Header.Set("X-Librarian-Chat-Secret", "wrong")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body.String())
	}
	got, _ := backend.Get()
	if got == nil || got.ChatSecret != "the-real-secret" {
		t.Errorf("association mutated despite 401: %+v", got)
	}
}

// TestHandleAPILibrarianForget_MissingHeader verifies that an
// unauthenticated probe (no header at all) is rejected even when nothing
// is paired — refusing to act as a "is anyone paired?" oracle.
func TestHandleAPILibrarianForget_MissingHeader(t *testing.T) {
	srv, _, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	req := httptest.NewRequest(http.MethodPost, "/api/librarian/forget", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no header, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleAPILibrarianForget_PublicRoute verifies the endpoint is
// reachable WITHOUT a session cookie / OPDS token — the librarian holds
// only the chat secret.
func TestHandleAPILibrarianForget_PublicRoute(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw", OPDSToken: "shared-tok"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://librarian.example",
		LibrarianInstance: "i",
		ChatSecret:        "valid",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/librarian/forget", nil)
	req.Header.Set("X-Librarian-Chat-Secret", "valid")
	// No cookies, no ?token=, no Basic Auth — only the header.
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("forget must be reachable without session/OPDS auth, got %d: %s",
			rr.Code, rr.Body.String())
	}
}

// ---- POST /api/librarian/announce (inbound) -----------------------------

// TestHandleAPILibrarianAnnounce_HappyPath verifies that with a matching
// X-Librarian-Chat-Secret header and a well-formed librarian_url the stored
// LibrarianURL is updated, all other fields (instance, both secrets,
// CreatedAt) are preserved, and only UpdatedAt advances.
func TestHandleAPILibrarianAnnounce_HappyPath(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://old.example",
		LibrarianInstance: "inst-announce",
		ChatSecret:        "the-secret",
		WebhookSecret:     "the-webhook",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before, _ := backend.Get()
	createdAt := before.CreatedAt.Unix()
	// Sleep so UpdatedAt strictly advances (Unix-second resolution).
	time.Sleep(1100 * time.Millisecond)

	req := httptest.NewRequest(http.MethodPost, "/api/librarian/announce",
		strings.NewReader(`{"librarian_url":"http://new.example:9090"}`))
	req.Header.Set("X-Librarian-Chat-Secret", "the-secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}

	after, _ := backend.Get()
	if after == nil {
		t.Fatal("association should still exist after announce")
	}
	if after.LibrarianURL != "http://new.example:9090" {
		t.Errorf("LibrarianURL should be updated: got %q", after.LibrarianURL)
	}
	if after.LibrarianInstance != "inst-announce" {
		t.Errorf("instance should be preserved: got %q", after.LibrarianInstance)
	}
	if after.ChatSecret != "the-secret" {
		t.Errorf("chat_secret should be preserved: got %q", after.ChatSecret)
	}
	if after.WebhookSecret != "the-webhook" {
		t.Errorf("webhook_secret should be preserved: got %q", after.WebhookSecret)
	}
	if after.CreatedAt.Unix() != createdAt {
		t.Errorf("CreatedAt should be preserved: was %d, now %d",
			createdAt, after.CreatedAt.Unix())
	}
	if after.UpdatedAt.Unix() <= createdAt {
		t.Errorf("UpdatedAt should advance after announce (createdAt=%d updatedAt=%d)",
			createdAt, after.UpdatedAt.Unix())
	}
}

// TestHandleAPILibrarianAnnounce_TrimsTrailingSlash verifies the handler
// strips a trailing slash from the inbound URL so callers can be sloppy
// without producing double-slash webhook URLs later.
func TestHandleAPILibrarianAnnounce_TrimsTrailingSlash(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://old.example",
		LibrarianInstance: "i",
		ChatSecret:        "s",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/librarian/announce",
		strings.NewReader(`{"librarian_url":"https://new.example/  "}`))
	req.Header.Set("X-Librarian-Chat-Secret", "s")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
	after, _ := backend.Get()
	if after == nil || after.LibrarianURL != "https://new.example" {
		t.Errorf("expected trailing slash trimmed, got %q", after.LibrarianURL)
	}
}

// TestHandleAPILibrarianAnnounce_Idempotent verifies that announcing the
// same URL twice does not break anything (subsequent POST still 204) and
// the second call still updates UpdatedAt (announce is a heartbeat-ish
// signal; an exactly-same payload is not a hard no-op).
func TestHandleAPILibrarianAnnounce_Idempotent(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "http://lib.example:9090",
		LibrarianInstance: "inst",
		ChatSecret:        "the-secret",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	body := `{"librarian_url":"http://lib.example:9090"}`

	for i, label := range []string{"first", "second"} {
		req := httptest.NewRequest(http.MethodPost, "/api/librarian/announce",
			strings.NewReader(body))
		req.Header.Set("X-Librarian-Chat-Secret", "the-secret")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("call %d (%s): expected 204, got %d: %s", i, label, rr.Code, rr.Body.String())
		}
	}
	after, _ := backend.Get()
	if after == nil || after.LibrarianURL != "http://lib.example:9090" {
		t.Errorf("URL should be unchanged after idempotent announce, got %+v", after)
	}
	if after.ChatSecret != "the-secret" {
		t.Errorf("chat_secret should not have rotated: %q", after.ChatSecret)
	}
}

// TestHandleAPILibrarianAnnounce_MissingHeader verifies absence of the chat
// secret header yields 401 and the stored association is untouched.
func TestHandleAPILibrarianAnnounce_MissingHeader(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://before.example",
		LibrarianInstance: "i",
		ChatSecret:        "the-secret",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/librarian/announce",
		strings.NewReader(`{"librarian_url":"https://new.example"}`))
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no header, got %d: %s", rr.Code, rr.Body.String())
	}
	got, _ := backend.Get()
	if got == nil || got.LibrarianURL != "https://before.example" {
		t.Errorf("association mutated despite 401: %+v", got)
	}
}

// TestHandleAPILibrarianAnnounce_WrongHeader verifies a mismatched secret
// yields 401 and the stored association is preserved verbatim.
func TestHandleAPILibrarianAnnounce_WrongHeader(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://before.example",
		LibrarianInstance: "i",
		ChatSecret:        "the-real-secret",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/librarian/announce",
		strings.NewReader(`{"librarian_url":"https://new.example"}`))
	req.Header.Set("X-Librarian-Chat-Secret", "wrong")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong header, got %d: %s", rr.Code, rr.Body.String())
	}
	got, _ := backend.Get()
	if got == nil || got.LibrarianURL != "https://before.example" {
		t.Errorf("association mutated despite 401: %+v", got)
	}
}

// TestHandleAPILibrarianAnnounce_NoAssociation verifies 404 when nothing is
// currently paired.
func TestHandleAPILibrarianAnnounce_NoAssociation(t *testing.T) {
	srv, _, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	req := httptest.NewRequest(http.MethodPost, "/api/librarian/announce",
		strings.NewReader(`{"librarian_url":"https://new.example"}`))
	req.Header.Set("X-Librarian-Chat-Secret", "anything")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when unpaired, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleAPILibrarianAnnounce_BadURL verifies missing / malformed URLs
// are rejected with 400 and the stored association is untouched.
func TestHandleAPILibrarianAnnounce_BadURL(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"missing field", `{}`},
		{"empty string", `{"librarian_url":""}`},
		{"whitespace only", `{"librarian_url":"   "}`},
		{"no scheme", `{"librarian_url":"new.example:9090"}`},
		{"relative path", `{"librarian_url":"/instances/foo"}`},
		{"wrong scheme", `{"librarian_url":"ftp://new.example"}`},
		{"malformed", `{"librarian_url":"http://%zz"}`},
		{"invalid json", `{"librarian_url":`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
			if err := backend.Set(catalog.LibrarianAssociationData{
				LibrarianURL:      "https://before.example",
				LibrarianInstance: "i",
				ChatSecret:        "the-secret",
				WebhookSecret:     "w",
			}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/librarian/announce",
				strings.NewReader(tc.body))
			req.Header.Set("X-Librarian-Chat-Secret", "the-secret")
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
			}
			got, _ := backend.Get()
			if got == nil || got.LibrarianURL != "https://before.example" {
				t.Errorf("association mutated on 400: %+v", got)
			}
		})
	}
}

// TestHandleAPILibrarianAnnounce_PublicRoute verifies the endpoint is
// reachable WITHOUT a session cookie / OPDS token — the librarian holds
// only the chat secret.
func TestHandleAPILibrarianAnnounce_PublicRoute(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw", OPDSToken: "shared-tok"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://before.example",
		LibrarianInstance: "i",
		ChatSecret:        "valid",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/librarian/announce",
		strings.NewReader(`{"librarian_url":"http://new.example:9090"}`))
	req.Header.Set("X-Librarian-Chat-Secret", "valid")
	// No cookies, no ?token=, no Basic Auth — only the header.
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("announce must be reachable without session/OPDS auth, got %d: %s",
			rr.Code, rr.Body.String())
	}
}

// ---- POST /api/librarian/heartbeat (inbound) ----------------------------

// TestHandleAPILibrarianHeartbeat_HappyPath verifies a heartbeat with the
// correct chat secret returns 204, stamps LastSeenAt, and does NOT advance
// UpdatedAt (a heartbeat is not a mutation).  URL, instance and both
// secrets are also preserved verbatim.
func TestHandleAPILibrarianHeartbeat_HappyPath(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://librarian.example",
		LibrarianInstance: "inst-hb",
		ChatSecret:        "the-secret",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before, _ := backend.Get()
	if !before.LastSeenAt.IsZero() {
		t.Fatalf("seed should have zero LastSeenAt, got %v", before.LastSeenAt)
	}
	updatedAtBefore := before.UpdatedAt.Unix()
	// Sleep so a (would-be) UpdatedAt change would be observable at second resolution.
	time.Sleep(1100 * time.Millisecond)

	req := httptest.NewRequest(http.MethodPost, "/api/librarian/heartbeat",
		strings.NewReader(`{"version":"v0.1"}`))
	req.Header.Set("X-Librarian-Chat-Secret", "the-secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}

	after, _ := backend.Get()
	if after == nil {
		t.Fatal("association should still exist after heartbeat")
	}
	if after.LastSeenAt.IsZero() {
		t.Errorf("LastSeenAt should be stamped after heartbeat")
	}
	if after.UpdatedAt.Unix() != updatedAtBefore {
		t.Errorf("UpdatedAt must NOT advance on heartbeat: was %d, now %d",
			updatedAtBefore, after.UpdatedAt.Unix())
	}
	if after.LibrarianURL != "https://librarian.example" || after.LibrarianInstance != "inst-hb" {
		t.Errorf("URL/instance changed: %+v", after)
	}
	if after.ChatSecret != "the-secret" || after.WebhookSecret != "w" {
		t.Errorf("secrets changed on heartbeat: chat=%q webhook=%q",
			after.ChatSecret, after.WebhookSecret)
	}
}

// TestHandleAPILibrarianHeartbeat_NoBodyAccepted verifies an empty body is
// accepted (the version field is optional / ignored).
func TestHandleAPILibrarianHeartbeat_NoBodyAccepted(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://librarian.example",
		LibrarianInstance: "i",
		ChatSecret:        "s",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/librarian/heartbeat", nil)
	req.Header.Set("X-Librarian-Chat-Secret", "s")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204 with empty body, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleAPILibrarianHeartbeat_Idempotent verifies repeated heartbeats
// keep returning 204 and only update LastSeenAt.
func TestHandleAPILibrarianHeartbeat_Idempotent(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://librarian.example",
		LibrarianInstance: "i",
		ChatSecret:        "s",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/librarian/heartbeat", nil)
		req.Header.Set("X-Librarian-Chat-Secret", "s")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("call %d: expected 204, got %d: %s", i, rr.Code, rr.Body.String())
		}
	}
	after, _ := backend.Get()
	if after == nil || after.LastSeenAt.IsZero() {
		t.Errorf("LastSeenAt should be set after repeated heartbeats: %+v", after)
	}
}

// TestHandleAPILibrarianHeartbeat_MissingHeader verifies absence of the
// chat secret header yields 401 and LastSeenAt stays untouched.
func TestHandleAPILibrarianHeartbeat_MissingHeader(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://librarian.example",
		LibrarianInstance: "i",
		ChatSecret:        "s",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/librarian/heartbeat", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no header, got %d: %s", rr.Code, rr.Body.String())
	}
	after, _ := backend.Get()
	if after == nil || !after.LastSeenAt.IsZero() {
		t.Errorf("LastSeenAt should be unchanged on 401: %+v", after)
	}
}

// TestHandleAPILibrarianHeartbeat_WrongHeader verifies a mismatched secret
// yields 401 and LastSeenAt is not updated.
func TestHandleAPILibrarianHeartbeat_WrongHeader(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://librarian.example",
		LibrarianInstance: "i",
		ChatSecret:        "real",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/librarian/heartbeat", nil)
	req.Header.Set("X-Librarian-Chat-Secret", "wrong")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong header, got %d: %s", rr.Code, rr.Body.String())
	}
	after, _ := backend.Get()
	if after == nil || !after.LastSeenAt.IsZero() {
		t.Errorf("LastSeenAt should be unchanged on 401: %+v", after)
	}
}

// TestHandleAPILibrarianHeartbeat_NoAssociation verifies 404 when nothing is
// currently paired (consistent with /rotate, /announce).
func TestHandleAPILibrarianHeartbeat_NoAssociation(t *testing.T) {
	srv, _, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	req := httptest.NewRequest(http.MethodPost, "/api/librarian/heartbeat", nil)
	req.Header.Set("X-Librarian-Chat-Secret", "anything")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when unpaired, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleAPILibrarianHeartbeat_PublicRoute verifies the endpoint is
// reachable WITHOUT a session cookie / OPDS token — the librarian holds
// only the chat secret.
func TestHandleAPILibrarianHeartbeat_PublicRoute(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw", OPDSToken: "shared-tok"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://librarian.example",
		LibrarianInstance: "i",
		ChatSecret:        "valid",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/librarian/heartbeat", nil)
	req.Header.Set("X-Librarian-Chat-Secret", "valid")
	// No cookies, no ?token=, no Basic Auth — only the header.
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("heartbeat must be reachable without session/OPDS auth, got %d: %s",
			rr.Code, rr.Body.String())
	}
}

// TestHandleAPILibrarianHeartbeat_BadJSONBody verifies a malformed JSON body
// is rejected with 400 (auth still happened first; the body is decoded).
func TestHandleAPILibrarianHeartbeat_BadJSONBody(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if err := backend.Set(catalog.LibrarianAssociationData{
		LibrarianURL:      "https://librarian.example",
		LibrarianInstance: "i",
		ChatSecret:        "s",
		WebhookSecret:     "w",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/librarian/heartbeat",
		strings.NewReader(`{"version":`))
	req.Header.Set("X-Librarian-Chat-Secret", "s")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on malformed JSON, got %d: %s", rr.Code, rr.Body.String())
	}
}
