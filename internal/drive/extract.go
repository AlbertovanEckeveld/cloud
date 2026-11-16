package drive

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
)

// Text extraction for content search (ADR 0017). Files come from users and
// are untrusted, so everything is bounded: the file size, how much a zip
// may unpack to (zip bombs), the text kept and the time per file. PDFs are
// read by PDFium inside a WebAssembly sandbox with no file system access
// and limited memory.
const (
	maxIndexFile = 50 << 20  // larger files are not indexed
	maxUnpacked  = 200 << 20 // total bytes read from a zip
	maxText      = 1 << 20   // text kept per file
	extractTime  = time.Minute
	pdfiumMemory = 4096 // WebAssembly pages of 64 KiB: 256 MiB
	maxPDFPages  = 2000
)

// errNotIndexable means the file type has no text to search.
var errNotIndexable = errors.New("not indexable")

// textFiles are extensions read as plain text.
var textFiles = map[string]bool{
	"txt": true, "md": true, "markdown": true, "csv": true, "tsv": true, "json": true, "xml": true,
	"yaml": true, "yml": true, "toml": true, "ini": true, "log": true, "sql": true, "html": true, "htm": true,
	"go": true, "py": true, "js": true, "ts": true, "java": true, "c": true, "h": true, "cpp": true,
	"rs": true, "sh": true, "css": true, "svelte": true, "php": true, "rb": true, "tex": true,
}

// officeParts are the XML parts holding a document's text.
var officeParts = map[string]func(name string) bool{
	"docx": func(n string) bool { return n == "word/document.xml" },
	"xlsx": func(n string) bool { return n == "xl/sharedStrings.xml" },
	"pptx": func(n string) bool { return strings.HasPrefix(n, "ppt/slides/slide") && strings.HasSuffix(n, ".xml") },
	"odt":  func(n string) bool { return n == "content.xml" },
	"ods":  func(n string) bool { return n == "content.xml" },
	"odp":  func(n string) bool { return n == "content.xml" },
}

// extractText returns the searchable text of a file, or errNotIndexable.
func (s *Service) extractText(ctx context.Context, name string, data []byte) (string, error) {
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".")
	var text string
	var err error
	switch {
	case textFiles[ext]:
		text = string(data)
	case officeParts[ext] != nil:
		text, err = officeText(data, officeParts[ext])
	case ext == "pdf":
		text, err = s.pdfText(ctx, data)
	default:
		return "", errNotIndexable
	}
	if err != nil {
		return "", err
	}
	return clean(text), nil
}

// clean keeps valid UTF-8 without control characters (Postgres text cannot
// hold NUL, and \x02/\x03 mark search highlights), within maxText.
func clean(s string) string {
	s = strings.ToValidUTF8(s, " ")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if len(s) > maxText {
		s = s[:maxText]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return strings.TrimSpace(s)
}

// officeText reads the text of OOXML (docx/xlsx/pptx) and OpenDocument
// files: zip archives of XML. Unpacking is bounded against zip bombs.
func officeText(data []byte, want func(string) bool) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open document: %w", err)
	}
	var parts []*zip.File
	for _, f := range zr.File {
		if want(f.Name) {
			parts = append(parts, f)
		}
	}
	sort.Slice(parts, func(i, j int) bool { return slideOrder(parts[i].Name) < slideOrder(parts[j].Name) })
	var out strings.Builder
	budget := int64(maxUnpacked)
	for _, f := range parts {
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		lr := &io.LimitedReader{R: rc, N: budget}
		err = xmlText(lr, &out)
		rc.Close()
		budget = lr.N
		if err != nil {
			return "", err
		}
		if budget <= 0 {
			return "", errors.New("document unpacks to too much data")
		}
		if out.Len() > maxText {
			break
		}
	}
	return out.String(), nil
}

// slideOrder sorts ppt/slides/slide10.xml after slide9.xml.
func slideOrder(name string) string {
	digits := strings.TrimFunc(path.Base(name), func(r rune) bool { return !unicode.IsDigit(r) })
	return fmt.Sprintf("%08s", digits)
}

// xmlText collects character data, with a line break after paragraphs and
// rows and a space after table cells. Nothing is added after text runs
// (w:t, text:span): Word splits words over runs for formatting, so a space
// there would break "Jaarplan" into "Jaar plan". encoding/xml does not
// resolve external entities.
func xmlText(r io.Reader, out *strings.Builder) error {
	d := xml.NewDecoder(r)
	d.Strict = false
	for out.Len() <= maxText {
		tok, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read document: %w", err)
		}
		switch t := tok.(type) {
		case xml.CharData:
			out.Write(t)
		case xml.EndElement:
			switch t.Name.Local {
			case "p", "si", "row", "tab", "br", "h", "table-row":
				out.WriteByte('\n')
			case "tc", "c", "table-cell":
				out.WriteByte(' ')
			}
		}
	}
	return nil
}

var (
	pdfiumOnce sync.Once
	pdfiumPool pdfium.Pool
	pdfiumErr  error
)

// pdfText reads a PDF's text with PDFium in a WebAssembly sandbox: no file
// system (the library mounts / by default; here nothing is mounted), a
// memory limit, and cancellation when extraction takes too long.
func (s *Service) pdfText(ctx context.Context, data []byte) (string, error) {
	pdfiumOnce.Do(func() {
		pdfiumPool, pdfiumErr = webassembly.Init(webassembly.Config{
			MinIdle: 0, MaxIdle: 1, MaxTotal: 1,
			FSConfig: wazero.NewFSConfig(),
			RuntimeConfig: wazero.NewRuntimeConfig().
				WithCoreFeatures(api.CoreFeaturesV2 | experimental.CoreFeaturesExceptionHandling).
				WithMemoryLimitPages(pdfiumMemory).
				WithCloseOnContextDone(true),
			Stdout: io.Discard,
			Stderr: io.Discard,
		})
	})
	if pdfiumErr != nil {
		return "", fmt.Errorf("start pdf reader: %w", pdfiumErr)
	}
	ctx, cancel := context.WithTimeout(ctx, extractTime)
	defer cancel()
	inst, err := pdfiumPool.GetInstanceWithContext(ctx)
	if err != nil {
		return "", fmt.Errorf("pdf reader busy: %w", err)
	}
	defer inst.Close() // also cancels a running extraction

	doc, err := inst.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		return "", fmt.Errorf("open pdf: %w", err)
	}
	defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
	count, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return "", fmt.Errorf("read pdf: %w", err)
	}
	var out strings.Builder
	for i := 0; i < min(count.PageCount, maxPDFPages) && out.Len() <= maxText; i++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		page, err := inst.GetPageText(&requests.GetPageText{Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: i}}})
		if err != nil {
			continue // one unreadable page does not spoil the rest
		}
		out.WriteString(page.Text)
		out.WriteByte('\n')
	}
	return out.String(), nil
}
