package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"github.com/banux/nxt-opds/internal/catalog"
)

// webhookJSON is the public JSON representation of a webhook.
// The secret is never written back to the client in plaintext; only a hint
// of whether one is set is exposed via HasSecret.
type webhookJSON struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	URL         string   `json:"url"`
	Events      []string `json:"events"`
	HasSecret   bool     `json:"hasSecret"`
	Enabled     bool     `json:"enabled"`
	CreatedAt   int64    `json:"createdAt,omitempty"`
	LastFiredAt int64    `json:"lastFiredAt,omitempty"`
	LastStatus  string   `json:"lastStatus,omitempty"`
}

func webhookToJSON(h catalog.Webhook) webhookJSON {
	j := webhookJSON{
		ID:        h.ID,
		Name:      h.Name,
		URL:       h.URL,
		Events:    h.Events,
		HasSecret: h.Secret != "",
		Enabled:   h.Enabled,
	}
	if !h.CreatedAt.IsZero() {
		j.CreatedAt = h.CreatedAt.UnixMilli()
	}
	if !h.LastFiredAt.IsZero() {
		j.LastFiredAt = h.LastFiredAt.UnixMilli()
	}
	j.LastStatus = h.LastStatus
	if j.Events == nil {
		j.Events = []string{}
	}
	return j
}

// webhookRequest carries the editable fields from the admin form.
type webhookRequest struct {
	Name    *string  `json:"name,omitempty"`
	URL     *string  `json:"url,omitempty"`
	Events  []string `json:"events,omitempty"`
	Secret  *string  `json:"secret,omitempty"`
	Enabled *bool    `json:"enabled,omitempty"`
}

// handleAPIWebhooks lists every configured webhook.
func (s *Server) handleAPIWebhooks(w http.ResponseWriter, r *http.Request) {
	if s.webhookManager == nil {
		http.Error(w, "webhooks not supported by this backend", http.StatusNotImplemented)
		return
	}
	hooks, err := s.webhookManager.Webhooks()
	if err != nil {
		http.Error(w, "list webhooks: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]webhookJSON, 0, len(hooks))
	for _, h := range hooks {
		out = append(out, webhookToJSON(h))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleAPICreateWebhook registers a new webhook.
func (s *Server) handleAPICreateWebhook(w http.ResponseWriter, r *http.Request) {
	if s.webhookManager == nil {
		http.Error(w, "webhooks not supported by this backend", http.StatusNotImplemented)
		return
	}
	var req webhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(strVal(req.Name))
	urlStr := strings.TrimSpace(strVal(req.URL))
	if urlStr == "" {
		http.Error(w, "url is required", http.StatusBadRequest)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	secret := strVal(req.Secret)
	events := sanitiseEvents(req.Events)
	h, err := s.webhookManager.CreateWebhook(name, urlStr, events, secret, enabled)
	if err != nil {
		http.Error(w, "create webhook: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(webhookToJSON(*h))
}

// handleAPIUpdateWebhook patches an existing webhook.
func (s *Server) handleAPIUpdateWebhook(w http.ResponseWriter, r *http.Request) {
	if s.webhookManager == nil {
		http.Error(w, "webhooks not supported by this backend", http.StatusNotImplemented)
		return
	}
	id := mux.Vars(r)["id"]
	var req webhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	upd := catalog.WebhookUpdate{
		Name:    req.Name,
		URL:     req.URL,
		Secret:  req.Secret,
		Enabled: req.Enabled,
	}
	if req.Events != nil {
		upd.Events = sanitiseEvents(req.Events)
	}
	h, err := s.webhookManager.UpdateWebhook(id, upd)
	if err != nil {
		http.Error(w, "update webhook: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(webhookToJSON(*h))
}

// handleAPIDeleteWebhook removes a webhook by ID.
func (s *Server) handleAPIDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	if s.webhookManager == nil {
		http.Error(w, "webhooks not supported by this backend", http.StatusNotImplemented)
		return
	}
	id := mux.Vars(r)["id"]
	if err := s.webhookManager.DeleteWebhook(id); err != nil {
		http.Error(w, "delete webhook: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// handleAPITestWebhook fires a "test" event against a single webhook so the
// admin can verify the receiver is reachable without having to wait for a
// real book.added/book.updated event.  Other webhooks are NOT notified.
func (s *Server) handleAPITestWebhook(w http.ResponseWriter, r *http.Request) {
	if s.webhookManager == nil {
		http.Error(w, "webhooks not supported by this backend", http.StatusNotImplemented)
		return
	}
	id := mux.Vars(r)["id"]
	h, err := s.webhookManager.WebhookByID(id)
	if err != nil {
		http.Error(w, "webhook not found", http.StatusNotFound)
		return
	}
	s.webhooks.FireOne(*h, "test", map[string]string{
		"message": "Ceci est un test depuis nxt-opds",
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// sanitiseEvents trims, deduplicates, and rejects unknown event names.
// Returning a nil slice means "subscribe to all events".
func sanitiseEvents(events []string) []string {
	if len(events) == 0 {
		return nil
	}
	known := map[string]bool{}
	for _, e := range catalog.AllWebhookEvents {
		known[e] = true
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(events))
	for _, e := range events {
		e = strings.TrimSpace(e)
		if e == "" || !known[e] || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}

func strVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// bookEventPayload builds the JSON-serialisable "data" payload sent with
// every book.* webhook event.  It deliberately exposes only the public-facing
// book fields (no filesystem paths) and uses the same field names as the REST
// API so receivers can parse it with the same data model.
func bookEventPayload(bk *catalog.Book) map[string]any {
	if bk == nil {
		return nil
	}
	authors := make([]string, 0, len(bk.Authors))
	for _, a := range bk.Authors {
		authors = append(authors, a.Name)
	}
	tags := bk.Tags
	if tags == nil {
		tags = []string{}
	}
	mime := ""
	size := int64(0)
	if len(bk.Files) > 0 {
		mime = bk.Files[0].MIMEType
		size = bk.Files[0].Size
	}
	return map[string]any{
		"id":              bk.ID,
		"title":           bk.Title,
		"authors":         authors,
		"tags":            tags,
		"summary":         bk.Summary,
		"language":        bk.Language,
		"publisher":       bk.Publisher,
		"series":          bk.Series,
		"seriesIndex":     bk.SeriesIndex,
		"seriesTotal":     bk.SeriesTotal,
		"collection":      bk.Collection,
		"collectionIndex": bk.CollectionIndex,
		"rating":          bk.Rating,
		"ageRating":       bk.AgeRating,
		"isRead":          bk.IsRead,
		"coverUrl":        bk.CoverURL,
		"downloadUrl":     "/opds/books/" + bk.ID + "/download",
		"fileType":        mime,
		"fileSize":        size,
	}
}

// bookReadEventPayload is the payload sent when a user toggles a book's
// read status.  It includes the book identity plus the userID/userName so
// receivers can route notifications per-user (e.g. Slack channels).
func bookReadEventPayload(bk *catalog.Book, user *catalog.User, isRead bool) map[string]any {
	out := map[string]any{
		"bookId": "",
		"title":  "",
		"isRead": isRead,
	}
	if bk != nil {
		out["bookId"] = bk.ID
		out["title"] = bk.Title
	}
	if user != nil {
		out["userId"] = user.ID
		out["userName"] = user.Name
	}
	return out
}
