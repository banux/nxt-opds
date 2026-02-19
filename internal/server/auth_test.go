package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	fsbackend "github.com/banux/nxt-opds/internal/backend/fs"
)

// newTestServer creates a Server backed by an empty temp-dir backend.
func newTestServer(t *testing.T, opts Options) *Server {
	t.Helper()
	dir := t.TempDir()
	backend, err := fsbackend.New(dir)
	if err != nil {
		t.Fatalf("backend.New: %v", err)
	}
	return New(backend, opts)
}

func TestAuth_Disabled(t *testing.T) {
	// When no password is set, all requests should succeed without credentials.
	srv := newTestServer(t, Options{Password: ""})

	req := httptest.NewRequest(http.MethodGet, "/opds", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestAuth_MissingCredentials_OPDS(t *testing.T) {
	// OPDS requests (no HTML Accept) without credentials → 401, not redirect.
	srv := newTestServer(t, Options{Password: "secret"})

	req := httptest.NewRequest(http.MethodGet, "/opds", nil)
	// Mimic an OPDS reader: explicitly request XML, not HTML.
	req.Header.Set("Accept", "application/atom+xml")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
	if rr.Header().Get("WWW-Authenticate") == "" {
		t.Error("expected WWW-Authenticate header, got none")
	}
}

func TestAuth_MissingCredentials_Browser(t *testing.T) {
	// Browser requests to a protected non-OPDS path without credentials → redirect to /login.
	srv := newTestServer(t, Options{Password: "secret"})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect to /login, got %d", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/login" {
		t.Errorf("expected Location: /login, got %q", loc)
	}
}

func TestAuth_WrongPassword_BasicAuth(t *testing.T) {
	// Basic Auth with wrong password → 401.
	srv := newTestServer(t, Options{Password: "secret"})

	req := httptest.NewRequest(http.MethodGet, "/opds", nil)
	req.SetBasicAuth("user", "wrong")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestAuth_CorrectPassword_BasicAuth(t *testing.T) {
	// OPDS clients can still use HTTP Basic Auth.
	srv := newTestServer(t, Options{Password: "secret"})

	req := httptest.NewRequest(http.MethodGet, "/opds", nil)
	req.SetBasicAuth("anyuser", "secret")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestAuth_HealthAlwaysPublic(t *testing.T) {
	// /health must be reachable without credentials even when auth is enabled.
	srv := newTestServer(t, Options{Password: "secret"})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for /health without auth, got %d", rr.Code)
	}
}

func TestAuth_LoginPage_Public(t *testing.T) {
	// GET /login must be reachable without auth (serves the login form).
	srv := newTestServer(t, Options{Password: "secret"})

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for GET /login, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("expected text/html content type, got %q", ct)
	}
}

func TestAuth_LoginPost_WrongPassword(t *testing.T) {
	// POST /login with wrong password → 401 and re-renders the form.
	srv := newTestServer(t, Options{Password: "secret"})

	form := url.Values{"password": {"wrong"}, "redirect": {"/"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 on wrong password, got %d", rr.Code)
	}
}

func TestAuth_LoginPost_CorrectPassword(t *testing.T) {
	// POST /login with correct password → sets session cookie and redirects.
	srv := newTestServer(t, Options{Password: "secret"})

	form := url.Values{"password": {"secret"}, "redirect": {"/"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect after login, got %d", rr.Code)
	}

	// Must set a session cookie.
	var sessionCookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected session cookie to be set, got none")
	}
	if sessionCookie.Value == "" {
		t.Error("session cookie value must not be empty")
	}
}

func TestAuth_SessionCookie_GrantsAccess(t *testing.T) {
	// A valid session cookie grants access to protected routes.
	srv := newTestServer(t, Options{Password: "secret"})

	// Login to get a session token.
	token, err := srv.sessions.create()
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/opds", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 with valid session cookie, got %d", rr.Code)
	}
}

func TestAuth_Logout_ClearsSession(t *testing.T) {
	// POST /logout must invalidate the session and redirect to /login.
	srv := newTestServer(t, Options{Password: "secret"})

	token, err := srv.sessions.create()
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Verify the token is valid before logout.
	if !srv.sessions.valid(token) {
		t.Fatal("token should be valid before logout")
	}

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect after logout, got %d", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/login" {
		t.Errorf("expected redirect to /login, got %q", loc)
	}

	// Token must be invalidated.
	if srv.sessions.valid(token) {
		t.Error("session token should be invalid after logout")
	}
}

func TestAuth_UsernameIgnored_BasicAuth(t *testing.T) {
	// Any username is accepted via Basic Auth as long as the password is correct.
	srv := newTestServer(t, Options{Password: "mypass"})

	for _, user := range []string{"alice", "bob", "", "admin"} {
		req := httptest.NewRequest(http.MethodGet, "/opds", nil)
		req.SetBasicAuth(user, "mypass")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("user=%q: expected 200, got %d", user, rr.Code)
		}
	}
}
