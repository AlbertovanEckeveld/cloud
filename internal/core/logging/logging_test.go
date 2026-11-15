package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactsSensitiveKeys(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, slog.LevelInfo, false)
	log.Info("login",
		"user", "alice",
		"password", "hunter2",
		"DB_Password", "hunter2",
		slog.Group("req", "Authorization", "Bearer hunter2", "session_token", "hunter2"),
	)

	out := buf.String()
	if strings.Contains(out, "hunter2") {
		t.Fatalf("log leaks secret: %s", out)
	}
	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if entry["user"] != "alice" {
		t.Errorf("non-sensitive attribute was altered: %v", entry["user"])
	}
}

func TestLevelAndFormat(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, slog.LevelWarn, true)
	log.Info("hidden")
	log.Warn("shown")

	out := buf.String()
	if strings.Contains(out, "hidden") {
		t.Error("info message logged at warn level")
	}
	if !strings.Contains(out, "msg=shown") {
		t.Errorf("expected text format, got %q", out)
	}
}
