package mailer

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlbertovanEckeveld/cloud/internal/core/config"
)

func TestDevDirWritesPrivateFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mail")
	cfg := &config.Mail{From: "Cloud <cloud@example.test>", DevDir: dir}
	m, err := New(nil, nil, cfg, "cloud.example.test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Verify(context.Background()); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("directory: %v, mode %v", err, info.Mode().Perm())
	}

	raw := []byte("Subject: test\r\n\r\nactivation link\r\n")
	if err := m.send(context.Background(), "cloud@example.test", "alice@example.test", raw); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".eml") {
		t.Fatalf("want exactly one .eml file and no temporary file, got %v (%v)", entries, err)
	}
	path := filepath.Join(dir, entries[0].Name())
	info, err = os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v, mode %v", err, info.Mode().Perm())
	}
	if got, _ := os.ReadFile(path); string(got) != string(raw) {
		t.Fatalf("content: got %q", got)
	}
}

func TestDevDirMustBeADirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Mail{From: "Cloud <cloud@example.test>", DevDir: file}
	if _, err := New(nil, nil, cfg, "cloud.example.test", slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("New accepted a file as MAIL_DEV_DIR")
	}
}
