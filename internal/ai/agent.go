// Package ai provides an AI-powered assistant for the nxt-opds catalog.
//
// It uses the Anthropic Messages API with tool use to give Claude access
// to catalog operations (search, read, update) and runs an agentic loop
// until the model produces a final text response.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/banux/nxt-opds/internal/catalog"
)

const (
	anthropicAPIURL    = "https://api.anthropic.com/v1/messages"
	anthropicVersion   = "2023-06-01"
	defaultModel       = "claude-sonnet-4-6"
	defaultMaxTokens   = 4096
	maxAgentIterations = 10
)

// Agent is an AI assistant backed by the Anthropic Claude API.
// It can search and modify the book catalog through tool calls.
type Agent struct {
	apiKey   string
	catalog  catalog.Catalog
	updater  catalog.Updater
	httpClient *http.Client
}

// New creates a new Agent using the given API key and catalog.
func New(apiKey string, cat catalog.Catalog) *Agent {
	a := &Agent{
		apiKey:     apiKey,
		catalog:    cat,
		httpClient: &http.Client{},
	}
	if u, ok := cat.(catalog.Updater); ok {
		a.updater = u
	}
	return a
}

// Message represents a single turn in the conversation.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ─── Anthropic API types ───────────────────────────────────────────────────────

type apiRequest struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	System    string       `json:"system"`
	Tools     []apiTool    `json:"tools,omitempty"`
	Messages  []apiMessage `json:"messages"`
}

type apiMessage struct {
	Role    string       `json:"role"`
	Content []apiContent `json:"content"`
}

type apiContent struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type apiTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema toolInputSchema `json:"input_schema"`
}

type toolInputSchema struct {
	Type       string                      `json:"type"`
	Properties map[string]toolPropSchema   `json:"properties,omitempty"`
	Required   []string                    `json:"required,omitempty"`
}

type toolPropSchema struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Items       *toolPropSchema `json:"items,omitempty"`
}

type apiResponse struct {
	ID         string       `json:"id"`
	Type       string       `json:"type"`
	Role       string       `json:"role"`
	Content    []apiContent `json:"content"`
	Model      string       `json:"model"`
	StopReason string       `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// ─── Tool definitions ──────────────────────────────────────────────────────────

func (a *Agent) toolDefs() []apiTool {
	return []apiTool{
		{
			Name:        "search_books",
			Description: "Recherche des livres dans le catalogue avec des filtres optionnels. Retourne une liste de livres correspondant aux critères.",
			InputSchema: toolInputSchema{
				Type: "object",
				Properties: map[string]toolPropSchema{
					"query":       {Type: "string", Description: "Texte de recherche (titre, auteur, description)"},
					"author":      {Type: "string", Description: "Filtre par nom d'auteur (correspondance partielle)"},
					"tag":         {Type: "string", Description: "Filtre par tag/genre"},
					"series":      {Type: "string", Description: "Filtre par nom de série"},
					"publisher":   {Type: "string", Description: "Filtre par éditeur"},
					"collection":  {Type: "string", Description: "Filtre par collection éditoriale"},
					"unread_only": {Type: "boolean", Description: "Uniquement les livres non lus"},
					"sort": {
						Type:        "string",
						Description: "Tri des résultats",
						Enum:        []string{"added_desc", "added_asc", "title_asc", "title_desc"},
					},
					"limit":  {Type: "integer", Description: "Nombre de résultats (défaut: 20, max: 50)"},
					"offset": {Type: "integer", Description: "Décalage pour la pagination"},
				},
			},
		},
		{
			Name:        "get_book",
			Description: "Récupère les détails complets d'un livre par son identifiant.",
			InputSchema: toolInputSchema{
				Type:     "object",
				Required: []string{"id"},
				Properties: map[string]toolPropSchema{
					"id": {Type: "string", Description: "Identifiant unique du livre"},
				},
			},
		},
		{
			Name:        "update_book",
			Description: "Modifie les métadonnées d'un livre. Seuls les champs fournis sont mis à jour.",
			InputSchema: toolInputSchema{
				Type:     "object",
				Required: []string{"id"},
				Properties: map[string]toolPropSchema{
					"id":               {Type: "string", Description: "Identifiant unique du livre"},
					"title":            {Type: "string", Description: "Nouveau titre"},
					"authors":          {Type: "array", Description: "Liste des auteurs", Items: &toolPropSchema{Type: "string"}},
					"tags":             {Type: "array", Description: "Liste des tags/genres", Items: &toolPropSchema{Type: "string"}},
					"summary":          {Type: "string", Description: "Résumé du livre"},
					"publisher":        {Type: "string", Description: "Éditeur"},
					"language":         {Type: "string", Description: "Code de langue BCP 47 (ex: fr, en)"},
					"series":           {Type: "string", Description: "Nom de la série"},
					"series_index":     {Type: "string", Description: "Numéro dans la série"},
					"series_total":     {Type: "string", Description: "Nombre total de livres dans la série"},
					"collection":       {Type: "string", Description: "Nom de la collection éditoriale"},
					"collection_index": {Type: "string", Description: "Numéro dans la collection"},
					"is_read":             {Type: "boolean", Description: "Marquer comme lu (true) ou non lu (false)"},
					"rating":              {Type: "integer", Description: "Note de 0 (non noté) à 5 étoiles"},
					"age_rating":          {Type: "integer", Description: "Classification d'âge : 0=non classifié, 3=3+, 6=6+, 10=10+, 12=12+, 16=16+, 18=18+"},
					"last_maintenance_at": {Type: "integer", Description: "Date de dernière maintenance en Unix ms. Passer -1 pour la date actuelle."},
				},
			},
		},
		{
			Name:        "list_authors",
			Description: "Retourne la liste de tous les auteurs présents dans le catalogue.",
			InputSchema: toolInputSchema{
				Type: "object",
				Properties: map[string]toolPropSchema{
					"limit": {Type: "integer", Description: "Nombre max de résultats (défaut: 100)"},
				},
			},
		},
		{
			Name:        "list_tags",
			Description: "Retourne la liste de tous les tags/genres présents dans le catalogue.",
			InputSchema: toolInputSchema{
				Type: "object",
				Properties: map[string]toolPropSchema{
					"limit": {Type: "integer", Description: "Nombre max de résultats (défaut: 100)"},
				},
			},
		},
		{
			Name:        "fetch_url",
			Description: "Récupère le contenu texte d'une URL (page web d'éditeur, Babelio, ActuSF, Wikipedia…). Utile pour rechercher le résumé officiel d'un livre. Retourne le texte brut de la page (HTML dépouillé), limité à 8000 caractères.",
			InputSchema: toolInputSchema{
				Type:     "object",
				Required: []string{"url"},
				Properties: map[string]toolPropSchema{
					"url": {Type: "string", Description: "URL complète à récupérer (https://...)"},
				},
			},
		},
	}
}

// ─── Chat ──────────────────────────────────────────────────────────────────────

// Chat sends a user prompt and runs the agentic loop, returning the final
// text response from Claude.
func (a *Agent) Chat(ctx context.Context, history []Message, userPrompt string) (string, error) {
	// Build the message history.
	messages := make([]apiMessage, 0, len(history)+1)
	for _, m := range history {
		role := m.Role
		if role != "user" && role != "assistant" {
			role = "user"
		}
		messages = append(messages, apiMessage{
			Role:    role,
			Content: []apiContent{{Type: "text", Text: m.Content}},
		})
	}
	messages = append(messages, apiMessage{
		Role:    "user",
		Content: []apiContent{{Type: "text", Text: userPrompt}},
	})

	systemPrompt := `Tu es un assistant bibliothécaire pour nxt-opds, une application de gestion de bibliothèque numérique personnelle. Tu aides l'utilisateur à trouver des livres, gérer ses lectures, et enrichir les métadonnées du catalogue.

## Outils disponibles
- search_books : rechercher des livres (résultats partiels, sans résumé complet)
- get_book : obtenir les détails complets d'un livre (résumé, age_rating, tags…)
- update_book : modifier les métadonnées d'un livre
- list_authors : lister les auteurs
- list_tags : lister tous les tags/genres existants

## Enrichissement des métadonnées

Quand l'utilisateur demande d'enrichir, corriger ou maintenir des livres, applique ce processus :

**1. Tags**
- Charge list_tags une fois pour connaître le vocabulaire existant
- Dédoublonne (garde la version capitalisée)
- Capitalise chaque tag : "Science-Fiction", "Roman Graphique", etc.
- Ajoute les tags pertinents manquants (genre, thèmes) — 5 à 10 tags au total

**2. Résumé**
- Utilise get_book pour vérifier le résumé existant (search_books ne le retourne pas)
- Si absent ou trop court (≤ 50 caractères), indique que tu peux le rechercher sur Babelio/ActuSF/éditeur
- Ne pas inventer de résumé

**3. Classification d'âge (age_rating)**
Utilise le champ entier age_rating dans update_book — jamais un tag texte :
- 0 = non classifié, 3 = tout public, 6 = dès 6 ans, 10 = jeunesse
- 12 = young adult / ado, 16 = adulte averti, 18 = adulte uniquement
Si age_rating > 0 est déjà renseigné, ne pas le modifier.

**4. Finalisation**
- Fais un seul appel update_book par livre avec tous les champs modifiés
- Inclus toujours last_maintenance_at: -1 pour enregistrer la date de maintenance
- Résume les changements en une ligne par livre

## Instructions générales
- Réponds toujours en français
- Toujours appeler get_book avant de modifier un livre (les données de search_books sont incomplètes)
- Quand tu affiches des livres, utilise un format lisible avec titre, auteur et infos pertinentes`

	// Agentic loop.
	for i := 0; i < maxAgentIterations; i++ {
		resp, err := a.callAPI(ctx, messages, systemPrompt)
		if err != nil {
			return "", err
		}

		// Append assistant response to messages.
		messages = append(messages, apiMessage{
			Role:    "assistant",
			Content: resp.Content,
		})

		// If done, extract and return text.
		if resp.StopReason == "end_turn" || resp.StopReason == "stop_sequence" {
			return extractText(resp.Content), nil
		}

		// If tool use requested, execute tools and add results.
		if resp.StopReason == "tool_use" {
			toolResults := make([]apiContent, 0)
			for _, c := range resp.Content {
				if c.Type == "tool_use" {
					result, isErr := a.executeTool(c.Name, c.Input)
					toolResults = append(toolResults, apiContent{
						Type:      "tool_result",
						ToolUseID: c.ID,
						Content:   result,
						IsError:   isErr,
					})
				}
			}
			if len(toolResults) > 0 {
				messages = append(messages, apiMessage{
					Role:    "user",
					Content: toolResults,
				})
			}
			continue
		}

		// Unknown stop reason – return whatever text we have.
		return extractText(resp.Content), nil
	}

	return "Désolé, la conversation a atteint la limite d'itérations.", nil
}

func extractText(content []apiContent) string {
	var parts []string
	for _, c := range content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// ─── Tool execution ────────────────────────────────────────────────────────────

func (a *Agent) executeTool(name string, rawInput json.RawMessage) (string, bool) {
	var args map[string]any
	if err := json.Unmarshal(rawInput, &args); err != nil {
		return "Erreur de parsing des arguments: " + err.Error(), true
	}

	switch name {
	case "search_books":
		return a.toolSearchBooks(args)
	case "get_book":
		return a.toolGetBook(args)
	case "update_book":
		return a.toolUpdateBook(args)
	case "list_authors":
		return a.toolListAuthors(args)
	case "list_tags":
		return a.toolListTags(args)
	case "fetch_url":
		return a.toolFetchURL(args)
	default:
		return "Outil inconnu: " + name, true
	}
}

func (a *Agent) toolSearchBooks(args map[string]any) (string, bool) {
	q := catalog.SearchQuery{SortBy: "added", SortOrder: "desc", Limit: 20}
	if v, ok := args["query"].(string); ok {
		q.Query = v
	}
	if v, ok := args["author"].(string); ok {
		q.Author = v
	}
	if v, ok := args["tag"].(string); ok {
		q.Tag = v
	}
	if v, ok := args["series"].(string); ok {
		q.Series = v
	}
	if v, ok := args["publisher"].(string); ok {
		q.Publisher = v
	}
	if v, ok := args["collection"].(string); ok {
		q.Collection = v
	}
	if v, ok := args["unread_only"].(bool); ok {
		q.UnreadOnly = v
	}
	if v, ok := args["sort"].(string); ok {
		switch v {
		case "added_desc":
			q.SortBy, q.SortOrder = "added", "desc"
		case "added_asc":
			q.SortBy, q.SortOrder = "added", "asc"
		case "title_asc":
			q.SortBy, q.SortOrder = "title", "asc"
		case "title_desc":
			q.SortBy, q.SortOrder = "title", "desc"
		}
	}
	if v, ok := numArg(args, "limit"); ok && v > 0 {
		if v > 50 {
			v = 50
		}
		q.Limit = v
	}
	if v, ok := numArg(args, "offset"); ok && v >= 0 {
		q.Offset = v
	}

	books, total, err := a.catalog.Search(q)
	if err != nil {
		return "Erreur recherche: " + err.Error(), true
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d livre(s) trouvé(s) (total: %d)\n\n", len(books), total)
	for i, b := range books {
		fmt.Fprintf(&sb, "%d. **%s**\n", i+1, b.Title)
		fmt.Fprintf(&sb, "   ID: `%s`\n", b.ID)
		if len(b.Authors) > 0 {
			names := make([]string, len(b.Authors))
			for j, a := range b.Authors {
				names[j] = a.Name
			}
			fmt.Fprintf(&sb, "   Auteur(s): %s\n", strings.Join(names, ", "))
		}
		if b.Series != "" {
			si := b.Series
			if b.SeriesIndex != "" {
				si += " #" + b.SeriesIndex
			}
			fmt.Fprintf(&sb, "   Série: %s\n", si)
		}
		if len(b.Tags) > 0 {
			fmt.Fprintf(&sb, "   Tags: %s\n", strings.Join(b.Tags, ", "))
		}
		read := "Non lu"
		if b.IsRead {
			read = "Lu"
		}
		fmt.Fprintf(&sb, "   Statut: %s", read)
		if b.Rating > 0 {
			fmt.Fprintf(&sb, " | Note: %s", strings.Repeat("★", b.Rating))
		}
		sb.WriteString("\n\n")
	}
	return sb.String(), false
}

func (a *Agent) toolGetBook(args map[string]any) (string, bool) {
	id, ok := args["id"].(string)
	if !ok || id == "" {
		return "Paramètre 'id' requis", true
	}
	b, err := a.catalog.BookByID(id)
	if err != nil {
		return "Livre non trouvé: " + err.Error(), true
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n", b.Title)
	fmt.Fprintf(&sb, "**ID:** `%s`\n", b.ID)
	if len(b.Authors) > 0 {
		names := make([]string, len(b.Authors))
		for i, a := range b.Authors {
			names[i] = a.Name
		}
		fmt.Fprintf(&sb, "**Auteur(s):** %s\n", strings.Join(names, ", "))
	}
	if b.Publisher != "" {
		fmt.Fprintf(&sb, "**Éditeur:** %s\n", b.Publisher)
	}
	if b.Language != "" {
		fmt.Fprintf(&sb, "**Langue:** %s\n", b.Language)
	}
	if b.Series != "" {
		si := b.Series
		if b.SeriesIndex != "" {
			si += " #" + b.SeriesIndex
			if b.SeriesTotal != "" {
				si += "/" + b.SeriesTotal
			}
		}
		fmt.Fprintf(&sb, "**Série:** %s\n", si)
	}
	if b.Collection != "" {
		ci := b.Collection
		if b.CollectionIndex != "" {
			ci += " #" + b.CollectionIndex
		}
		fmt.Fprintf(&sb, "**Collection:** %s\n", ci)
	}
	if len(b.Tags) > 0 {
		fmt.Fprintf(&sb, "**Tags:** %s\n", strings.Join(b.Tags, ", "))
	}
	readStr := "Non lu"
	if b.IsRead {
		readStr = "Lu ✓"
	}
	fmt.Fprintf(&sb, "**Statut:** %s\n", readStr)
	if b.Rating > 0 {
		fmt.Fprintf(&sb, "**Note:** %s\n", strings.Repeat("★", b.Rating)+strings.Repeat("☆", 5-b.Rating))
	}
	if b.Summary != "" {
		fmt.Fprintf(&sb, "\n**Résumé:** %s\n", b.Summary)
	}
	return sb.String(), false
}

func (a *Agent) toolUpdateBook(args map[string]any) (string, bool) {
	if a.updater == nil {
		return "Le backend ne supporte pas la modification", true
	}
	id, ok := args["id"].(string)
	if !ok || id == "" {
		return "Paramètre 'id' requis", true
	}

	upd := catalog.BookUpdate{}
	if v, ok := args["title"].(string); ok {
		upd.Title = &v
	}
	if v, ok := args["summary"].(string); ok {
		upd.Summary = &v
	}
	if v, ok := args["publisher"].(string); ok {
		upd.Publisher = &v
	}
	if v, ok := args["language"].(string); ok {
		upd.Language = &v
	}
	if v, ok := args["series"].(string); ok {
		upd.Series = &v
	}
	if v, ok := args["series_index"].(string); ok {
		upd.SeriesIndex = &v
	}
	if v, ok := args["series_total"].(string); ok {
		upd.SeriesTotal = &v
	}
	if v, ok := args["collection"].(string); ok {
		upd.Collection = &v
	}
	if v, ok := args["collection_index"].(string); ok {
		upd.CollectionIndex = &v
	}
	if v, ok := args["is_read"].(bool); ok {
		upd.IsRead = &v
	}
	if v, ok := numArg(args, "rating"); ok {
		if v < 0 {
			v = 0
		}
		if v > 5 {
			v = 5
		}
		upd.Rating = &v
	}
	if v, ok := numArg(args, "age_rating"); ok {
		upd.AgeRating = &v
	}
	if v, ok := numArg(args, "last_maintenance_at"); ok {
		var t time.Time
		if v == -1 {
			t = time.Now()
		} else if v > 0 {
			t = time.UnixMilli(int64(v))
		}
		upd.LastMaintenanceAt = &t
	}
	if raw, ok := args["authors"]; ok {
		if arr, ok := raw.([]any); ok {
			authors := make([]string, 0, len(arr))
			for _, x := range arr {
				if s, ok := x.(string); ok {
					authors = append(authors, s)
				}
			}
			upd.Authors = authors
		}
	}
	if raw, ok := args["tags"]; ok {
		if arr, ok := raw.([]any); ok {
			tags := make([]string, 0, len(arr))
			for _, x := range arr {
				if s, ok := x.(string); ok {
					tags = append(tags, s)
				}
			}
			upd.Tags = tags
		}
	}

	b, err := a.updater.UpdateBook(id, upd)
	if err != nil {
		return "Erreur mise à jour: " + err.Error(), true
	}
	return fmt.Sprintf("Livre **%s** mis à jour avec succès.", b.Title), false
}

func (a *Agent) toolListAuthors(args map[string]any) (string, bool) {
	limit := 100
	if v, ok := numArg(args, "limit"); ok && v > 0 {
		limit = v
	}
	authors, total, err := a.catalog.Authors(0, limit)
	if err != nil {
		return "Erreur: " + err.Error(), true
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d auteur(s) :\n", total)
	for _, a := range authors {
		fmt.Fprintf(&sb, "- %s\n", a)
	}
	return sb.String(), false
}

func (a *Agent) toolListTags(args map[string]any) (string, bool) {
	limit := 100
	if v, ok := numArg(args, "limit"); ok && v > 0 {
		limit = v
	}
	tags, total, err := a.catalog.Tags(0, limit)
	if err != nil {
		return "Erreur: " + err.Error(), true
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d tag(s) :\n", total)
	for _, t := range tags {
		fmt.Fprintf(&sb, "- %s\n", t)
	}
	return sb.String(), false
}

var (
	reHTMLTags    = regexp.MustCompile(`<[^>]+>`)
	reWhitespace  = regexp.MustCompile(`\s{2,}`)
)

func (a *Agent) toolFetchURL(args map[string]any) (string, bool) {
	url, ok := args["url"].(string)
	if !ok || url == "" {
		return "Paramètre 'url' requis", true
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "URL invalide : doit commencer par https://", true
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "Erreur création requête: " + err.Error(), true
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; nxt-opds-agent/1.0)")
	req.Header.Set("Accept", "text/html,text/plain")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "Erreur fetch: " + err.Error(), true
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Sprintf("HTTP %d pour %s", resp.StatusCode, url), true
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 200*1024))
	if err != nil {
		return "Erreur lecture: " + err.Error(), true
	}

	// Strip HTML and normalise whitespace.
	text := reHTMLTags.ReplaceAllString(string(body), " ")
	text = html.UnescapeString(text)
	text = reWhitespace.ReplaceAllString(text, " ")
	text = strings.TrimSpace(text)

	const maxChars = 8000
	if len([]rune(text)) > maxChars {
		runes := []rune(text)
		text = string(runes[:maxChars]) + "…"
	}
	return text, false
}

// ─── HTTP API call ─────────────────────────────────────────────────────────────

func (a *Agent) callAPI(ctx context.Context, messages []apiMessage, system string) (*apiResponse, error) {
	reqBody := apiRequest{
		Model:     defaultModel,
		MaxTokens: defaultMaxTokens,
		System:    system,
		Tools:     a.toolDefs(),
		Messages:  messages,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicAPIURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var apiResp apiResponse
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if apiResp.Error != nil {
		return nil, fmt.Errorf("API error %s: %s", apiResp.Error.Type, apiResp.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned HTTP %d: %s", resp.StatusCode, string(respBytes))
	}

	return &apiResp, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func numArg(args map[string]any, key string) (int, bool) {
	v, ok := args[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}
