package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// loginAsHelper logs a user in via POST /login and returns their session cookies.
func loginAsHelper(t *testing.T, srv *Server, uid string) []*http.Cookie {
	t.Helper()
	form := strings.NewReader("password=pw&user_id=" + uid)
	req := httptest.NewRequest(http.MethodPost, "/login", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr.Result().Cookies()
}

// TestAdminGate_CreateUser_NonAdminForbidden verifies that a non-admin cannot
// create new users (escalation vector — could promote themselves to admin).
func TestAdminGate_CreateUser_NonAdminForbidden(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if _, err := backend.CreateUser("Admin", "#000", true, false, 0); err != nil {
		t.Fatalf("admin: %v", err)
	}
	alice, err := backend.CreateUser("Alice", "#f00", false, false, 0)
	if err != nil {
		t.Fatalf("alice: %v", err)
	}

	body := bytes.NewBufferString(`{"name":"Eve","color":"#0f0","isAdmin":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/users", body)
	req.Header.Set("Content-Type", "application/json")
	for _, c := range loginAsHelper(t, srv, alice.ID) {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin should not create users; got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestAdminGate_UpdateUser_NonAdminForbidden verifies that a non-admin cannot
// update other users' roles (escalation vector).
func TestAdminGate_UpdateUser_NonAdminForbidden(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if _, err := backend.CreateUser("Admin", "#000", true, false, 0); err != nil {
		t.Fatalf("admin: %v", err)
	}
	alice, err := backend.CreateUser("Alice", "#f00", false, false, 0)
	if err != nil {
		t.Fatalf("alice: %v", err)
	}

	// Alice tries to grant herself admin via PATCH.
	body := bytes.NewBufferString(`{"name":"Alice","color":"#f00","isAdmin":true}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/users/"+alice.ID, body)
	req.Header.Set("Content-Type", "application/json")
	for _, c := range loginAsHelper(t, srv, alice.ID) {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin should not update users; got %d: %s", rr.Code, rr.Body.String())
	}

	// Re-fetch alice to confirm she is still not admin.
	got, err := backend.UserByID(alice.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if got.IsAdmin {
		t.Fatalf("alice's IsAdmin should remain false")
	}
}

// TestAdminGate_DeleteUser_NonAdminForbidden verifies that a non-admin cannot
// delete other users.
func TestAdminGate_DeleteUser_NonAdminForbidden(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	admin, err := backend.CreateUser("Admin", "#000", true, false, 0)
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	alice, err := backend.CreateUser("Alice", "#f00", false, false, 0)
	if err != nil {
		t.Fatalf("alice: %v", err)
	}

	// Alice tries to delete admin.
	req := httptest.NewRequest(http.MethodDelete, "/api/users/"+admin.ID, nil)
	for _, c := range loginAsHelper(t, srv, alice.ID) {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin should not delete users; got %d", rr.Code)
	}
	// Admin still exists.
	if _, err := backend.UserByID(admin.ID); err != nil {
		t.Fatalf("admin should still exist: %v", err)
	}
}

// TestAdminGate_ChildCannotEscalate ensures a child profile cannot raise its
// own MaxAge (which would lift the age-rating filter).
func TestAdminGate_ChildCannotEscalate(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if _, err := backend.CreateUser("Admin", "#000", true, false, 0); err != nil {
		t.Fatalf("admin: %v", err)
	}
	kid, err := backend.CreateUser("Kid", "#0f0", false, true, 6)
	if err != nil {
		t.Fatalf("kid: %v", err)
	}

	body := bytes.NewBufferString(`{"name":"Kid","color":"#0f0","isAdmin":false,"isChild":true,"maxAge":18}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/users/"+kid.ID, body)
	req.Header.Set("Content-Type", "application/json")
	for _, c := range loginAsHelper(t, srv, kid.ID) {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("child should not patch own MaxAge; got %d", rr.Code)
	}
	got, err := backend.UserByID(kid.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if got.MaxAge != 6 {
		t.Fatalf("MaxAge should remain 6, got %d", got.MaxAge)
	}
}

// TestAdminGate_AdminAllowed verifies the gate lets admins through on the
// same routes that reject non-admins.
func TestAdminGate_AdminAllowed(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	admin, err := backend.CreateUser("Admin", "#000", true, false, 0)
	if err != nil {
		t.Fatalf("admin: %v", err)
	}

	body := bytes.NewBufferString(`{"name":"Bob","color":"#0ff","isAdmin":false}`)
	req := httptest.NewRequest(http.MethodPost, "/api/users", body)
	req.Header.Set("Content-Type", "application/json")
	for _, c := range loginAsHelper(t, srv, admin.ID) {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("admin should create users; got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestAdminGate_RefreshAndRestart_NonAdminForbidden verifies that operational
// endpoints (catalog refresh, restart, update apply) are admin-gated.
func TestAdminGate_RefreshAndRestart_NonAdminForbidden(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{Password: "pw"})
	if _, err := backend.CreateUser("Admin", "#000", true, false, 0); err != nil {
		t.Fatalf("admin: %v", err)
	}
	alice, err := backend.CreateUser("Alice", "#f00", false, false, 0)
	if err != nil {
		t.Fatalf("alice: %v", err)
	}
	cookies := loginAsHelper(t, srv, alice.ID)

	tests := []struct {
		method, path string
	}{
		{http.MethodPost, "/api/refresh"},
		{http.MethodPost, "/api/update/apply"},
		{http.MethodPost, "/api/restart"},
	}
	for _, tc := range tests {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s %s: non-admin expected 403, got %d", tc.method, tc.path, rr.Code)
		}
	}
}

// TestAdminGate_SingleUserMode verifies that the gate is transparent when no
// users are registered (single-user / dev mode preserves prior behaviour).
func TestAdminGate_SingleUserMode(t *testing.T) {
	srv, _, _ := newSQLiteTestServer(t, Options{}) // no Password, no users
	req := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("single-user mode should allow /api/refresh; got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestQRCode_ReturnsPNG verifies that /api/qr returns a non-empty PNG when an
// OPDS token is configured.
func TestQRCode_ReturnsPNG(t *testing.T) {
	srv := newTestServer(t, Options{OPDSToken: "shared-tok"})
	req := httptest.NewRequest(http.MethodGet, "/api/qr", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("expected image/png Content-Type, got %q", got)
	}
	body := rr.Body.Bytes()
	if len(body) < 8 || string(body[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Errorf("response is not a PNG (first 8 bytes %x)", body[:min(8, len(body))])
	}
}

// TestQRCode_NoToken_ServiceUnavailable verifies that the endpoint refuses to
// generate a QR with no token configured (otherwise it would encode an
// unauthenticated URL).
func TestQRCode_NoToken_ServiceUnavailable(t *testing.T) {
	srv := newTestServer(t, Options{}) // no token
	req := httptest.NewRequest(http.MethodGet, "/api/qr", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when no token, got %d", rr.Code)
	}
}

// TestQRCode_UserURL_RejectsForeignHost verifies the admin-side user_url
// parameter cannot be used to QR-encode arbitrary off-host URLs.
func TestQRCode_UserURL_RejectsForeignHost(t *testing.T) {
	srv := newTestServer(t, Options{OPDSToken: "shared"})
	req := httptest.NewRequest(http.MethodGet,
		"/api/qr?user_url=https://evil.example.com/phish",
		nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for foreign host user_url, got %d", rr.Code)
	}
}

// TestQRCode_UserURL_AcceptsRelative verifies that a relative (host-less)
// user_url is accepted and produces a PNG.
func TestQRCode_UserURL_AcceptsRelative(t *testing.T) {
	srv := newTestServer(t, Options{OPDSToken: "shared"})
	req := httptest.NewRequest(http.MethodGet,
		"/api/qr?user_url=%2Fopds%3Ftoken%3Duser-tok",
		nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for relative user_url, got %d: %s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Content-Type") != "image/png" {
		t.Errorf("expected PNG, got Content-Type %q", rr.Header().Get("Content-Type"))
	}
}
