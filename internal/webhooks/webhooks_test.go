package webhooks

import (
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
