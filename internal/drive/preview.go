package drive

import (
	"mime"
	"strings"
)

// Media types the browser may show inline. Everything else is only ever a
// download. SVG is not here: it can contain script. Text of any kind
// (including HTML) is shown as plain text, so it is displayed, never run.
var inlineMedia = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true, "image/avif": true,
	"video/mp4": true, "video/webm": true, "video/ogg": true,
	"audio/mpeg": true, "audio/ogg": true, "audio/wav": true, "audio/x-wav": true, "audio/mp4": true, "audio/webm": true, "audio/flac": true,
	// Read by Cloud's own PDF reader (pdf.js); opened directly, the sandbox
	// CSP keeps the browser's viewer from running anything on this origin.
	"application/pdf": true,
}

var textLike = map[string]bool{
	"application/json": true, "application/xml": true, "application/javascript": true,
	"application/x-yaml": true, "application/yaml": true, "application/toml": true,
}

// previewType returns the Content-Type to show a file inline with, or ""
// when it may only be downloaded. Combined with X-Content-Type-Options:
// nosniff, an HTML page uploaded as "photo.png" is never rendered as HTML.
func previewType(contentType string) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	switch {
	case inlineMedia[mt]:
		return mt
	case strings.HasPrefix(mt, "text/") || textLike[mt]:
		return "text/plain; charset=utf-8"
	}
	return ""
}
