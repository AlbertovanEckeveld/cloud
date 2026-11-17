package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSidebarBookmarks(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	p := filepath.Join(cfg, "gtk-3.0", "bookmarks")
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte("file:///home/x/Muziek Muziek\n"), 0o600)

	dir := "/home/x/Cloud/Mijn bestanden"
	if err := setSidebar(dir, "Cloud – Mijn\nbestanden", true); err != nil {
		t.Fatal(err)
	}
	_ = setSidebar(dir, "Cloud – Mijn bestanden", true) // twice: still one line
	b, _ := os.ReadFile(p)
	got := string(b)
	if strings.Count(got, "Cloud/Mijn%20bestanden") != 1 || !strings.Contains(got, "Muziek") || strings.Contains(got, "Mijn\nbestanden") {
		t.Fatalf("bookmarks:\n%s", got)
	}
	if err := setSidebar(dir, "", false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "file:///home/x/Muziek Muziek\n" {
		t.Errorf("after removing:\n%s", b)
	}
}
