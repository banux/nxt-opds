// Package epub provides EPUB and PDF metadata extraction utilities shared
// across catalog backend implementations.
package epub

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/banux/nxt-opds/internal/catalog"
)

// ParseBook opens an EPUB file, extracts OPF metadata and cover image, and
// returns a populated Book. coversDir is the directory where extracted cover
// images are cached. An error is returned only for fatal parsing failures;
// cover extraction failures are silently ignored.
func ParseBook(path, coversDir string) (catalog.Book, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return catalog.Book{}, fmt.Errorf("open epub %q: %w", path, err)
	}
	defer zr.Close()

	opfPath, err := readContainerXML(&zr.Reader)
	if err != nil {
		return catalog.Book{}, fmt.Errorf("epub container %q: %w", path, err)
	}

	pkg, err := readOPFPackage(&zr.Reader, opfPath)
	if err != nil {
		return catalog.Book{}, fmt.Errorf("epub opf %q: %w", path, err)
	}
	meta := pkg.Metadata

	info, _ := os.Stat(path)
	size := int64(0)
	addedAt := time.Now()
	if info != nil {
		size = info.Size()
		addedAt = info.ModTime()
	}

	id := PathToID(path)
	book := catalog.Book{
		ID:        id,
		Title:     firstOrFilename(meta.Titles, path),
		Summary:   meta.Description,
		Language:  meta.Language,
		Publisher: meta.Publisher,
		UpdatedAt: time.Now(),
		AddedAt:   addedAt,
		Tags:      meta.Subjects,
		Files: []catalog.File{
			{MIMEType: "application/epub+zip", Path: path, Size: size},
		},
	}

	for _, c := range meta.Creators {
		book.Authors = append(book.Authors, catalog.Author{Name: c.Name})
	}

	if meta.Date != "" {
		if t, err := time.Parse("2006-01-02", meta.Date[:min(10, len(meta.Date))]); err == nil {
			book.PublishedAt = t
		}
	}

	if coverPath := extractCoverFromPkg(&zr.Reader, opfPath, pkg, id, coversDir); coverPath != "" {
		book.CoverURL = "/covers/" + id
		book.ThumbnailURL = "/covers/" + id
	}

	return book, nil
}

// ParsePath creates a minimal Book entry for a non-EPUB file (e.g. PDF).
func ParsePath(path string) catalog.Book {
	info, _ := os.Stat(path)
	size := int64(0)
	addedAt := time.Now()
	if info != nil {
		size = info.Size()
		addedAt = info.ModTime()
	}

	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	mime := "application/pdf"
	if strings.ToLower(filepath.Ext(path)) != ".pdf" {
		mime = "application/octet-stream"
	}

	return catalog.Book{
		ID:    PathToID(path),
		Title: name,
		Files: []catalog.File{
			{MIMEType: mime, Path: path, Size: size},
		},
		UpdatedAt: time.Now(),
		AddedAt:   addedAt,
	}
}

// PathToID generates a stable string ID from a file path using a short SHA-256 hash.
func PathToID(path string) string {
	sum := sha256.Sum256([]byte(path))
	return fmt.Sprintf("%x", sum[:8])
}

// CoverPath returns the filesystem path to the cached cover image for a book
// ID, searching for common image extensions. Returns an error if no cover exists.
func CoverPath(coversDir, id string) (string, error) {
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp"} {
		p := filepath.Join(coversDir, id+ext)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no cover for book %q", id)
}

// --- internal XML struct types for OPF/container parsing ---

type opfPackage struct {
	Metadata opfMetadata `xml:"metadata"`
	Manifest opfManifest `xml:"manifest"`
	Spine    opfSpine    `xml:"spine"`
}

type opfSpine struct {
	ItemRefs []opfItemRef `xml:"itemref"`
}

type opfItemRef struct {
	IDRef string `xml:"idref,attr"`
}

type opfMetadata struct {
	Titles      []string    `xml:"title"`
	Creators    []opfAuthor `xml:"creator"`
	Subjects    []string    `xml:"subject"`
	Description string      `xml:"description"`
	Language    string      `xml:"language"`
	Publisher   string      `xml:"publisher"`
	Date        string      `xml:"date"`
	Metas       []opfMeta   `xml:"meta"`
}

type opfAuthor struct {
	Name string `xml:",chardata"`
	Role string `xml:"role,attr"`
}

type opfMeta struct {
	Name    string `xml:"name,attr"`
	Content string `xml:"content,attr"`
}

type opfManifest struct {
	Items []opfItem `xml:"item"`
}

type opfItem struct {
	ID         string `xml:"id,attr"`
	Href       string `xml:"href,attr"`
	MediaType  string `xml:"media-type,attr"`
	Properties string `xml:"properties,attr"`
}

type containerXML struct {
	Rootfile struct {
		FullPath string `xml:"full-path,attr"`
	} `xml:"rootfiles>rootfile"`
}

func readContainerXML(zr *zip.Reader) (string, error) {
	for _, f := range zr.File {
		if f.Name == "META-INF/container.xml" {
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			defer rc.Close()

			var c containerXML
			if err := xml.NewDecoder(rc).Decode(&c); err != nil {
				return "", err
			}
			if c.Rootfile.FullPath == "" {
				return "", fmt.Errorf("no rootfile found in container.xml")
			}
			return c.Rootfile.FullPath, nil
		}
	}
	return "", fmt.Errorf("META-INF/container.xml not found")
}

func readOPFPackage(zr *zip.Reader, opfPath string) (opfPackage, error) {
	for _, f := range zr.File {
		if f.Name == opfPath {
			rc, err := f.Open()
			if err != nil {
				return opfPackage{}, err
			}
			defer rc.Close()

			data, err := io.ReadAll(rc)
			if err != nil {
				return opfPackage{}, err
			}

			var pkg opfPackage
			if err := xml.Unmarshal(data, &pkg); err != nil {
				return opfPackage{}, err
			}
			return pkg, nil
		}
	}
	return opfPackage{}, fmt.Errorf("OPF file %q not found in epub", opfPath)
}

func extractCoverFromPkg(zr *zip.Reader, opfPath string, pkg opfPackage, bookID, coversDir string) string {
	opfDir := filepath.ToSlash(filepath.Dir(opfPath))
	if opfDir == "." {
		opfDir = ""
	}

	coverItemID := ""
	for _, m := range pkg.Metadata.Metas {
		if strings.EqualFold(m.Name, "cover") && m.Content != "" {
			coverItemID = m.Content
			break
		}
	}

	var coverHref, coverMIME string
	for _, item := range pkg.Manifest.Items {
		if strings.Contains(item.Properties, "cover-image") {
			coverHref = item.Href
			coverMIME = item.MediaType
			break
		}
		if coverItemID != "" && item.ID == coverItemID {
			coverHref = item.Href
			coverMIME = item.MediaType
		}
	}

	if coverHref == "" {
		// Fallback: scan the first HTML spine item for the first <img> tag.
		return findCoverInSpine(zr, opfDir, pkg, bookID, coversDir)
	}

	var fullHref string
	if opfDir != "" {
		fullHref = opfDir + "/" + coverHref
	} else {
		fullHref = coverHref
	}

	var coverFile *zip.File
	for _, f := range zr.File {
		if f.Name == fullHref {
			coverFile = f
			break
		}
	}
	if coverFile == nil {
		return ""
	}

	ext := mimeToExt(coverMIME)
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(coverHref))
	}
	if ext == "" {
		ext = ".jpg"
	}

	destPath := filepath.Join(coversDir, bookID+ext)
	if _, err := os.Stat(destPath); err == nil {
		return destPath
	}

	rc, err := coverFile.Open()
	if err != nil {
		return ""
	}
	defer rc.Close()

	out, err := os.Create(destPath)
	if err != nil {
		return ""
	}
	defer out.Close()

	if _, err := io.Copy(out, rc); err != nil {
		_ = os.Remove(destPath)
		return ""
	}
	return destPath
}

// findCoverInSpine walks the OPF spine in order, opens the first HTML/XHTML
// item, and saves the first <img src="…"> it finds as the book cover.
// Returns the saved cover path or "" if nothing is found.
func findCoverInSpine(zr *zip.Reader, opfDir string, pkg opfPackage, bookID, coversDir string) string {
	// Build manifest map: id → item.
	byID := make(map[string]opfItem, len(pkg.Manifest.Items))
	for _, item := range pkg.Manifest.Items {
		byID[item.ID] = item
	}

	for _, ref := range pkg.Spine.ItemRefs {
		item, ok := byID[ref.IDRef]
		if !ok {
			continue
		}
		if !strings.Contains(item.MediaType, "html") {
			continue
		}

		// Resolve full path inside the ZIP.
		var fullPath string
		if opfDir != "" {
			fullPath = opfDir + "/" + item.Href
		} else {
			fullPath = item.Href
		}

		// Open the HTML file.
		var htmlFile *zip.File
		for _, f := range zr.File {
			if f.Name == fullPath {
				htmlFile = f
				break
			}
		}
		if htmlFile == nil {
			continue
		}

		rc, err := htmlFile.Open()
		if err != nil {
			continue
		}
		// Read only the first 64 KB to find the image quickly.
		content, err := io.ReadAll(io.LimitReader(rc, 64*1024))
		rc.Close()
		if err != nil {
			continue
		}

		imgSrc := findFirstImgSrc(string(content))
		if imgSrc == "" {
			continue
		}

		// Resolve the image path relative to the HTML file's directory.
		htmlDir := filepath.ToSlash(filepath.Dir(fullPath))
		if htmlDir == "." {
			htmlDir = ""
		}
		var imgPath string
		if strings.HasPrefix(imgSrc, "/") {
			imgPath = strings.TrimPrefix(imgSrc, "/")
		} else if htmlDir != "" {
			imgPath = htmlDir + "/" + imgSrc
		} else {
			imgPath = imgSrc
		}
		// Clean up any ../ in the path.
		imgPath = filepath.ToSlash(filepath.Clean(imgPath))

		// Find the image file in the ZIP.
		var imgFile *zip.File
		for _, f := range zr.File {
			if f.Name == imgPath {
				imgFile = f
				break
			}
		}
		if imgFile == nil {
			continue
		}

		ext := strings.ToLower(filepath.Ext(imgSrc))
		if ext == "" {
			ext = ".jpg"
		}

		destPath := filepath.Join(coversDir, bookID+ext)
		if _, statErr := os.Stat(destPath); statErr == nil {
			return destPath // already extracted
		}

		imgRC, err := imgFile.Open()
		if err != nil {
			continue
		}
		out, err := os.Create(destPath)
		if err != nil {
			imgRC.Close()
			continue
		}
		_, copyErr := io.Copy(out, imgRC)
		imgRC.Close()
		out.Close()
		if copyErr != nil {
			_ = os.Remove(destPath)
			continue
		}
		return destPath
	}
	return ""
}

// findFirstImgSrc does a simple scan for the first <img … src="…"> in an
// HTML string. Returns the raw src value (not URL-decoded) or "".
func findFirstImgSrc(html string) string {
	lower := strings.ToLower(html)
	idx := strings.Index(lower, "<img")
	if idx == -1 {
		return ""
	}
	tag := html[idx:]
	endIdx := strings.Index(strings.ToLower(tag), ">")
	if endIdx == -1 {
		endIdx = len(tag)
	}
	tag = tag[:endIdx]

	lowerTag := strings.ToLower(tag)
	srcIdx := strings.Index(lowerTag, "src=")
	if srcIdx == -1 {
		return ""
	}
	rest := tag[srcIdx+4:]
	if len(rest) == 0 {
		return ""
	}

	var quote byte
	if rest[0] == '"' || rest[0] == '\'' {
		quote = rest[0]
		rest = rest[1:]
	}

	var endSrc int
	if quote != 0 {
		endSrc = strings.IndexByte(rest, quote)
	} else {
		endSrc = strings.IndexAny(rest, " \t\n\r>")
	}
	if endSrc == -1 {
		endSrc = len(rest)
	}

	src := rest[:endSrc]
	// Strip query string and fragment.
	if i := strings.IndexByte(src, '?'); i != -1 {
		src = src[:i]
	}
	if i := strings.IndexByte(src, '#'); i != -1 {
		src = src[:i]
	}
	return strings.TrimSpace(src)
}

func mimeToExt(mimeType string) string {
	switch strings.ToLower(mimeType) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	default:
		return ""
	}
}

func firstOrFilename(vals []string, path string) string {
	if len(vals) > 0 && vals[0] != "" {
		return vals[0]
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}
