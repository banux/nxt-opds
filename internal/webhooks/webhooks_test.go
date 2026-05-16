package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/banux/nxt-opds/internal/catalog"
)

// fakeManager is a minimal in-memory catalog.WebhookManager for tests.
type fakeManager struct {
	mu           sync.Mutex
	hooks        []catalog.Webhook
	lastStatus   string
	lastFiredAt  time.Time
	recordCalled int32
}

func (f *fakeManager) Webhooks() ([]catalog.Webhook, error)           { return f.hooks, nil }
func (f *fakeManager) WebhookByID(id string) (*catalog.Webhook, error) { return &f.hooks[0], nil }
func (f *fakeManager) CreateWebhook(name, url string, events []string, secret string, enabled bool) (*catalog.Webhook, error) {
	return nil, nil
}
func (f *fakeManager) UpdateWebhook(id string, u catalog.WebhookUpdate) (*catalog.Webhook, error) {
	return nil, nil
}
func (f *fakeManager) DeleteWebhook(id string) error { return nil }
func (f *fakeManager) RecordWebhookFire(id, status string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastStatus = status
	f.lastFiredAt = at
	atomic.AddInt32(&f.recordCalled, 1)
	return nil
}

// TestFireDeliversToSubscribedEnabledWebhook verifies that a Fire call
// reaches a receiver that is enabled and subscribed to the event, that the
// JSON envelope is well-formed, and that RecordWebhookFire is invoked.
func TestFireDeliversToSubscribedEnabledWebhook(t *testing.T) {
	var (
		gotEnvelope map[string]any
		gotEvent    string
		hit         int32
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hit, 1)
		gotEvent = r.Header.Get("X-NxtOpds-Event")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotEnvelope)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	mgr := &fakeManager{hooks: []catalog.Webhook{{
		ID:      "wh1",
		URL:     srv.URL,
		Events:  []string{catalog.WebhookEventBookCreated},
		Enabled: true,
	}}}

	d := New(mgr)
	d.Fire(catalog.WebhookEventBookCreated, map[string]string{"id": "abc"})
	d.Wait()

	if atomic.LoadInt32(&hit) != 1 {
		t.Fatalf("expected 1 HTTP call, got %d", hit)
	}
	if gotEvent != catalog.WebhookEventBookCreated {
		t.Errorf("X-NxtOpds-Event header = %q, want %q", gotEvent, catalog.WebhookEventBookCreated)
	}
	if gotEnvelope["event"] != catalog.WebhookEventBookCreated {
		t.Errorf("envelope event = %v, want %q", gotEnvelope["event"], catalog.WebhookEventBookCreated)
	}
	if atomic.LoadInt32(&mgr.recordCalled) != 1 {
		t.Errorf("expected 1 RecordWebhookFire call, got %d", mgr.recordCalled)
	}
}

// TestFireSkipsDisabledAndUnsubscribed verifies that the dispatcher honours
// the Enabled flag and the per-event subscription list.
func TestFireSkipsDisabledAndUnsubscribed(t *testing.T) {
	var hit int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hit, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	mgr := &fakeManager{hooks: []catalog.Webhook{
		{ID: "disabled", URL: srv.URL, Enabled: false}, // no subscription = all events, but disabled
		{ID: "wrong", URL: srv.URL, Events: []string{catalog.WebhookEventBookDeleted}, Enabled: true},
	}}

	d := New(mgr)
	d.Fire(catalog.WebhookEventBookCreated, map[string]string{"id": "abc"})
	d.Wait()

	if atomic.LoadInt32(&hit) != 0 {
		t.Fatalf("expected 0 HTTP calls, got %d", hit)
	}
}

// TestFireOneBypassesSubscriptionAndEnabled verifies that the admin "test
// webhook" path always reaches the receiver, regardless of subscription/enabled.
func TestFireOneBypassesSubscriptionAndEnabled(t *testing.T) {
	var hit int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hit, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	mgr := &fakeManager{hooks: []catalog.Webhook{{
		ID:      "wh1",
		URL:     srv.URL,
		Events:  []string{catalog.WebhookEventBookDeleted}, // not the event we fire
		Enabled: false,                                     // and disabled
	}}}
	d := New(mgr)
	d.FireOne(mgr.hooks[0], "test", map[string]string{"hello": "world"})
	d.Wait()

	if atomic.LoadInt32(&hit) != 1 {
		t.Fatalf("expected 1 HTTP call, got %d", hit)
	}
}

// fakeLibrarianTarget implements LibrarianTargetProvider for tests.
type fakeLibrarianTarget struct {
	assoc *catalog.LibrarianAssociationData
	err   error
}

func (f *fakeLibrarianTarget) Get() (*catalog.LibrarianAssociationData, error) {
	return f.assoc, f.err
}

// TestLibrarianFanout_PostsToBookEventEndpoint verifies that when a librarian
// target is configured and Get() returns an association, Fire() POSTs the
// envelope to ${librarian_url}/webhooks/${instance}/book-event with both the
// X-NxtOpds-Event header and a matching X-Signature HMAC-SHA256.
func TestLibrarianFanout_PostsToBookEventEndpoint(t *testing.T) {
	var (
		gotPath    string
		gotEvent   string
		gotSig     string
		gotBody    []byte
		gotMethod  string
		hit        int32
		bodyReadMu sync.Mutex
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hit, 1)
		bodyReadMu.Lock()
		defer bodyReadMu.Unlock()
		gotPath = r.URL.Path
		gotEvent = r.Header.Get("X-NxtOpds-Event")
		gotSig = r.Header.Get("X-Signature")
		gotMethod = r.Method
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	target := &fakeLibrarianTarget{
		assoc: &catalog.LibrarianAssociationData{
			LibrarianURL:      srv.URL,
			LibrarianInstance: "inst-99",
			ChatSecret:        "irrelevant-chat",
			WebhookSecret:     "the-webhook-secret-32-bytes",
		},
	}
	// No admin webhook manager — librarian is the only target.
	d := New(nil)
	d.SetLibrarianTarget(target)

	d.Fire(catalog.WebhookEventBookCreated, map[string]string{"id": "abc"})
	d.Wait()

	if atomic.LoadInt32(&hit) != 1 {
		t.Fatalf("expected 1 librarian HTTP call, got %d", hit)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/webhooks/inst-99/book-event" {
		t.Errorf("path = %q, want /webhooks/inst-99/book-event", gotPath)
	}
	if gotEvent != catalog.WebhookEventBookCreated {
		t.Errorf("X-NxtOpds-Event header = %q, want %q", gotEvent, catalog.WebhookEventBookCreated)
	}

	// Compute the expected signature over the actual body the receiver saw.
	mac := hmac.New(sha256.New, []byte("the-webhook-secret-32-bytes"))
	mac.Write(gotBody)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if gotSig != want {
		t.Errorf("X-Signature = %q, want %q", gotSig, want)
	}

	// Body is a JSON envelope with the right event.
	var env map[string]any
	if err := json.Unmarshal(gotBody, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env["event"] != catalog.WebhookEventBookCreated {
		t.Errorf("envelope event = %v, want %q", env["event"], catalog.WebhookEventBookCreated)
	}
}

// TestLibrarianFanout_SkippedWhenNoAssociation verifies the dispatcher
// gracefully ignores a configured target whose Get() returns nil (i.e. no
// pairing exists yet).
func TestLibrarianFanout_SkippedWhenNoAssociation(t *testing.T) {
	var hit int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hit, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := New(nil)
	d.SetLibrarianTarget(&fakeLibrarianTarget{assoc: nil})
	d.Fire(catalog.WebhookEventBookCreated, map[string]string{"id": "abc"})
	d.Wait()

	if atomic.LoadInt32(&hit) != 0 {
		t.Fatalf("expected 0 HTTP calls without an association, got %d", hit)
	}
}

// TestLibrarianFanout_SkippedWhenWebhookSecretMissing verifies the
// dispatcher refuses to POST without a webhook secret (which would mean
// the librarian cannot verify the signature anyway).
func TestLibrarianFanout_SkippedWhenWebhookSecretMissing(t *testing.T) {
	var hit int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hit, 1)
	}))
	defer srv.Close()

	d := New(nil)
	d.SetLibrarianTarget(&fakeLibrarianTarget{
		assoc: &catalog.LibrarianAssociationData{
			LibrarianURL:      srv.URL,
			LibrarianInstance: "i",
			WebhookSecret:     "", // missing
		},
	})
	d.Fire(catalog.WebhookEventBookCreated, nil)
	d.Wait()

	if atomic.LoadInt32(&hit) != 0 {
		t.Fatalf("expected 0 HTTP calls with empty webhook_secret, got %d", hit)
	}
}

// TestLibrarianFanout_ParallelWithAdminWebhooks verifies that an admin
// webhook and the librarian target both fire on the same event, and that
// the librarian fan-out does NOT call RecordWebhookFire (no row exists).
func TestLibrarianFanout_ParallelWithAdminWebhooks(t *testing.T) {
	var adminHit, libHit int32
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&adminHit, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer admin.Close()
	lib := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&libHit, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer lib.Close()

	mgr := &fakeManager{hooks: []catalog.Webhook{{
		ID:      "wh1",
		URL:     admin.URL,
		Events:  []string{catalog.WebhookEventBookUpdated},
		Enabled: true,
	}}}
	d := New(mgr)
	d.SetLibrarianTarget(&fakeLibrarianTarget{assoc: &catalog.LibrarianAssociationData{
		LibrarianURL:      lib.URL,
		LibrarianInstance: "i",
		WebhookSecret:     "s",
	}})

	d.Fire(catalog.WebhookEventBookUpdated, map[string]string{"id": "x"})
	d.Wait()

	if atomic.LoadInt32(&adminHit) != 1 {
		t.Errorf("admin webhook should fire once, got %d", adminHit)
	}
	if atomic.LoadInt32(&libHit) != 1 {
		t.Errorf("librarian fan-out should fire once, got %d", libHit)
	}
	// RecordWebhookFire is called for the admin hook only — the librarian
	// is a parallel target with no row in the webhooks table.
	if atomic.LoadInt32(&mgr.recordCalled) != 1 {
		t.Errorf("RecordWebhookFire should be called exactly once (admin only), got %d",
			mgr.recordCalled)
	}
}

// TestLibrarianFanout_NoTargetNoOp verifies that without a target set, the
// dispatcher is fully transparent (no behaviour change vs. legacy callers).
func TestLibrarianFanout_NoTargetNoOp(t *testing.T) {
	mgr := &fakeManager{hooks: []catalog.Webhook{}}
	d := New(mgr)
	// No SetLibrarianTarget call.
	d.Fire(catalog.WebhookEventBookCreated, nil)
	d.Wait()
	if atomic.LoadInt32(&mgr.recordCalled) != 0 {
		t.Errorf("no targets configured, RecordWebhookFire shouldn't be called: %d", mgr.recordCalled)
	}
}
