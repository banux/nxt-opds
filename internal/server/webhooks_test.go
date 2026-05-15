package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/banux/nxt-opds/internal/catalog"
)

// TestWebhook_CRUDEndpoints exercises the admin HTTP endpoints round-trip
// against a SQLite-backed catalog: create, list, patch, delete.
func TestWebhook_CRUDEndpoints(t *testing.T) {
	srv, _, _ := newSQLiteTestServer(t, Options{})

	// Initial list should be empty.
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/webhooks", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/webhooks: %d %s", rr.Code, rr.Body.String())
	}
	var initial []map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&initial)
	if len(initial) != 0 {
		t.Fatalf("expected 0 webhooks, got %d", len(initial))
	}

	// Create.
	body := bytes.NewBufferString(`{
		"name": "Test",
		"url": "https://example.test/hook",
		"events": ["book.created", "unknown.event"],
		"secret": "s3cret",
		"enabled": true
	}`)
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks", body)
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST /api/webhooks: %d %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&created)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("missing id in response: %+v", created)
	}
	if created["hasSecret"] != true {
		t.Errorf("expected hasSecret=true, got %+v", created["hasSecret"])
	}
	if _, ok := created["secret"]; ok {
		t.Errorf("secret must NEVER appear in API response: %+v", created)
	}
	// Unknown events must have been filtered out.
	events, _ := created["events"].([]any)
	if len(events) != 1 || events[0] != "book.created" {
		t.Errorf("expected only book.created event, got %+v", events)
	}

	// PATCH (disable + rename).
	patchBody := bytes.NewBufferString(`{"name":"Renamed","enabled":false}`)
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/api/webhooks/"+id, patchBody)
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", rr.Code, rr.Body.String())
	}
	var patched map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&patched)
	if patched["name"] != "Renamed" || patched["enabled"] != false {
		t.Errorf("patch mismatch: %+v", patched)
	}

	// DELETE.
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/api/webhooks/"+id, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE: %d %s", rr.Code, rr.Body.String())
	}

	// List again → 0 results.
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/webhooks", nil))
	var after []map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&after)
	if len(after) != 0 {
		t.Errorf("expected 0 webhooks after delete, got %d", len(after))
	}
}

// TestWebhook_FiresOnBookUpload installs a webhook URL pointing to a
// httptest server, uploads a book, and verifies the receiver got a
// well-formed payload with the expected event and signature header.
func TestWebhook_FiresOnBookUpload(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{})

	type call struct {
		event     string
		signature string
		body      []byte
	}
	var (
		mu        sync.Mutex
		calls     []call
		gotCallCh = make(chan struct{}, 4)
	)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, call{
			event:     r.Header.Get("X-NxtOpds-Event"),
			signature: r.Header.Get("X-Signature"),
			body:      buf,
		})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		select {
		case gotCallCh <- struct{}{}:
		default:
		}
	}))
	defer receiver.Close()

	// Register a webhook subscribed to all events with a secret.
	_, err := backend.CreateWebhook("test", receiver.URL, nil, "hmac-key", true)
	if err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}

	// Upload an EPUB to fire book.created.
	uploadBook(t, srv, "wh-test.epub", "Hooked", "Author")

	// Wait briefly for the async delivery.
	select {
	case <-gotCallCh:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for webhook delivery")
	}
	srv.webhooks.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(calls) == 0 {
		t.Fatalf("no webhook calls received")
	}
	c := calls[0]
	if c.event != catalog.WebhookEventBookCreated {
		t.Errorf("expected event %q, got %q", catalog.WebhookEventBookCreated, c.event)
	}
	if !strings.HasPrefix(c.signature, "sha256=") {
		t.Errorf("expected HMAC signature, got %q", c.signature)
	}
	// Body should be valid JSON with event=book.created.
	var env map[string]any
	if err := json.Unmarshal(c.body, &env); err != nil {
		t.Fatalf("payload not JSON: %v (%s)", err, c.body)
	}
	if env["event"] != catalog.WebhookEventBookCreated {
		t.Errorf("payload event mismatch: %v", env["event"])
	}
	data, _ := env["data"].(map[string]any)
	if data == nil || data["title"] != "Hooked" {
		t.Errorf("payload data missing book title: %+v", env["data"])
	}
}

// TestWebhook_SkipsDisabled verifies that a disabled webhook is never called.
func TestWebhook_SkipsDisabled(t *testing.T) {
	srv, backend, _ := newSQLiteTestServer(t, Options{})

	called := false
	var mu sync.Mutex
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		called = true
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	_, err := backend.CreateWebhook("disabled", receiver.URL, nil, "", false)
	if err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}

	uploadBook(t, srv, "skip.epub", "Skip", "Author")
	srv.webhooks.Wait()

	mu.Lock()
	defer mu.Unlock()
	if called {
		t.Errorf("disabled webhook was called")
	}
}

// TestWebhook_SubscriptionFilter ensures only matching events fire.
func TestWebhook_SubscriptionFilter(t *testing.T) {
	srv, backend, bookID := newSQLiteTestServer(t, Options{})

	var (
		mu     sync.Mutex
		events []string
	)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		events = append(events, r.Header.Get("X-NxtOpds-Event"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	// Only subscribe to book.updated.
	_, err := backend.CreateWebhook("only-updated", receiver.URL,
		[]string{catalog.WebhookEventBookUpdated}, "", true)
	if err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}

	// Trigger an update on the existing book → fires.
	patch := `{"title": "Updated"}`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/books/"+bookID, strings.NewReader(patch))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH book: %d %s", rr.Code, rr.Body.String())
	}

	// Trigger an upload → book.created, should NOT fire.
	uploadBook(t, srv, "other.epub", "Other", "Author")

	srv.webhooks.Wait()

	mu.Lock()
	defer mu.Unlock()
	for _, e := range events {
		if e != catalog.WebhookEventBookUpdated {
			t.Errorf("unexpected event %q (only book.updated should fire)", e)
		}
	}
	if len(events) == 0 {
		t.Errorf("expected at least one book.updated event")
	}
}
