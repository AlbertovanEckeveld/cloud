package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestCallAndPermissions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := filepath.Join(t.TempDir(), "s.sock")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := func(_ context.Context, method string, args json.RawMessage) (any, error) {
		switch method {
		case "Echo":
			var s string
			_ = json.Unmarshal(args, &s)
			return s, nil
		}
		return nil, errors.New("onbekend")
	}
	if _, err := Listen(ctx, path, h, log); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("socket mode %v", info.Mode().Perm())
	}
	c := NewClient(path)
	var out string
	if err := c.Call(ctx, "Echo", "hallo", &out); err != nil || out != "hallo" {
		t.Fatalf("call: %q %v", out, err)
	}
	if err := c.Call(ctx, "Nope", nil, nil); err == nil || err.Error() != "onbekend" {
		t.Errorf("error not passed on: %v", err)
	}
	// A second service on the same socket refuses to start.
	if _, err := Listen(ctx, path, h, log); !errors.Is(err, ErrRunning) {
		t.Errorf("second listener: %v", err)
	}
}
