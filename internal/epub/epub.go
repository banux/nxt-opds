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
	if info != nil {
		size = info.Size()
	}

	id := PathToID(path)
	book := catalog.Book{
		ID:        id,
		Title:     firstOrFilename(meta.Titles, path),
		Summary:   meta.Description,
		Language:  meta.Language,
		Publisher: meta.Publisher,
		UpdatedAt: time.Now(),
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
	if info != nil {
		size = info.Size()
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
		return ""
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
