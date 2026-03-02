package server

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/banux/nxt-opds/internal/ai"
)

// handleAIChat handles POST /api/ai/chat.
// It expects a JSON body: {"prompt": "...", "history": [...]}
// Returns JSON: {"response": "..."}
func (s *Server) handleAIChat(w http.ResponseWriter, r *http.Request) {
	if s.aiAgent == nil {
		http.Error(w, `{"error":"AI non configuré (ANTHROPIC_API_KEY manquant)"}`, http.StatusServiceUnavailable)
		return
	}

	var req struct {
		Prompt  string       `json:"prompt"`
		History []ai.Message `json:"history"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"JSON invalide"}`, http.StatusBadRequest)
		return
	}
	if req.Prompt == "" {
		http.Error(w, `{"error":"Champ 'prompt' requis"}`, http.StatusBadRequest)
		return
	}

	response, err := s.aiAgent.Chat(context.Background(), req.History, req.Prompt)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"response": response})
}
