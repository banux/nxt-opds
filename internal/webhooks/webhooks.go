// Package webhooks dispatches catalog events to admin-configured HTTP
// callbacks.  Calls are made asynchronously through a small worker pool so
// the request that originated the event is never blocked by a slow receiver.
package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/banux/nxt-opds/internal/catalog"
)

// Dispatcher fires registered webhooks asynchronously when catalog events
// occur.  It is safe for concurrent use.
type Dispatcher struct {
	mgr    catalog.WebhookManager
	client *http.Client
	wg     sync.WaitGroup
}

// New returns a Dispatcher backed by the given WebhookManager.  When mgr is
// nil the dispatcher is a no-op (every Fire call simply returns).
func New(mgr catalog.WebhookManager) *Dispatcher {
	return &Dispatcher{
		mgr:    mgr,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Fire dispatches the given event with the supplied JSON-serialisable payload
// to every registered webhook subscribed to that event.  It returns
// immediately; the HTTP call(s) happen in a background goroutine.
func (d *Dispatcher) Fire(event string, payload any) {
	if d == nil || d.mgr == nil {
		return
	}
	hooks, err := d.mgr.Webhooks()
	if err != nil {
		log.Printf("webhooks: list failed: %v", err)
		return
	}
	if len(hooks) == 0 {
		return
	}

	envelope := map[string]any{
		"event":     event,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"data":      payload,
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		log.Printf("webhooks: marshal payload for %s: %v", event, err)
		return
	}

	for _, h := range hooks {
		if !h.Enabled {
			continue
		}
		if !subscribed(h.Events, event) {
			continue
		}
		d.wg.Add(1)
		go d.deliver(h, event, body)
	}
}

// FireOne dispatches the supplied event to one specific webhook, bypassing
// the subscription list and the enabled flag.  Used by the admin "test
// webhook" button so the operator can validate a freshly-configured
// receiver before enabling it for real traffic.
func (d *Dispatcher) FireOne(h catalog.Webhook, event string, payload any) {
	if d == nil || d.mgr == nil {
		return
	}
	envelope := map[string]any{
		"event":     event,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"data":      payload,
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		log.Printf("webhooks: marshal payload for %s: %v", event, err)
		return
	}
	d.wg.Add(1)
	go d.deliver(h, event, body)
}

// Wait blocks until all in-flight webhook deliveries have completed.
// Intended for shutdown / tests; not required for normal operation.
func (d *Dispatcher) Wait() { d.wg.Wait() }

// subscribed reports whether the given event matches the subscription list.
// An empty subscription list means "all events" so the webhook fires for
// every event.
func subscribed(events []string, event string) bool {
	if len(events) == 0 {
		return true
	}
	for _, e := range events {
		if e == event {
			return true
		}
	}
	return false
}

// deliver performs the actual HTTP POST and records the outcome.
func (d *Dispatcher) deliver(h catalog.Webhook, event string, body []byte) {
	defer d.wg.Done()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		d.record(h.ID, fmt.Sprintf("bad request: %v", err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "nxt-opds/webhook")
	req.Header.Set("X-NxtOpds-Event", event)
	if h.Secret != "" {
		mac := hmac.New(sha256.New, []byte(h.Secret))
		mac.Write(body)
		req.Header.Set("X-NxtOpds-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := d.client.Do(req)
	if err != nil {
		d.record(h.ID, err.Error())
		return
	}
	defer resp.Body.Close()
	d.record(h.ID, fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode)))
}

func (d *Dispatcher) record(id, status string) {
	if err := d.mgr.RecordWebhookFire(id, status, time.Now()); err != nil {
		log.Printf("webhooks: record fire %s: %v", id, err)
	}
}
