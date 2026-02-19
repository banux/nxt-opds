package server

import (
	"crypto/subtle"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html/template"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gorilla/mux"

	"github.com/banux/nxt-opds/internal/catalog"
	"github.com/banux/nxt-opds/internal/opds"
)

const (
	defaultPageSize = 50
	maxPageSize     = 200
)

// writeOPDS writes an OPDS XML feed response.
func writeOPDS(w http.ResponseWriter, status int, feed *opds.Feed) {
	data, err := feed.MarshalToXML()
	if err != nil {
		http.Error(w, "feed serialization error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", opds.MIMENavigationFeed+"; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// parsePagination extracts offset and limit from query parameters.
func parsePagination(r *http.Request) (offset, limit int) {
	q := r.URL.Query()
	offset, _ = strconv.Atoi(q.Get("offset"))
	limit, _ = strconv.Atoi(q.Get("limit"))
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > maxPageSize {
		limit = defaultPageSize
	}
	return
}

// bookToEntry converts a catalog.Book to an opds.Entry for an acquisition feed.
func bookToEntry(b catalog.Book) opds.Entry {
	entry := opds.Entry{
		ID:      "urn:nxt-opds:book:" + b.ID,
		Title:   opds.Text{Value: b.Title},
		Updated: opds.AtomDate{Time: b.UpdatedAt},
	}

	if b.Summary != "" {
		entry.Summary = &opds.Text{Value: b.Summary}
	}

	if !b.PublishedAt.IsZero() {
		entry.Published = b.PublishedAt.UTC().Format(time.RFC3339)
	}

	for _, a := range b.Authors {
		entry.Authors = append(entry.Authors, opds.Author{Name: a.Name, URI: a.URI})
	}

	// Acquisition links for each available file
	for _, f := range b.Files {
		entry.Links = append(entry.Links, opds.Link{
			Rel:  opds.RelAcquisition,
			Href: "/opds/books/" + b.ID + "/download?path=" + url.QueryEscape(f.Path),
			Type: f.MIMEType,
		})
	}

	if b.CoverURL != "" {
		entry.Links = append(entry.Links, opds.Link{
			Rel:  opds.RelCover,
			Href: b.CoverURL,
			Type: "image/jpeg",
		})
	}
	if b.ThumbnailURL != "" {
		entry.Links = append(entry.Links, opds.Link{
			Rel:  opds.RelThumbnail,
			Href: b.ThumbnailURL,
			Type: "image/jpeg",
		})
	}

	return entry
}

// handleRoot serves the root OPDS navigation feed.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	feed := opds.NewNavigationFeed(
		"urn:nxt-opds:root",
		"nxt-opds Catalog",
	)
	feed.Author = &opds.Author{Name: "nxt-opds"}

	// Self link
	feed.AddLink(opds.RelSelf, "/opds", opds.MIMENavigationFeed)
	// Start link (root)
	feed.AddLink(opds.RelStart, "/opds", opds.MIMENavigationFeed)
	// Search link
	feed.AddLink(opds.RelSearch, "/opds/opensearch.xml", opds.MIMEOpenSearchDesc)

	now := time.Now()

	// Navigation entries
	feed.AddEntry(opds.Entry{
		ID:      "urn:nxt-opds:all-books",
		Title:   opds.Text{Value: "All Books"},
		Updated: opds.AtomDate{Time: now},
		Content: &opds.Content{Type: "text", Value: "Browse all books in the catalog"},
		Links: []opds.Link{
			{Rel: opds.RelCatalogNavigation, Href: "/opds/books", Type: opds.MIMEAcquisitionFeed},
		},
	})

	feed.AddEntry(opds.Entry{
		ID:      "urn:nxt-opds:by-author",
		Title:   opds.Text{Value: "By Author"},
		Updated: opds.AtomDate{Time: now},
		Content: &opds.Content{Type: "text", Value: "Browse books by author"},
		Links: []opds.Link{
			{Rel: opds.RelCatalogNavigation, Href: "/opds/authors", Type: opds.MIMENavigationFeed},
		},
	})

	feed.AddEntry(opds.Entry{
		ID:      "urn:nxt-opds:by-tag",
		Title:   opds.Text{Value: "By Genre"},
		Updated: opds.AtomDate{Time: now},
		Content: &opds.Content{Type: "text", Value: "Browse books by genre/tag"},
		Links: []opds.Link{
			{Rel: opds.RelCatalogNavigation, Href: "/opds/tags", Type: opds.MIMENavigationFeed},
		},
	})

	writeOPDS(w, http.StatusOK, feed)
}

// handleAllBooks serves the acquisition feed with all books.
func (s *Server) handleAllBooks(w http.ResponseWriter, r *http.Request) {
	offset, limit := parsePagination(r)

	books, total, err := s.catalog.AllBooks(offset, limit)
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := opds.NewAcquisitionFeed(
		"urn:nxt-opds:all-books",
		fmt.Sprintf("All Books (%d)", total),
	)
	feed.AddLink(opds.RelSelf, "/opds/books", opds.MIMEAcquisitionFeed)
	feed.AddLink(opds.RelStart, "/opds", opds.MIMENavigationFeed)

	for _, bk := range books {
		feed.AddEntry(bookToEntry(bk))
	}

	writeOPDS(w, http.StatusOK, feed)
}

// handleBook serves a single book entry.
func (s *Server) handleBook(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	bk, err := s.catalog.BookByID(id)
	if err != nil {
		http.Error(w, "book not found", http.StatusNotFound)
		return
	}

	feed := opds.NewAcquisitionFeed(
		"urn:nxt-opds:book:"+id,
		bk.Title,
	)
	feed.AddLink(opds.RelSelf, "/opds/books/"+id, opds.MIMEAcquisitionFeed)
	feed.AddLink(opds.RelStart, "/opds", opds.MIMENavigationFeed)
	feed.AddEntry(bookToEntry(*bk))

	writeOPDS(w, http.StatusOK, feed)
}

// handleSearch performs a catalog search.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "missing search query parameter 'q'", http.StatusBadRequest)
		return
	}

	offset, limit := parsePagination(r)

	books, total, err := s.catalog.Search(catalog.SearchQuery{
		Query:  q,
		Offset: offset,
		Limit:  limit,
	})
	if err != nil {
		http.Error(w, "search error", http.StatusInternalServerError)
		return
	}

	feed := opds.NewAcquisitionFeed(
		"urn:nxt-opds:search",
		fmt.Sprintf("Search: %s (%d results)", q, total),
	)
	feed.AddLink(opds.RelSelf, r.URL.RequestURI(), opds.MIMEAcquisitionFeed)
	feed.AddLink(opds.RelStart, "/opds", opds.MIMENavigationFeed)

	for _, bk := range books {
		feed.AddEntry(bookToEntry(bk))
	}

	writeOPDS(w, http.StatusOK, feed)
}

// handleAuthors serves the author navigation feed.
func (s *Server) handleAuthors(w http.ResponseWriter, r *http.Request) {
	offset, limit := parsePagination(r)

	authors, total, err := s.catalog.Authors(offset, limit)
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := opds.NewNavigationFeed(
		"urn:nxt-opds:authors",
		fmt.Sprintf("Authors (%d)", total),
	)
	feed.AddLink(opds.RelSelf, "/opds/authors", opds.MIMENavigationFeed)
	feed.AddLink(opds.RelStart, "/opds", opds.MIMENavigationFeed)

	now := time.Now()
	for _, name := range authors {
		feed.AddEntry(opds.Entry{
			ID:      "urn:nxt-opds:author:" + name,
			Title:   opds.Text{Value: name},
			Updated: opds.AtomDate{Time: now},
			Links: []opds.Link{
				{
					Rel:  opds.RelCatalogNavigation,
					Href: "/opds/authors/" + url.PathEscape(name),
					Type: opds.MIMEAcquisitionFeed,
				},
			},
		})
	}

	writeOPDS(w, http.StatusOK, feed)
}

// handleAuthorBooks serves books filtered by a specific author.
func (s *Server) handleAuthorBooks(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	author, _ := url.PathUnescape(vars["author"])
	offset, limit := parsePagination(r)

	books, total, err := s.catalog.BooksByAuthor(author, offset, limit)
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := opds.NewAcquisitionFeed(
		"urn:nxt-opds:author:"+author,
		fmt.Sprintf("Books by %s (%d)", author, total),
	)
	feed.AddLink(opds.RelSelf, r.URL.RequestURI(), opds.MIMEAcquisitionFeed)
	feed.AddLink(opds.RelStart, "/opds", opds.MIMENavigationFeed)

	for _, bk := range books {
		feed.AddEntry(bookToEntry(bk))
	}

	writeOPDS(w, http.StatusOK, feed)
}

// handleTags serves the tag/genre navigation feed.
func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	offset, limit := parsePagination(r)

	tags, total, err := s.catalog.Tags(offset, limit)
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := opds.NewNavigationFeed(
		"urn:nxt-opds:tags",
		fmt.Sprintf("Genres (%d)", total),
	)
	feed.AddLink(opds.RelSelf, "/opds/tags", opds.MIMENavigationFeed)
	feed.AddLink(opds.RelStart, "/opds", opds.MIMENavigationFeed)

	now := time.Now()
	for _, tag := range tags {
		feed.AddEntry(opds.Entry{
			ID:      "urn:nxt-opds:tag:" + tag,
			Title:   opds.Text{Value: tag},
			Updated: opds.AtomDate{Time: now},
			Links: []opds.Link{
				{
					Rel:  opds.RelCatalogNavigation,
					Href: "/opds/tags/" + url.PathEscape(tag),
					Type: opds.MIMEAcquisitionFeed,
				},
			},
		})
	}

	writeOPDS(w, http.StatusOK, feed)
}

// handleTagBooks serves books filtered by a specific tag/genre.
func (s *Server) handleTagBooks(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	tag, _ := url.PathUnescape(vars["tag"])
	offset, limit := parsePagination(r)

	books, total, err := s.catalog.BooksByTag(tag, offset, limit)
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	feed := opds.NewAcquisitionFeed(
		"urn:nxt-opds:tag:"+tag,
		fmt.Sprintf("Genre: %s (%d)", tag, total),
	)
	feed.AddLink(opds.RelSelf, r.URL.RequestURI(), opds.MIMEAcquisitionFeed)
	feed.AddLink(opds.RelStart, "/opds", opds.MIMENavigationFeed)

	for _, bk := range books {
		feed.AddEntry(bookToEntry(bk))
	}

	writeOPDS(w, http.StatusOK, feed)
}

// handleOpenSearch serves the OpenSearch description document.
func (s *Server) handleOpenSearch(w http.ResponseWriter, r *http.Request) {
	type OpenSearchDescription struct {
		XMLName     xml.Name `xml:"OpenSearchDescription"`
		Xmlns       string   `xml:"xmlns,attr"`
		ShortName   string   `xml:"ShortName"`
		Description string   `xml:"Description"`
		URL         struct {
			Type     string `xml:"type,attr"`
			Template string `xml:"template,attr"`
		} `xml:"Url"`
	}

	desc := OpenSearchDescription{
		Xmlns:       "http://a9.com/-/spec/opensearch/1.1/",
		ShortName:   "nxt-opds",
		Description: "Search the nxt-opds catalog",
	}
	desc.URL.Type = opds.MIMEAcquisitionFeed
	desc.URL.Template = "/opds/search?q={searchTerms}"

	data, err := xml.MarshalIndent(desc, "", "  ")
	if err != nil {
		http.Error(w, "opensearch serialization error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", opds.MIMEOpenSearchDesc+"; charset=utf-8")
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(data)
}

// handleHealth serves a simple health-check endpoint.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// bookJSON is the JSON representation of a book for the frontend API.
type bookJSON struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Authors      []string `json:"authors"`
	CoverURL     string   `json:"coverUrl,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	Language     string   `json:"language,omitempty"`
	Publisher    string   `json:"publisher,omitempty"`
	Summary      string   `json:"summary,omitempty"`
	DownloadURL  string   `json:"downloadUrl"`
}

// handleAPIBooks serves the full book list as JSON for the web frontend.
// Supports optional ?q= search query and standard ?offset=&limit= pagination.
func (s *Server) handleAPIBooks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	offset, limit := parsePagination(r)

	var (
		books []catalog.Book
		total int
		err   error
	)

	if q != "" {
		books, total, err = s.catalog.Search(catalog.SearchQuery{
			Query:  q,
			Offset: offset,
			Limit:  limit,
		})
	} else {
		books, total, err = s.catalog.AllBooks(offset, limit)
	}
	if err != nil {
		http.Error(w, "catalog error", http.StatusInternalServerError)
		return
	}

	result := make([]bookJSON, 0, len(books))
	for _, bk := range books {
		j := bookJSON{
			ID:          bk.ID,
			Title:       bk.Title,
			CoverURL:    bk.CoverURL,
			Tags:        bk.Tags,
			Language:    bk.Language,
			Publisher:   bk.Publisher,
			Summary:     bk.Summary,
			DownloadURL: "/opds/books/" + bk.ID + "/download",
		}
		for _, a := range bk.Authors {
			j.Authors = append(j.Authors, a.Name)
		}
		result = append(result, j)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"books": result,
		"total": total,
	})
}

// handleCover serves the cached cover image for a book by its ID.
// Returns 501 if the backend does not support cover serving.
// Returns 404 if no cover image exists for the given ID.
func (s *Server) handleCover(w http.ResponseWriter, r *http.Request) {
	if s.coverProvider == nil {
		http.Error(w, "cover serving not supported by this backend", http.StatusNotImplemented)
		return
	}

	vars := mux.Vars(r)
	id := vars["id"]

	coverPath, err := s.coverProvider.CoverPath(id)
	if err != nil {
		http.Error(w, "cover not found", http.StatusNotFound)
		return
	}

	f, err := os.Open(coverPath)
	if err != nil {
		http.Error(w, "cover unavailable", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	contentType := mime.TypeByExtension(filepath.Ext(coverPath))
	if contentType == "" {
		contentType = "image/jpeg"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")

	http.ServeContent(w, r, filepath.Base(coverPath), time.Time{}, f)
}

// maxUploadSize is the maximum file size accepted for upload (100 MiB).
const maxUploadSize = 100 << 20

// handleUpload accepts a multipart/form-data POST with a single file field named "file".
// It stores the file in the catalog and returns the resulting Book as JSON.
// Returns 501 if the backend does not support upload.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if s.uploader == nil {
		http.Error(w, "upload not supported by this backend", http.StatusNotImplemented)
		return
	}

	// Limit request body to prevent memory exhaustion
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "request too large or malformed: "+err.Error(), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing 'file' field in form: "+err.Error(), http.StatusBadRequest)
		return
	}
	// file is an io.ReadCloser; StoreBook will close it
	book, err := s.uploader.StoreBook(header.Filename, file)
	if err != nil {
		http.Error(w, "upload failed: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(book)
}

// handleDownload serves the raw file for a book's acquisition link.
// Query param "path" is the filesystem path stored in the catalog File entry.
// Only files inside the catalog root are served (path traversal prevention).
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	bk, err := s.catalog.BookByID(id)
	if err != nil {
		http.Error(w, "book not found", http.StatusNotFound)
		return
	}

	reqPath, _ := url.QueryUnescape(r.URL.Query().Get("path"))
	if reqPath == "" {
		// Default to the first file
		if len(bk.Files) == 0 {
			http.Error(w, "no files available for this book", http.StatusNotFound)
			return
		}
		reqPath = bk.Files[0].Path
	}

	// Verify the requested path belongs to one of the book's known files
	var matched *catalog.File
	for i := range bk.Files {
		if bk.Files[i].Path == reqPath {
			matched = &bk.Files[i]
			break
		}
	}
	if matched == nil {
		http.Error(w, "file not found for this book", http.StatusNotFound)
		return
	}

	f, err := os.Open(matched.Path)
	if err != nil {
		http.Error(w, "file unavailable", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	contentType := matched.MIMEType
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(matched.Path))
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+filepath.Base(matched.Path)+`"`)

	http.ServeContent(w, r, filepath.Base(matched.Path), time.Time{}, f)
}

// loginPageHTML is the standalone login form served at GET /login.
// It is self-contained (Tailwind CDN) so it works even when the main
// app SPA cannot be served (not authenticated yet).
const loginPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8"/>
  <meta name="viewport" content="width=device-width,initial-scale=1.0"/>
  <title>Login – nxt-opds</title>
  <script src="https://cdn.tailwindcss.com"></script>
</head>
<body class="min-h-screen bg-gray-100 flex items-center justify-center">
  <div class="bg-white rounded-2xl shadow-lg p-8 w-full max-w-sm">
    <div class="flex flex-col items-center mb-6">
      <svg class="w-10 h-10 text-blue-600 mb-2" fill="none" stroke="currentColor" viewBox="0 0 24 24">
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
          d="M12 6.253v13m0-13C10.832 5.477 9.246 5 7.5 5S4.168 5.477 3 6.253v13C4.168 18.477 5.754 18 7.5 18s3.332.477 4.5 1.253m0-13C13.168 5.477 14.754 5 16.5 5c1.746 0 3.332.477 4.5 1.253v13C19.832 18.477 18.246 18 16.5 18c-1.746 0-3.332.477-4.5 1.253"/>
      </svg>
      <h1 class="text-xl font-bold text-gray-900">nxt-opds Library</h1>
      <p class="text-sm text-gray-500 mt-1">Enter your password to continue</p>
    </div>
    {{if .Error}}
    <div class="mb-4 px-3 py-2 bg-red-50 border border-red-200 rounded-lg text-sm text-red-700">
      {{.Error}}
    </div>
    {{end}}
    <form method="POST" action="/login">
      <input type="hidden" name="redirect" value="{{.Redirect}}"/>
      <div class="mb-4">
        <label class="block text-sm font-medium text-gray-700 mb-1" for="password">Password</label>
        <input
          id="password" name="password" type="password" autocomplete="current-password"
          autofocus required
          class="w-full px-3 py-2 border border-gray-300 rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent text-sm"
          placeholder="••••••••"
        />
      </div>
      <button type="submit"
        class="w-full py-2 px-4 bg-blue-600 hover:bg-blue-700 text-white font-medium rounded-lg text-sm transition-colors">
        Sign in
      </button>
    </form>
  </div>
</body>
</html>`

// handleLoginPage serves the GET /login HTML form.
func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	// If auth is disabled, redirect straight to home.
	if s.opts.Password == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	// If already logged in, redirect to home.
	if c, err := r.Cookie(sessionCookieName); err == nil && s.sessions.valid(c.Value) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	redirect := r.URL.Query().Get("redirect")
	if redirect == "" {
		redirect = "/"
	}
	s.renderLoginPage(w, redirect, "")
}

// handleLoginPost processes the POST /login form submission.
func (s *Server) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	password := r.FormValue("password")
	redirect := r.FormValue("redirect")
	if redirect == "" || redirect[0] != '/' {
		redirect = "/"
	}

	// Constant-time password comparison to prevent timing attacks.
	passwordOK := s.opts.Password == "" ||
		(subtle.ConstantTimeCompare([]byte(password), []byte(s.opts.Password)) == 1)

	if passwordOK {
		token, err := s.sessions.create()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    token,
			Path:     "/",
			MaxAge:   int(sessionDuration.Seconds()),
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, redirect, http.StatusSeeOther)
		return
	}

	// Wrong password – re-render the form with an error.
	s.renderLoginPage(w, redirect, "Incorrect password. Please try again.")
}

// handleLogout clears the session cookie and redirects to /login.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		s.sessions.delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:    sessionCookieName,
		Value:   "",
		Path:    "/",
		MaxAge:  -1,
		Expires: time.Unix(0, 0),
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// renderLoginPage writes the login HTML page with the given error message.
func (s *Server) renderLoginPage(w http.ResponseWriter, redirect, errMsg string) {
	type data struct {
		Error    string
		Redirect string
	}
	tmpl, err := template.New("login").Parse(loginPageHTML)
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if errMsg != "" {
		w.WriteHeader(http.StatusUnauthorized)
	}
	_ = tmpl.Execute(w, data{Error: errMsg, Redirect: redirect})
}
