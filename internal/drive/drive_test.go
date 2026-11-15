package drive

import (
	"mime"
	"strings"
	"testing"
)

func TestNormalizeName(t *testing.T) {
	valid := map[string]string{
		"rapport.pdf":            "rapport.pdf",
		"  spaties  ":            "spaties",
		"café.txt":              "café.txt", // NFD (macOS) becomes NFC
		".bashrc":                ".bashrc",
		"emoji 🎉":                "emoji 🎉",
		strings.Repeat("a", 255): strings.Repeat("a", 255),
	}
	for in, want := range valid {
		got, err := normalizeName(in)
		if err != nil || got != want {
			t.Errorf("normalizeName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", " ", ".", "..", "a/b", `a\b`, "a\x00b", "line\nbreak", strings.Repeat("a", 256)} {
		if _, err := normalizeName(bad); err == nil {
			t.Errorf("normalizeName(%q) accepted", bad)
		}
	}
}

func TestWithSuffix(t *testing.T) {
	for in, want := range map[[2]any]string{
		{"rapport.pdf", 1}: "rapport (hersteld).pdf",
		{"rapport.pdf", 3}: "rapport (hersteld 3).pdf",
		{"Map", 1}:         "Map (hersteld)",
		{".bashrc", 2}:     ".bashrc (hersteld 2)",
	} {
		if got := withSuffix(in[0].(string), in[1].(int)); got != want {
			t.Errorf("withSuffix(%v) = %q, want %q", in, got, want)
		}
	}
}

// The download header must survive any file name without letting it add
// header parameters or lines.
func TestContentDispositionEscaping(t *testing.T) {
	for _, name := range []string{`a"; filename="evil.html`, "résumé.pdf", "x\r\nSet-Cookie: a=b"} {
		h := mime.FormatMediaType("attachment", map[string]string{"filename": name})
		if h == "" || strings.ContainsAny(h, "\r\n") {
			t.Fatalf("header for %q: %q", name, h)
		}
		disp, params, err := mime.ParseMediaType(h)
		if err != nil || disp != "attachment" || params["filename"] != name || len(params) != 1 {
			t.Errorf("round trip of %q: %q -> %v %v", name, h, params, err)
		}
	}
}

func TestContentType(t *testing.T) {
	if got := contentType("foto.JPG", nil); got != "image/jpeg" {
		t.Errorf("by extension: %q", got)
	}
	if got := contentType("zonder-extensie", []byte("%PDF-1.7")); got != "application/pdf" {
		t.Errorf("by content: %q", got)
	}
}
