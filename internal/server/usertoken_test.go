package server

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/banux/nxt-opds/internal/opds"
)

// TestUserToken_Generated verifies that CreateUser auto-generates a token.
func TestUserToken_Generated(t *testing.T) {
	_, backend, _ := newSQLiteTestServer(t, Options{})
	u, err := backend.CreateUser("Alice", "#ff0000", false, false, 0)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if len(u.Token) != 64 {
		t.Errorf("expected 64-char hex token, got %q (len=%d)", u.Token, len(u.Token))
	}
}

// TestUserByToken_RoundTrip verifies that UserByToken returns the user that
// owns a freshly created token.
func TestUserByToken_RoundTrip(t *testing.T) {
	_, backend, _ := newSQLiteTestServer(t, Options{})
	a, err := backend.CreateUser("Alice", "#ff0000", false, false, 0)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	got, err := backend.UserByToken(a.Token)
	if err != nil {
		t.Fatalf("UserByToken: %v", err)
	}
	if got.ID != a.ID {
		t.Errorf("expected user %s, got %s", a.ID, got.ID)
	}
}

// TestRegenerateUserToken_InvalidatesOld verifies that regenerating a user's
// token returns a different token and that the old one no longer authenticates.
func TestRegenerateUserToken_InvalidatesOld(t *testing.T) {
	_, backend, _ := newSQLiteTestServer(t, Options{})
	a, err := backend.CreateUser("Alice", "#ff0000", false, false, 0)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	oldTok := a.Token
	updated, err := backend.RegenerateUserToken(a.ID)
	if err != nil {
		t.Fatalf("RegenerateUserToken: %v", err)
	}
	if updated.Token == oldTok {
		t.Errorf("token unchanged after regeneration")
	}
	if _, err := backend.UserByToken(oldTok); err == nil {
		t.Errorf("old token should no longer authenticate")
	}
	if _, err := backend.UserByToken(updated.Token); err != nil {
		t.Errorf("new token should authenticate: %v", err)
	}
}

// TestPerUserToken_AuthenticatesAndSetsContext verifies that the auth
// middleware accepts a per-user token on OPDS routes and populates the
// request context with the matching user ID, so /opds/to-read returns that
// user's pile without ?user= being passed.
func TestPerUserToken_AuthenticatesAndSetsContext(t *testing.T) {
	srv, backend, bookID := newSQLiteTestServer(t, Options{Password: "pw", OPDSToken: "shared"})
	alice, err := backend.CreateUser("Alice", "#ff0000", false, false, 0)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := backend.AddToReadList(alice.ID, bookID); err != nil {
		t.Fatalf("AddToReadList: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/opds/to-read?token="+alice.Token, nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with per-user token, got %d: %s", rr.Code, rr.Body.String())
	}
	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	if len(feed.Entries) != 1 {
		t.Errorf("expected 1 entry in Alice's pile, got %d", len(feed.Entries))
	}
}

// TestPerUserToken_RecommendationsScopedToUser verifies that the OPDS
// recommendations feed only returns books recommended TO the authenticated
// user, not the global cross-user list.
func TestPerUserToken_RecommendationsScopedToUser(t *testing.T) {
	srv, backend, bookID := newSQLiteTestServer(t, Options{Password: "pw", OPDSToken: "shared"})
	alice, err := backend.CreateUser("Alice", "#ff0000", false, false, 0)
	if err != nil {
		t.Fatalf("CreateUser Alice: %v", err)
	}
	bob, err := backend.CreateUser("Bob", "#00ff00", false, false, 0)
	if err != nil {
		t.Fatalf("CreateUser Bob: %v", err)
	}
	// Bob recommends the book to Alice.
	if err := backend.RecommendBook(bob.ID, alice.ID, bookID, "must read"); err != nil {
		t.Fatalf("RecommendBook: %v", err)
	}

	// Authenticate as Alice with her per-user token.
	req := httptest.NewRequest(http.MethodGet, "/opds/recommendations?token="+alice.Token, nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var feed opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	if len(feed.Entries) != 1 {
		t.Errorf("expected 1 recommendation for Alice, got %d", len(feed.Entries))
	}

	// Bob has no incoming recommendations, so his feed should be empty.
	req = httptest.NewRequest(http.MethodGet, "/opds/recommendations?token="+bob.Token, nil)
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for Bob, got %d", rr.Code)
	}
	var feedBob opds.Feed
	if err := xml.Unmarshal(rr.Body.Bytes(), &feedBob); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	if len(feedBob.Entries) != 0 {
		t.Errorf("expected 0 recommendations for Bob, got %d", len(feedBob.Entries))
	}
}

// TestAPIMe_IncludesToken verifies that /api/me exposes the per-user token
// for the authenticated session so the frontend can build personal feed URLs.
func TestAPIMe_IncludesToken(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw", OPDSToken: "shared"})
	alice, err := backend.CreateUser("Alice", "#ff0000", false, false, 0)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Authenticate with Alice's per-user token.
	req := httptest.NewRequest(http.MethodGet, "/api/me?token="+alice.Token, nil)
	// /api/me is not an OPDS path, so token query won't be picked up – use
	// a session cookie via the login flow instead.
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	// Without a session cookie this should be 401.
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without session cookie on /api/me, got %d", rr.Code)
	}

	// Now log in as Alice to obtain a session cookie.
	form := strings.NewReader("password=pw&user_id=" + alice.ID)
	loginReq := httptest.NewRequest(http.MethodPost, "/login", form)
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	srv.ServeHTTP(loginRR, loginReq)
	if loginRR.Code != http.StatusSeeOther && loginRR.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", loginRR.Code, loginRR.Body.String())
	}
	cookies := loginRR.Result().Cookies()

	meReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	for _, c := range cookies {
		meReq.AddCookie(c)
	}
	meRR := httptest.NewRecorder()
	srv.ServeHTTP(meRR, meReq)

	if meRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", meRR.Code, meRR.Body.String())
	}
	var me map[string]any
	if err := json.NewDecoder(meRR.Body).Decode(&me); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if me["token"] != alice.Token {
		t.Errorf("expected token %q, got %v", alice.Token, me["token"])
	}
}

// TestAPIRegenerateUserToken_AdminOnly verifies that only an administrator can
// invoke POST /api/users/{id}/token to rotate another user's token.
func TestAPIRegenerateUserToken_AdminOnly(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw", OPDSToken: "shared"})
	admin, err := backend.CreateUser("Admin", "#000000", true, false, 0)
	if err != nil {
		t.Fatalf("CreateUser admin: %v", err)
	}
	alice, err := backend.CreateUser("Alice", "#ff0000", false, false, 0)
	if err != nil {
		t.Fatalf("CreateUser alice: %v", err)
	}
	oldTok := alice.Token

	loginAs := func(uid string) []*http.Cookie {
		t.Helper()
		form := strings.NewReader("password=pw&user_id=" + uid)
		req := httptest.NewRequest(http.MethodPost, "/login", form)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr.Result().Cookies()
	}

	// Non-admin (Alice) tries to rotate her own token via the admin endpoint
	// → forbidden.
	req := httptest.NewRequest(http.MethodPost, "/api/users/"+alice.ID+"/token", nil)
	for _, c := range loginAs(alice.ID) {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d", rr.Code)
	}

	// Admin can rotate Alice's token.
	req = httptest.NewRequest(http.MethodPost, "/api/users/"+alice.ID+"/token", nil)
	for _, c := range loginAs(admin.ID) {
		req.AddCookie(c)
	}
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	newTok, _ := resp["token"].(string)
	if newTok == "" || newTok == oldTok {
		t.Errorf("expected fresh token, got %q (old %q)", newTok, oldTok)
	}
}
