package mcp_test

import (
	"bytes"
	"encoding/json"
	"fmt"
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
