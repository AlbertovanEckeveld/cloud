// Package logging builds the platform's structured logger.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// sensitiveKeys are attribute-key fragments whose values are always redacted.
// This is defence in depth: the primary protection is the config.Secret type,
// which redacts itself. This catches plain strings logged under a telling key.
var sensitiveKeys = []string{"password", "passwd", "secret", "token", "authorization", "cookie", "apikey", "api_key", "private_key"}

// New returns a logger that writes JSON (for log aggregation) or, when
// human is true, readable text for local development.
func New(w io.Writer, level slog.Level, human bool) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level, ReplaceAttr: redact}
	if human {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

func redact(_ []string, a slog.Attr) slog.Attr {
	key := strings.ToLower(a.Key)
	for _, s := range sensitiveKeys {
		if strings.Contains(key, s) {
			return slog.String(a.Key, "[REDACTED]")
		}
	}
	return a
}
