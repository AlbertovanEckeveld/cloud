package drive

import "testing"

func TestPreviewType(t *testing.T) {
	for in, want := range map[string]string{
		"image/png":                "image/png",
		"image/svg+xml":            "",
		"text/html; charset=utf-8": "text/plain; charset=utf-8",
		"text/markdown":            "text/plain; charset=utf-8",
		"application/json":         "text/plain; charset=utf-8",
		"video/mp4":                "video/mp4",
		"application/pdf":          "application/pdf",
		"application/octet-stream": "",
		"application/x-msdownload": "",
		"not a type":               "",
	} {
		if got := previewType(in); got != want {
			t.Errorf("previewType(%q) = %q, want %q", in, got, want)
		}
	}
}
