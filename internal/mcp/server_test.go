package mcp_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/banux/nxt-opds/internal/catalog"
	"github.com/banux/nxt-opds/internal/mcp"
)

// ─── Minimal fake catalog ─────────────────────────────────────────────────────

type fakeCatalog struct {
	books []catalog.Book
}

func (f *fakeCatalog) Root() ([]catalog.NavEntry, error) { return nil, nil }
func (f *fakeCatalog) AllBooks(offset, limit int) ([]catalog.Book, int, error) {
	return f.books, len(f.books), nil
}
func (f *fakeCatalog) BookByID(id string) (*catalog.Book, error) {
	for _, b := range f.books {
		if b.ID == id {
			cp := b
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("book %q not found", id)
}
func (f *fakeCatalog) Search(q catalog.SearchQuery) ([]catalog.Book, int, error) {
	var out []catalog.Book
	for _, b := range f.books {
		if q.Query == "" || containsStr(b.Title, q.Query) {
			if q.UnreadOnly && b.IsRead {
				continue
			}
			out = append(out, b)
		}
	}
	total := len(out)
	if q.Offset < len(out) {
		out = out[q.Offset:]
	} else {
		out = nil
	}
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, total, nil
}
func (f *fakeCatalog) BooksByAuthor(author string, offset, limit int) ([]catalog.Book, int, error) {
	return nil, 0, nil
}
func (f *fakeCatalog) BooksByTag(tag string, offset, limit int) ([]catalog.Book, int, error) {
	return nil, 0, nil
}
func (f *fakeCatalog) Authors(offset, limit int) ([]string, int, error) {
	return []string{"Jules Verne", "Victor Hugo"}, 2, nil
}
func (f *fakeCatalog) Tags(offset, limit int) ([]string, int, error) {
	return []string{"aventure", "classique"}, 2, nil
}
func (f *fakeCatalog) Publishers(offset, limit int) ([]string, int, error) {
	return []string{"Gallimard"}, 1, nil
}
func (f *fakeCatalog) BooksByPublisher(publisher string, offset, limit int) ([]catalog.Book, int, error) {
	return nil, 0, nil
}

func containsStr(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}

func newFakeCatalog() *fakeCatalog {
	return &fakeCatalog{
		books: []catalog.Book{
			{
				ID:      "book-1",
				Title:   "Vingt mille lieues",
				Authors: []catalog.Author{{Name: "Jules Verne"}},
				Tags:    []string{"aventure", "science-fiction"},
				Series:  "Voyages extraordinaires",
				IsRead:  false,
				Rating:  4,
				AddedAt: time.Now(),
			},
			{
				ID:      "book-2",
				Title:   "Les Misérables",
				Authors: []catalog.Author{{Name: "Victor Hugo"}},
				Tags:    []string{"classique"},
				IsRead:  true,
				AddedAt: time.Now().Add(-time.Hour),
			},
		},
	}
}

// ─── Test helpers ─────────────────────────────────────────────────────────────

func mcpPost(t *testing.T, srv *mcp.Server, body any) map[string]any {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", rec.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	return resp
}

func rpcResult(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	if e, ok := resp["error"]; ok && e != nil {
		t.Fatalf("unexpected RPC error: %v", e)
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("result is not an object: %v", resp["result"])
	}
	return result
}

// ─── Tests ────────────────────────────────────────────────────────────────────

func TestInitialize(t *testing.T) {
	srv := mcp.New(newFakeCatalog())
	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"clientInfo":      map[string]any{"name": "test"},
			"capabilities":    map[string]any{},
		},
	})
	result := rpcResult(t, resp)
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("unexpected protocolVersion: %v", result["protocolVersion"])
	}
	if result["serverInfo"] == nil {
		t.Error("serverInfo missing")
	}
}

func TestToolsList(t *testing.T) {
	srv := mcp.New(newFakeCatalog())
	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	})
	result := rpcResult(t, resp)
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("expected tools array, got: %v", result["tools"])
	}
	// Verify expected tools are present.
	names := map[string]bool{}
	for _, ti := range tools {
		if tm, ok := ti.(map[string]any); ok {
			names[tm["name"].(string)] = true
		}
	}
	for _, want := range []string{"search_books", "get_book", "update_book", "list_authors", "list_tags", "list_series", "list_publishers"} {
		if !names[want] {
			t.Errorf("missing tool: %s", want)
		}
	}
}

func TestToolSearchBooks(t *testing.T) {
	srv := mcp.New(newFakeCatalog())
	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "search_books",
			"arguments": map[string]any{"query": "Misér"},
		},
	})
	result := rpcResult(t, resp)
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatal("expected content items")
	}
	text := content[0].(map[string]any)["text"].(string)
	if !containsStr(text, "Les Misérables") {
		t.Errorf("expected 'Les Misérables' in result, got: %s", text)
	}
}

func TestToolSearchBooksUnreadOnly(t *testing.T) {
	srv := mcp.New(newFakeCatalog())
	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "search_books",
			"arguments": map[string]any{"unread_only": true},
		},
	})
	result := rpcResult(t, resp)
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if containsStr(text, "Les Misérables") {
		t.Error("unread_only should exclude 'Les Misérables' (is_read=true)")
	}
	if !containsStr(text, "Vingt mille lieues") {
		t.Error("expected 'Vingt mille lieues' in unread results")
	}
}

func TestToolGetBook(t *testing.T) {
	srv := mcp.New(newFakeCatalog())
	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      5,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "get_book",
			"arguments": map[string]any{"id": "book-1"},
		},
	})
	result := rpcResult(t, resp)
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !containsStr(text, "Vingt mille lieues") {
		t.Errorf("expected book title in result, got: %s", text)
	}
	if !containsStr(text, "Jules Verne") {
		t.Errorf("expected author in result, got: %s", text)
	}
}

func TestToolGetBookNotFound(t *testing.T) {
	srv := mcp.New(newFakeCatalog())
	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      6,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "get_book",
			"arguments": map[string]any{"id": "does-not-exist"},
		},
	})
	result := rpcResult(t, resp)
	// Should return isError=true but still a valid result (not a JSON-RPC error).
	if result["isError"] != true {
		t.Errorf("expected isError=true for missing book, got: %v", result)
	}
}

func TestToolListAuthors(t *testing.T) {
	srv := mcp.New(newFakeCatalog())
	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      7,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "list_authors",
			"arguments": map[string]any{},
		},
	})
	result := rpcResult(t, resp)
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !containsStr(text, "Jules Verne") {
		t.Errorf("expected Jules Verne in authors, got: %s", text)
	}
}

func TestToolListTags(t *testing.T) {
	srv := mcp.New(newFakeCatalog())
	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      8,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "list_tags",
			"arguments": map[string]any{},
		},
	})
	result := rpcResult(t, resp)
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !containsStr(text, "aventure") {
		t.Errorf("expected 'aventure' in tags, got: %s", text)
	}
}

func TestToolListPublishers(t *testing.T) {
	srv := mcp.New(newFakeCatalog())
	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      9,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "list_publishers",
			"arguments": map[string]any{},
		},
	})
	result := rpcResult(t, resp)
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !containsStr(text, "Gallimard") {
		t.Errorf("expected 'Gallimard' in publishers, got: %s", text)
	}
}

func TestInvalidMethod(t *testing.T) {
	srv := mcp.New(newFakeCatalog())
	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      10,
		"method":  "unknown/method",
	})
	if resp["error"] == nil {
		t.Error("expected RPC error for unknown method")
	}
	errObj := resp["error"].(map[string]any)
	if errObj["code"].(float64) != -32601 {
		t.Errorf("expected code -32601, got %v", errObj["code"])
	}
}

func TestPing(t *testing.T) {
	srv := mcp.New(newFakeCatalog())
	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      11,
		"method":  "ping",
	})
	rpcResult(t, resp) // just verify no error
}

func TestWrongHTTPMethod(t *testing.T) {
	srv := mcp.New(newFakeCatalog())
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

// ─── upload_book tests ────────────────────────────────────────────────────────

// uploadableCatalog embeds fakeCatalog and implements catalog.Uploader.
type uploadableCatalog struct {
	*fakeCatalog
	stored []string // filenames passed to StoreBook
}

func (u *uploadableCatalog) StoreBook(filename string, src io.ReadCloser) (*catalog.Book, error) {
	defer src.Close()
	u.stored = append(u.stored, filename)
	return &catalog.Book{
		ID:    "uploaded-1",
		Title: "Uploaded Book",
	}, nil
}

func TestToolUploadBook(t *testing.T) {
	cat := &uploadableCatalog{fakeCatalog: newFakeCatalog()}
	srv := mcp.New(cat)

	content := base64.StdEncoding.EncodeToString([]byte("fake epub content"))
	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      20,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "upload_book",
			"arguments": map[string]any{
				"filename": "test.epub",
				"content":  content,
			},
		},
	})
	result := rpcResult(t, resp)
	content2 := result["content"].([]any)
	text := content2[0].(map[string]any)["text"].(string)
	if !containsStr(text, "succès") {
		t.Errorf("expected success message, got: %s", text)
	}
	if !containsStr(text, "Uploaded Book") {
		t.Errorf("expected book title in result, got: %s", text)
	}
	if len(cat.stored) != 1 || cat.stored[0] != "test.epub" {
		t.Errorf("expected StoreBook called with 'test.epub', got: %v", cat.stored)
	}
}

// ─── to-read tests ────────────────────────────────────────────────────────────

// toReadCatalog embeds fakeCatalog and implements catalog.ToReadManager.
type toReadCatalog struct {
	*fakeCatalog
	lists       map[string][]string // userID → ordered book IDs
	added       []string            // sequence of "userID:bookID" Add calls
	removed     []string            // sequence of "userID:bookID" Remove calls
	reordered   map[string][]string // last ordering passed to Reorder per user
}

func newToReadCatalog() *toReadCatalog {
	return &toReadCatalog{
		fakeCatalog: newFakeCatalog(),
		lists:       map[string][]string{},
		reordered:   map[string][]string{},
	}
}

func (c *toReadCatalog) ToReadList(userID string) ([]catalog.ToReadItem, error) {
	ids := c.lists[userID]
	out := make([]catalog.ToReadItem, 0, len(ids))
	for i, id := range ids {
		b, err := c.BookByID(id)
		if err != nil {
			continue
		}
		out = append(out, catalog.ToReadItem{
			UserID:   userID,
			Book:     *b,
			Position: i,
			AddedAt:  time.Now().Add(-time.Duration(i) * time.Hour),
		})
	}
	return out, nil
}

func (c *toReadCatalog) AddToReadList(userID, bookID string) error {
	c.added = append(c.added, userID+":"+bookID)
	for _, id := range c.lists[userID] {
		if id == bookID {
			return nil
		}
	}
	c.lists[userID] = append(c.lists[userID], bookID)
	return nil
}

func (c *toReadCatalog) RemoveFromToReadList(userID, bookID string) error {
	c.removed = append(c.removed, userID+":"+bookID)
	out := c.lists[userID][:0]
	for _, id := range c.lists[userID] {
		if id != bookID {
			out = append(out, id)
		}
	}
	c.lists[userID] = out
	return nil
}

func (c *toReadCatalog) ReorderToReadList(userID string, bookIDs []string) error {
	cp := append([]string(nil), bookIDs...)
	c.reordered[userID] = cp
	c.lists[userID] = cp
	return nil
}

func TestToolListToRead(t *testing.T) {
	cat := newToReadCatalog()
	cat.lists["u1"] = []string{"book-2", "book-1"}
	srv := mcp.New(cat)

	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      30,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "list_to_read",
			"arguments": map[string]any{"user_id": "u1"},
		},
	})
	result := rpcResult(t, resp)
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !containsStr(text, "Les Misérables") {
		t.Errorf("expected 'Les Misérables' in to-read list, got: %s", text)
	}
	if !containsStr(text, "Vingt mille lieues") {
		t.Errorf("expected 'Vingt mille lieues' in to-read list, got: %s", text)
	}
	// Position 1 (= "1.") should be Les Misérables since it appears first.
	if idxA, idxB := indexOf(text, "Les Misérables"), indexOf(text, "Vingt mille lieues"); idxA > idxB {
		t.Errorf("expected Les Misérables before Vingt mille lieues; got order reversed")
	}
}

func TestToolListToReadNotSupported(t *testing.T) {
	srv := mcp.New(newFakeCatalog())
	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      31,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "list_to_read",
			"arguments": map[string]any{"user_id": "u1"},
		},
	})
	result := rpcResult(t, resp)
	if result["isError"] != true {
		t.Errorf("expected isError=true, got: %v", result)
	}
}

func TestToolAddToRead(t *testing.T) {
	cat := newToReadCatalog()
	srv := mcp.New(cat)

	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      32,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "add_to_read",
			"arguments": map[string]any{
				"user_id": "u1",
				"book_id": "book-1",
			},
		},
	})
	rpcResult(t, resp)
	if len(cat.added) != 1 || cat.added[0] != "u1:book-1" {
		t.Errorf("expected AddToReadList called with u1:book-1, got: %v", cat.added)
	}
	if got := cat.lists["u1"]; len(got) != 1 || got[0] != "book-1" {
		t.Errorf("expected list to contain [book-1], got: %v", got)
	}
}

func TestToolRemoveToRead(t *testing.T) {
	cat := newToReadCatalog()
	cat.lists["u1"] = []string{"book-1", "book-2"}
	srv := mcp.New(cat)

	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      33,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "remove_to_read",
			"arguments": map[string]any{
				"user_id": "u1",
				"book_id": "book-1",
			},
		},
	})
	rpcResult(t, resp)
	if got := cat.lists["u1"]; len(got) != 1 || got[0] != "book-2" {
		t.Errorf("expected list to contain only [book-2] after removal, got: %v", got)
	}
}

func TestToolReorderToRead(t *testing.T) {
	cat := newToReadCatalog()
	cat.lists["u1"] = []string{"book-1", "book-2"}
	srv := mcp.New(cat)

	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      34,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "reorder_to_read",
			"arguments": map[string]any{
				"user_id":  "u1",
				"book_ids": []any{"book-2", "book-1"},
			},
		},
	})
	rpcResult(t, resp)
	got := cat.reordered["u1"]
	if len(got) != 2 || got[0] != "book-2" || got[1] != "book-1" {
		t.Errorf("expected reorder [book-2, book-1], got: %v", got)
	}
}

func TestToolAddToReadMissingArgs(t *testing.T) {
	srv := mcp.New(newToReadCatalog())
	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      35,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "add_to_read",
			"arguments": map[string]any{"user_id": "u1"},
		},
	})
	if resp["error"] == nil {
		t.Error("expected RPC error for missing book_id")
	}
}

// indexOf returns the byte index of sub in s, or -1.
func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// readCatalog embeds fakeCatalog and adds the bare minimum to satisfy
// catalog.Updater (single-user) and catalog.UserReadManager (multi-user) so
// the set_book_read tool can be exercised in both modes.
type readCatalog struct {
	*fakeCatalog
	perUserRead map[string]map[string]bool // userID → bookID → isRead
	updates     []string                   // sequence of "id:isRead" calls to UpdateBook
}

func newReadCatalog() *readCatalog {
	return &readCatalog{
		fakeCatalog: newFakeCatalog(),
		perUserRead: map[string]map[string]bool{},
	}
}

func (c *readCatalog) UpdateBook(id string, u catalog.BookUpdate) (*catalog.Book, error) {
	if u.IsRead != nil {
		c.updates = append(c.updates, fmt.Sprintf("%s:%v", id, *u.IsRead))
	}
	for i := range c.books {
		if c.books[i].ID == id {
			if u.IsRead != nil {
				c.books[i].IsRead = *u.IsRead
			}
			cp := c.books[i]
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("book %q not found", id)
}

func (c *readCatalog) SetUserRead(userID, bookID string, isRead bool) error {
	if c.perUserRead[userID] == nil {
		c.perUserRead[userID] = map[string]bool{}
	}
	c.perUserRead[userID][bookID] = isRead
	return nil
}

func (c *readCatalog) UserReadStatuses(userID string, bookIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range bookIDs {
		out[id] = c.perUserRead[userID][id]
	}
	return out, nil
}

func (c *readCatalog) BookReadColors(bookIDs []string) (map[string][]string, error) {
	return map[string][]string{}, nil
}

// TestToolSetBookRead_PerUser verifies the tool routes through UserReadManager
// when both user_id is supplied and the backend supports it.
func TestToolSetBookRead_PerUser(t *testing.T) {
	cat := newReadCatalog()
	srv := mcp.New(cat)
	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      40,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "set_book_read",
			"arguments": map[string]any{
				"book_id": "book-1",
				"user_id": "alice",
				"is_read": true,
			},
		},
	})
	result := rpcResult(t, resp)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("unexpected error result: %v", result)
	}
	if got := cat.perUserRead["alice"]["book-1"]; !got {
		t.Errorf("expected per-user read=true for alice/book-1, got %v", got)
	}
	if len(cat.updates) != 0 {
		t.Errorf("UpdateBook should not have been called in multi-user mode; got %v", cat.updates)
	}
}

// TestToolSetBookRead_SingleUser verifies the tool falls back to UpdateBook
// (global is_read column) when no user_id is provided.
func TestToolSetBookRead_SingleUser(t *testing.T) {
	cat := newReadCatalog()
	srv := mcp.New(cat)
	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      41,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "set_book_read",
			"arguments": map[string]any{
				"book_id": "book-1",
				"is_read": false,
			},
		},
	})
	result := rpcResult(t, resp)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("unexpected error result: %v", result)
	}
	if len(cat.updates) != 1 || cat.updates[0] != "book-1:false" {
		t.Errorf("expected single UpdateBook call book-1:false, got %v", cat.updates)
	}
}

// TestToolSetBookRead_NotSupported verifies a clean error when the backend
// supports neither per-user reads nor metadata updates.
func TestToolSetBookRead_NotSupported(t *testing.T) {
	srv := mcp.New(newFakeCatalog()) // no Updater, no UserReadManager
	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      42,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "set_book_read",
			"arguments": map[string]any{
				"book_id": "book-1",
				"is_read": true,
			},
		},
	})
	result := rpcResult(t, resp)
	isErr, _ := result["isError"].(bool)
	if !isErr {
		t.Errorf("expected isError=true when backend lacks both Updater and UserReadManager")
	}
}

func TestToolUploadBookNotSupported(t *testing.T) {
	// fakeCatalog does not implement Uploader → tool returns an error result.
	srv := mcp.New(newFakeCatalog())
	content := base64.StdEncoding.EncodeToString([]byte("data"))
	resp := mcpPost(t, srv, map[string]any{
		"jsonrpc": "2.0",
		"id":      21,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "upload_book",
			"arguments": map[string]any{
				"filename": "test.epub",
				"content":  content,
			},
		},
	})
	result := rpcResult(t, resp)
	content2 := result["content"].([]any)
	isError, _ := result["isError"].(bool)
	text := content2[0].(map[string]any)["text"].(string)
	if !isError {
		t.Errorf("expected isError=true when uploader not supported")
	}
	if !containsStr(text, "supporte pas") {
		t.Errorf("expected 'supporte pas' in error message, got: %s", text)
	}
}
