package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
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
