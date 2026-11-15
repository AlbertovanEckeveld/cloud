package httpserver

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
)

// These patterns only run on our own build output at startup, never on user input.
var (
	// inlineScript matches <script> elements; ones with a src attribute are skipped.
	inlineScript = regexp.MustCompile(`(?is)<script(\s[^>]*)?>(.*?)</script>`)
	// styleAttr matches style="..." attributes inside HTML templates in compiled
	// JavaScript, such as SvelteKit's visually hidden route announcer.
	styleAttr = regexp.MustCompile(`style="([^"<>]*)"`)
)

// SPA serves the compiled single-page frontend from fsys.
//
// Existing files are served as-is; any other path gets index.html, so the
// client-side router can handle it. index.html is served with a strict CSP
// that allows its inline bootstrap script, and the style attributes the
// framework renders, by hash instead of 'unsafe-inline'.
type SPA struct {
	fsys  fs.FS
	index []byte
	csp   string
}

// NewSPA reads index.html from fsys and derives its Content-Security-Policy.
func NewSPA(fsys fs.FS) (*SPA, error) {
	index, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		return nil, fmt.Errorf("read frontend index.html (was the frontend built?): %w", err)
	}
	styles, err := styleAttributes(fsys)
	if err != nil {
		return nil, err
	}
	return &SPA{fsys: fsys, index: index, csp: pageCSP(index, styles)}, nil
}

// styleAttributes collects every distinct style attribute value found in the
// compiled JavaScript files.
func styleAttributes(fsys fs.FS) ([][]byte, error) {
	seen := map[string]bool{}
	var styles [][]byte
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(name) != ".js" {
			return err
		}
		js, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		for _, m := range styleAttr.FindAllSubmatch(js, -1) {
			if !seen[string(m[1])] {
				seen[string(m[1])] = true
				styles = append(styles, m[1])
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan frontend for style attributes: %w", err)
	}
	return styles, nil
}

// hashSource returns a CSP hash source: base64 of the SHA-256 over the exact
// text, as defined for hash-source in CSP Level 2.
func hashSource(b []byte) string {
	sum := sha256.Sum256(b)
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// pageCSP builds the policy for index.html. Inline scripts are allowed by hash.
// Style attributes need the CSP Level 3 'unsafe-hashes' keyword, which despite
// its name only allows attribute values whose hash is listed, nothing else.
func pageCSP(html []byte, styleAttrs [][]byte) string {
	scriptSrc := []string{"'self'"}
	for _, m := range inlineScript.FindAllSubmatch(html, -1) {
		if attrs := strings.ToLower(string(m[1])); strings.Contains(attrs, "src=") {
			continue
		}
		scriptSrc = append(scriptSrc, hashSource(m[2]))
	}
	styleSrc := []string{"'self'"}
	if len(styleAttrs) > 0 {
		styleSrc = append(styleSrc, "'unsafe-hashes'")
		for _, s := range styleAttrs {
			styleSrc = append(styleSrc, hashSource(s))
		}
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src " + strings.Join(scriptSrc, " "),
		"style-src " + strings.Join(styleSrc, " "),
		"img-src 'self' data:",
		"font-src 'self'",
		"connect-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
}

func (s *SPA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// path.Clean on a rooted path removes every "..", so the name can never
	// escape fsys; fs.FS implementations reject such names as well.
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name != "" && name != "index.html" {
		if info, err := fs.Stat(s.fsys, name); err == nil && !info.IsDir() {
			if strings.HasPrefix(name, "_app/immutable/") {
				// Vite puts a content hash in these file names; they never change.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			http.ServeFileFS(w, r, s.fsys, name)
			return
		}
	}

	h := w.Header()
	h.Set("Content-Security-Policy", s.csp)
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(s.index)
	}
}
