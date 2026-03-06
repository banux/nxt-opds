// Package mcp implements a Model Context Protocol (MCP) server for nxt-opds.
//
// The MCP server exposes the book catalog as tools that AI agents can use
// to search books and modify metadata.  It implements the MCP 2024-11-05
// specification over the Streamable HTTP transport:
//
//	POST /mcp   – JSON-RPC 2.0 request → JSON-RPC 2.0 response
//
// Authentication uses the OPDS bearer token:
//
//	Authorization: Bearer <opds_token>
package mcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/banux/nxt-opds/internal/catalog"
)

const protocolVersion = "2024-11-05"

// Server handles MCP requests over HTTP.
type Server struct {
	cat             catalog.Catalog
	updater         catalog.Updater
	seriesLister    catalog.SeriesLister
	uploader        catalog.Uploader
	wishlistManager catalog.WishlistManager
	recommender     catalog.Recommender
	userManager     catalog.UserManager
}

// New creates a new MCP Server backed by the given catalog.
func New(cat catalog.Catalog) *Server {
	s := &Server{cat: cat}
	if u, ok := cat.(catalog.Updater); ok {
		s.updater = u
	}
	if sl, ok := cat.(catalog.SeriesLister); ok {
		s.seriesLister = sl
	}
	if up, ok := cat.(catalog.Uploader); ok {
		s.uploader = up
	}
	if wm, ok := cat.(catalog.WishlistManager); ok {
		s.wishlistManager = wm
	}
	if rc, ok := cat.(catalog.Recommender); ok {
		s.recommender = rc
	}
	if um, ok := cat.(catalog.UserManager); ok {
		s.userManager = um
	}
	return s
}

// ─── JSON-RPC 2.0 types ───────────────────────────────────────────────────────

type rpcRequest struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Result  any              `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ─── MCP protocol types ───────────────────────────────────────────────────────

type initializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	ServerInfo      map[string]any `json:"serverInfo"`
	Capabilities    map[string]any `json:"capabilities"`
}

type toolDef struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	InputSchema *jsonSchema `json:"inputSchema"`
}

type jsonSchema struct {
	Type        string                `json:"type"`
	Description string                `json:"description,omitempty"`
	Properties  map[string]*jsonSchema `json:"properties,omitempty"`
	Required    []string              `json:"required,omitempty"`
	Items       *jsonSchema           `json:"items,omitempty"`
	Enum        []string              `json:"enum,omitempty"`
}

type toolsListResult struct {
	Tools []toolDef `json:"tools"`
}

type toolsCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type toolsCallResult struct {
	Content []contentItem `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// ─── HTTP handler ─────────────────────────────────────────────────────────────

// ServeHTTP handles a single MCP HTTP request/response cycle.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPCError(w, nil, -32700, "Parse error: "+err.Error())
		return
	}
	if req.JSONRPC != "2.0" {
		writeRPCError(w, req.ID, -32600, "Invalid Request: jsonrpc must be \"2.0\"")
		return
	}

	var result any
	var rpcErr *rpcError

	switch req.Method {
	case "initialize":
		result = s.handleInitialize()

	case "notifications/initialized":
		// Notification – no response body required by spec.
		w.WriteHeader(http.StatusAccepted)
		return

	case "ping":
		result = map[string]any{}

	case "tools/list":
		result = s.toolsList()

	case "tools/call":
		result, rpcErr = s.handleToolsCall(req.Params)

	default:
		rpcErr = &rpcError{Code: -32601, Message: "Method not found: " + req.Method}
	}

	resp := rpcResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
		Error:   rpcErr,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func writeRPCError(w http.ResponseWriter, id *json.RawMessage, code int, msg string) {
	resp := rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: msg},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// ─── Method handlers ──────────────────────────────────────────────────────────

func (s *Server) handleInitialize() initializeResult {
	return initializeResult{
		ProtocolVersion: protocolVersion,
		ServerInfo: map[string]any{
			"name":    "nxt-opds",
			"version": "1.30.0",
		},
		Capabilities: map[string]any{
			"tools": map[string]any{},
		},
	}
}

func (s *Server) toolsList() toolsListResult {
	tools := []toolDef{
		{
			Name:        "search_books",
			Description: "Recherche des livres dans le catalogue avec des filtres optionnels. Retourne une liste de livres correspondant aux critères.",
			InputSchema: &jsonSchema{
				Type: "object",
				Properties: map[string]*jsonSchema{
					"query":       {Type: "string", Description: "Texte de recherche (titre, auteur, description)"},
					"author":      {Type: "string", Description: "Filtre par nom d'auteur (correspondance partielle)"},
					"tag":         {Type: "string", Description: "Filtre par tag/genre exact"},
					"series":      {Type: "string", Description: "Filtre par nom de série exact"},
					"publisher":   {Type: "string", Description: "Filtre par nom d'éditeur exact"},
					"collection":  {Type: "string", Description: "Filtre par collection éditoriale exacte"},
					"unread_only": {Type: "boolean", Description: "Si vrai, retourne uniquement les livres non lus"},
					"sort":        {Type: "string", Description: "Tri : added_desc (défaut), added_asc, title_asc, title_desc", Enum: []string{"added_desc", "added_asc", "title_asc", "title_desc"}},
					"limit":       {Type: "integer", Description: "Nombre maximum de résultats (défaut: 20, max: 100)"},
					"offset":      {Type: "integer", Description: "Décalage pour la pagination (défaut: 0)"},
				},
			},
		},
		{
			Name:        "get_book",
			Description: "Récupère les détails complets d'un livre par son identifiant unique.",
			InputSchema: &jsonSchema{
				Type:     "object",
				Required: []string{"id"},
				Properties: map[string]*jsonSchema{
					"id": {Type: "string", Description: "Identifiant unique du livre"},
				},
			},
		},
		{
			Name:        "update_book",
			Description: "Modifie les métadonnées d'un livre. Seuls les champs fournis sont mis à jour ; les autres restent inchangés.",
			InputSchema: &jsonSchema{
				Type:     "object",
				Required: []string{"id"},
				Properties: map[string]*jsonSchema{
					"id":               {Type: "string", Description: "Identifiant unique du livre"},
					"title":            {Type: "string", Description: "Nouveau titre"},
					"authors":          {Type: "array", Description: "Liste des auteurs (remplace la liste existante)", Items: &jsonSchema{Type: "string"}},
					"tags":             {Type: "array", Description: "Liste des tags/genres (remplace la liste existante)", Items: &jsonSchema{Type: "string"}},
					"summary":          {Type: "string", Description: "Résumé/description du livre"},
					"publisher":        {Type: "string", Description: "Nom de l'éditeur"},
					"language":         {Type: "string", Description: "Code de langue BCP 47 (ex: fr, en)"},
					"series":           {Type: "string", Description: "Nom de la série"},
					"series_index":     {Type: "string", Description: "Numéro dans la série (ex: 1, 2.5)"},
					"series_total":     {Type: "string", Description: "Nombre total de livres dans la série"},
					"collection":       {Type: "string", Description: "Nom de la collection éditoriale"},
					"collection_index": {Type: "string", Description: "Numéro dans la collection"},
					"is_read":          {Type: "boolean", Description: "Marquer comme lu (true) ou non lu (false)"},
					"rating":           {Type: "integer", Description: "Note de 0 (non noté) à 5 étoiles"},
				},
			},
		},
		{
			Name:        "list_authors",
			Description: "Retourne la liste de tous les auteurs distincts présents dans le catalogue.",
			InputSchema: &jsonSchema{
				Type: "object",
				Properties: map[string]*jsonSchema{
					"limit":  {Type: "integer", Description: "Nombre maximum de résultats (défaut: 200)"},
					"offset": {Type: "integer", Description: "Décalage pour la pagination"},
				},
			},
		},
		{
			Name:        "list_tags",
			Description: "Retourne la liste de tous les tags/genres distincts présents dans le catalogue.",
			InputSchema: &jsonSchema{
				Type: "object",
				Properties: map[string]*jsonSchema{
					"limit":  {Type: "integer", Description: "Nombre maximum de résultats (défaut: 200)"},
					"offset": {Type: "integer", Description: "Décalage pour la pagination"},
				},
			},
		},
		{
			Name:        "list_series",
			Description: "Retourne la liste de toutes les séries avec le nombre de livres par série.",
			InputSchema: &jsonSchema{
				Type:       "object",
				Properties: map[string]*jsonSchema{},
			},
		},
		{
			Name:        "list_publishers",
			Description: "Retourne la liste de tous les éditeurs distincts présents dans le catalogue.",
			InputSchema: &jsonSchema{
				Type: "object",
				Properties: map[string]*jsonSchema{
					"limit":  {Type: "integer", Description: "Nombre maximum de résultats (défaut: 200)"},
					"offset": {Type: "integer", Description: "Décalage pour la pagination"},
				},
			},
		},
		{
			Name:        "upload_book",
			Description: "Téléverse un livre (EPUB ou PDF) dans le catalogue à partir de son contenu encodé en base64. Retourne les métadonnées du livre indexé.",
			InputSchema: &jsonSchema{
				Type:     "object",
				Required: []string{"filename", "content"},
				Properties: map[string]*jsonSchema{
					"filename": {Type: "string", Description: "Nom du fichier avec extension (ex: monlivre.epub, roman.pdf)"},
					"content":  {Type: "string", Description: "Contenu du fichier encodé en base64"},
				},
			},
		},
		{
			Name:        "list_wishlist",
			Description: "Retourne la liste des livres souhaités (wishlist). Si user_id est fourni, filtre par utilisateur.",
			InputSchema: &jsonSchema{
				Type: "object",
				Properties: map[string]*jsonSchema{
					"user_id": {Type: "string", Description: "Identifiant de l'utilisateur (optionnel, vide = tous les utilisateurs)"},
				},
			},
		},
		{
			Name:        "add_wishlist_item",
			Description: "Ajoute un livre à la liste de souhaits.",
			InputSchema: &jsonSchema{
				Type:     "object",
				Required: []string{"title"},
				Properties: map[string]*jsonSchema{
					"title":        {Type: "string", Description: "Titre du livre recherché"},
					"author":       {Type: "string", Description: "Auteur du livre (optionnel)"},
					"release_date": {Type: "string", Description: "Date de parution (optionnel, ex: 2024 ou 2024-03-15)"},
					"notes":        {Type: "string", Description: "Notes supplémentaires (optionnel)"},
					"user_id":      {Type: "string", Description: "Identifiant de l'utilisateur (optionnel)"},
				},
			},
		},
		{
			Name:        "delete_wishlist_item",
			Description: "Supprime un élément de la liste de souhaits par son identifiant.",
			InputSchema: &jsonSchema{
				Type:     "object",
				Required: []string{"id"},
				Properties: map[string]*jsonSchema{
					"id": {Type: "string", Description: "Identifiant de l'élément à supprimer"},
				},
			},
		},
		{
			Name:        "list_recommendations",
			Description: "Retourne la liste des recommandations de livres entre utilisateurs. Si to_user_id est fourni, filtre les recommandations destinées à cet utilisateur.",
			InputSchema: &jsonSchema{
				Type: "object",
				Properties: map[string]*jsonSchema{
					"to_user_id": {Type: "string", Description: "Identifiant du destinataire (optionnel, vide = toutes les recommandations)"},
				},
			},
		},
	}
	return toolsListResult{Tools: tools}
}

func (s *Server) handleToolsCall(raw json.RawMessage) (any, *rpcError) {
	var p toolsCallParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "Invalid params: " + err.Error()}
	}

	switch p.Name {
	case "search_books":
		return s.toolSearchBooks(p.Arguments)
	case "get_book":
		return s.toolGetBook(p.Arguments)
	case "update_book":
		return s.toolUpdateBook(p.Arguments)
	case "list_authors":
		return s.toolListAuthors(p.Arguments)
	case "list_tags":
		return s.toolListTags(p.Arguments)
	case "list_series":
		return s.toolListSeries()
	case "list_publishers":
		return s.toolListPublishers(p.Arguments)
	case "upload_book":
		return s.toolUploadBook(p.Arguments)
	case "list_wishlist":
		return s.toolListWishlist(p.Arguments)
	case "add_wishlist_item":
		return s.toolAddWishlistItem(p.Arguments)
	case "delete_wishlist_item":
		return s.toolDeleteWishlistItem(p.Arguments)
	case "list_recommendations":
		return s.toolListRecommendations(p.Arguments)
	default:
		return nil, &rpcError{Code: -32602, Message: "Unknown tool: " + p.Name}
	}
}

// ─── Tool implementations ─────────────────────────────────────────────────────

func (s *Server) toolSearchBooks(args map[string]any) (any, *rpcError) {
	q := catalog.SearchQuery{
		SortBy:    "added",
		SortOrder: "desc",
	}

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

	limit := 20
	if v, ok := numericArg(args, "limit"); ok && v > 0 {
		if v > 100 {
			v = 100
		}
		limit = v
	}
	offset := 0
	if v, ok := numericArg(args, "offset"); ok && v > 0 {
		offset = v
	}
	q.Limit = limit
	q.Offset = offset

	books, total, err := s.cat.Search(q)
	if err != nil {
		return errorResult("Erreur lors de la recherche : " + err.Error()), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Trouvé %d livre(s) au total (page : %d/%d)\n\n", total, offset/limit+1, (total+limit-1)/limit)
	for i, b := range books {
		sb.WriteString(formatBook(i+offset+1, &b))
	}

	return textResult(sb.String()), nil
}

func (s *Server) toolGetBook(args map[string]any) (any, *rpcError) {
	id, ok := args["id"].(string)
	if !ok || id == "" {
		return nil, &rpcError{Code: -32602, Message: "Paramètre 'id' requis"}
	}

	b, err := s.cat.BookByID(id)
	if err != nil {
		return errorResult("Livre non trouvé : " + err.Error()), nil
	}

	return textResult(formatBookDetail(b)), nil
}

func (s *Server) toolUpdateBook(args map[string]any) (any, *rpcError) {
	if s.updater == nil {
		return errorResult("Le backend ne supporte pas la modification de métadonnées"), nil
	}

	id, ok := args["id"].(string)
	if !ok || id == "" {
		return nil, &rpcError{Code: -32602, Message: "Paramètre 'id' requis"}
	}

	update := catalog.BookUpdate{}

	if v, ok := args["title"].(string); ok {
		update.Title = &v
	}
	if v, ok := args["summary"].(string); ok {
		update.Summary = &v
	}
	if v, ok := args["publisher"].(string); ok {
		update.Publisher = &v
	}
	if v, ok := args["language"].(string); ok {
		update.Language = &v
	}
	if v, ok := args["series"].(string); ok {
		update.Series = &v
	}
	if v, ok := args["series_index"].(string); ok {
		update.SeriesIndex = &v
	}
	if v, ok := args["series_total"].(string); ok {
		update.SeriesTotal = &v
	}
	if v, ok := args["collection"].(string); ok {
		update.Collection = &v
	}
	if v, ok := args["collection_index"].(string); ok {
		update.CollectionIndex = &v
	}
	if v, ok := args["is_read"].(bool); ok {
		update.IsRead = &v
	}
	if v, ok := numericArg(args, "rating"); ok {
		if v < 0 {
			v = 0
		}
		if v > 5 {
			v = 5
		}
		update.Rating = &v
	}

	// authors: array of strings
	if raw, ok := args["authors"]; ok {
		if arr, ok := raw.([]any); ok {
			authors := make([]string, 0, len(arr))
			for _, a := range arr {
				if s, ok := a.(string); ok {
					authors = append(authors, s)
				}
			}
			update.Authors = authors
		}
	}

	// tags: array of strings
	if raw, ok := args["tags"]; ok {
		if arr, ok := raw.([]any); ok {
			tags := make([]string, 0, len(arr))
			for _, t := range arr {
				if s, ok := t.(string); ok {
					tags = append(tags, s)
				}
			}
			update.Tags = tags
		}
	}

	b, err := s.updater.UpdateBook(id, update)
	if err != nil {
		return errorResult("Erreur lors de la mise à jour : " + err.Error()), nil
	}

	return textResult("Livre mis à jour avec succès.\n\n" + formatBookDetail(b)), nil
}

func (s *Server) toolListAuthors(args map[string]any) (any, *rpcError) {
	limit := 200
	if v, ok := numericArg(args, "limit"); ok && v > 0 {
		limit = v
	}
	offset := 0
	if v, ok := numericArg(args, "offset"); ok && v > 0 {
		offset = v
	}

	authors, total, err := s.cat.Authors(offset, limit)
	if err != nil {
		return errorResult("Erreur : " + err.Error()), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d auteur(s) au total :\n", total)
	for _, a := range authors {
		fmt.Fprintf(&sb, "- %s\n", a)
	}
	return textResult(sb.String()), nil
}

func (s *Server) toolListTags(args map[string]any) (any, *rpcError) {
	limit := 200
	if v, ok := numericArg(args, "limit"); ok && v > 0 {
		limit = v
	}
	offset := 0
	if v, ok := numericArg(args, "offset"); ok && v > 0 {
		offset = v
	}

	tags, total, err := s.cat.Tags(offset, limit)
	if err != nil {
		return errorResult("Erreur : " + err.Error()), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d tag(s) au total :\n", total)
	for _, t := range tags {
		fmt.Fprintf(&sb, "- %s\n", t)
	}
	return textResult(sb.String()), nil
}

func (s *Server) toolListSeries() (any, *rpcError) {
	if s.seriesLister == nil {
		return errorResult("Le backend ne supporte pas la liste des séries"), nil
	}

	entries, err := s.seriesLister.Series()
	if err != nil {
		return errorResult("Erreur : " + err.Error()), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d série(s) :\n", len(entries))
	for _, e := range entries {
		fmt.Fprintf(&sb, "- %s (%d livre(s))\n", e.Name, e.Count)
	}
	return textResult(sb.String()), nil
}

func (s *Server) toolListPublishers(args map[string]any) (any, *rpcError) {
	limit := 200
	if v, ok := numericArg(args, "limit"); ok && v > 0 {
		limit = v
	}
	offset := 0
	if v, ok := numericArg(args, "offset"); ok && v > 0 {
		offset = v
	}

	publishers, total, err := s.cat.Publishers(offset, limit)
	if err != nil {
		return errorResult("Erreur : " + err.Error()), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d éditeur(s) au total :\n", total)
	for _, p := range publishers {
		fmt.Fprintf(&sb, "- %s\n", p)
	}
	return textResult(sb.String()), nil
}

func (s *Server) toolUploadBook(args map[string]any) (any, *rpcError) {
	if s.uploader == nil {
		return errorResult("Le backend ne supporte pas le téléversement de livres"), nil
	}

	filename, ok := args["filename"].(string)
	if !ok || filename == "" {
		return nil, &rpcError{Code: -32602, Message: "Paramètre 'filename' requis"}
	}
	encoded, ok := args["content"].(string)
	if !ok || encoded == "" {
		return nil, &rpcError{Code: -32602, Message: "Paramètre 'content' requis (base64)"}
	}

	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		// Try URL-safe base64 as fallback.
		data, err = base64.URLEncoding.DecodeString(encoded)
		if err != nil {
			return errorResult("Contenu base64 invalide : " + err.Error()), nil
		}
	}

	rc := io.NopCloser(bytes.NewReader(data))
	book, err := s.uploader.StoreBook(filename, rc)
	if err != nil {
		return errorResult("Erreur lors du téléversement : " + err.Error()), nil
	}

	return textResult("Livre téléversé avec succès.\n\n" + formatBookDetail(book)), nil
}

func (s *Server) toolListWishlist(args map[string]any) (any, *rpcError) {
	if s.wishlistManager == nil {
		return errorResult("Le backend ne supporte pas la liste de souhaits"), nil
	}
	userID, _ := args["user_id"].(string)
	items, err := s.wishlistManager.WishlistItems(userID)
	if err != nil {
		return errorResult("Erreur : " + err.Error()), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d élément(s) dans la liste de souhaits :\n\n", len(items))
	for i, it := range items {
		fmt.Fprintf(&sb, "%d. **%s**\n", i+1, it.Title)
		fmt.Fprintf(&sb, "   ID: %s\n", it.ID)
		if it.Author != "" {
			fmt.Fprintf(&sb, "   Auteur: %s\n", it.Author)
		}
		if it.ReleaseDate != "" {
			fmt.Fprintf(&sb, "   Date de parution: %s\n", it.ReleaseDate)
		}
		if it.Notes != "" {
			fmt.Fprintf(&sb, "   Notes: %s\n", it.Notes)
		}
		if it.UserName != "" {
			fmt.Fprintf(&sb, "   Souhaité par: %s\n", it.UserName)
		}
		fmt.Fprintf(&sb, "   Ajouté le: %s\n\n", it.CreatedAt.Format("2006-01-02"))
	}
	return textResult(sb.String()), nil
}

func (s *Server) toolAddWishlistItem(args map[string]any) (any, *rpcError) {
	if s.wishlistManager == nil {
		return errorResult("Le backend ne supporte pas la liste de souhaits"), nil
	}
	title, ok := args["title"].(string)
	if !ok || title == "" {
		return nil, &rpcError{Code: -32602, Message: "Paramètre 'title' requis"}
	}
	author, _ := args["author"].(string)
	releaseDate, _ := args["release_date"].(string)
	notes, _ := args["notes"].(string)
	userID, _ := args["user_id"].(string)

	it, err := s.wishlistManager.AddWishlistItem(userID, title, author, releaseDate, notes)
	if err != nil {
		return errorResult("Erreur lors de l'ajout : " + err.Error()), nil
	}
	return textResult(fmt.Sprintf("Élément ajouté à la liste de souhaits.\nID: %s\nTitre: %s", it.ID, it.Title)), nil
}

func (s *Server) toolDeleteWishlistItem(args map[string]any) (any, *rpcError) {
	if s.wishlistManager == nil {
		return errorResult("Le backend ne supporte pas la liste de souhaits"), nil
	}
	id, ok := args["id"].(string)
	if !ok || id == "" {
		return nil, &rpcError{Code: -32602, Message: "Paramètre 'id' requis"}
	}
	if err := s.wishlistManager.DeleteWishlistItem(id); err != nil {
		return errorResult("Erreur lors de la suppression : " + err.Error()), nil
	}
	return textResult("Élément supprimé de la liste de souhaits."), nil
}

func (s *Server) toolListRecommendations(args map[string]any) (any, *rpcError) {
	if s.recommender == nil {
		return errorResult("Le backend ne supporte pas les recommandations"), nil
	}

	toUserID, _ := args["to_user_id"].(string)

	var recs []catalog.Recommendation
	var err error
	if toUserID != "" {
		recs, err = s.recommender.RecommendationsForUser(toUserID)
	} else {
		// Return all recommendations for all users (requires UserManager).
		if s.userManager == nil {
			return errorResult("Le backend ne supporte pas la gestion des utilisateurs"), nil
		}
		users, uerr := s.userManager.Users()
		if uerr != nil {
			return errorResult("Erreur lors de la récupération des utilisateurs : " + uerr.Error()), nil
		}
		seen := map[string]bool{}
		for _, u := range users {
			urecs, rerr := s.recommender.RecommendationsForUser(u.ID)
			if rerr != nil {
				continue
			}
			for _, rec := range urecs {
				key := rec.Book.ID + ":" + rec.ToUser.ID
				if seen[key] {
					continue
				}
				seen[key] = true
				recs = append(recs, rec)
			}
		}
	}
	if err != nil {
		return errorResult("Erreur : " + err.Error()), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d recommandation(s) :\n\n", len(recs))
	for i, rec := range recs {
		fmt.Fprintf(&sb, "%d. **%s**\n", i+1, rec.Book.Title)
		fmt.Fprintf(&sb, "   ID du livre: %s\n", rec.Book.ID)
		fmt.Fprintf(&sb, "   De: %s → À: %s\n", rec.FromUser.Name, rec.ToUser.Name)
		if rec.Message != "" {
			fmt.Fprintf(&sb, "   Message: %s\n", rec.Message)
		}
		fmt.Fprintf(&sb, "   Date: %s\n\n", rec.CreatedAt.Format("2006-01-02"))
	}
	return textResult(sb.String()), nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func textResult(text string) toolsCallResult {
	return toolsCallResult{Content: []contentItem{{Type: "text", Text: text}}}
}

func errorResult(msg string) toolsCallResult {
	return toolsCallResult{
		Content: []contentItem{{Type: "text", Text: msg}},
		IsError: true,
	}
}

// numericArg extracts a numeric value from the args map, handling both
// float64 (JSON number default) and string representations.
func numericArg(args map[string]any, key string) (int, bool) {
	v, ok := args[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case string:
		if i, err := strconv.Atoi(n); err == nil {
			return i, true
		}
	}
	return 0, false
}

func formatBook(idx int, b *catalog.Book) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d. **%s**\n", idx, b.Title)
	fmt.Fprintf(&sb, "   ID: %s\n", b.ID)
	if len(b.Authors) > 0 {
		names := make([]string, len(b.Authors))
		for i, a := range b.Authors {
			names[i] = a.Name
		}
		fmt.Fprintf(&sb, "   Auteur(s): %s\n", strings.Join(names, ", "))
	}
	if b.Series != "" {
		serInfo := b.Series
		if b.SeriesIndex != "" {
			serInfo += " #" + b.SeriesIndex
		}
		fmt.Fprintf(&sb, "   Série: %s\n", serInfo)
	}
	if b.Publisher != "" {
		fmt.Fprintf(&sb, "   Éditeur: %s\n", b.Publisher)
	}
	if len(b.Tags) > 0 {
		fmt.Fprintf(&sb, "   Tags: %s\n", strings.Join(b.Tags, ", "))
	}
	if b.IsRead {
		sb.WriteString("   Statut: Lu ✓\n")
	}
	if b.Rating > 0 {
		fmt.Fprintf(&sb, "   Note: %s\n", stars(b.Rating))
	}
	sb.WriteString("\n")
	return sb.String()
}

func formatBookDetail(b *catalog.Book) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n", b.Title)
	fmt.Fprintf(&sb, "**ID:** %s\n", b.ID)

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
	if !b.PublishedAt.IsZero() {
		fmt.Fprintf(&sb, "**Publié le:** %s\n", b.PublishedAt.Format("2006-01-02"))
	}
	if b.Series != "" {
		serInfo := b.Series
		if b.SeriesIndex != "" {
			serInfo += " #" + b.SeriesIndex
			if b.SeriesTotal != "" {
				serInfo += "/" + b.SeriesTotal
			}
		}
		fmt.Fprintf(&sb, "**Série:** %s\n", serInfo)
	}
	if b.Collection != "" {
		colInfo := b.Collection
		if b.CollectionIndex != "" {
			colInfo += " #" + b.CollectionIndex
		}
		fmt.Fprintf(&sb, "**Collection:** %s\n", colInfo)
	}
	if len(b.Tags) > 0 {
		fmt.Fprintf(&sb, "**Tags:** %s\n", strings.Join(b.Tags, ", "))
	}
	if b.IsRead {
		sb.WriteString("**Statut:** Lu ✓\n")
	} else {
		sb.WriteString("**Statut:** Non lu\n")
	}
	if b.Rating > 0 {
		fmt.Fprintf(&sb, "**Note:** %s\n", stars(b.Rating))
	}
	if b.Summary != "" {
		fmt.Fprintf(&sb, "\n**Résumé:**\n%s\n", b.Summary)
	}
	if len(b.Files) > 0 {
		sb.WriteString("\n**Fichiers:**\n")
		for _, f := range b.Files {
			fmt.Fprintf(&sb, "- %s (%s, %s)\n", filepath_base(f.Path), f.MIMEType, formatSize(f.Size))
		}
	}
	return sb.String()
}

func stars(n int) string {
	return strings.Repeat("★", n) + strings.Repeat("☆", 5-n)
}

func filepath_base(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}

func formatSize(size int64) string {
	if size <= 0 {
		return "taille inconnue"
	}
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}
