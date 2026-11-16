package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckFolder(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "a")
	_ = os.MkdirAll(filepath.Join(a, "sub"), 0o755)
	b := filepath.Join(base, "b")
	_ = os.Mkdir(b, 0o755)
	link := filepath.Join(base, "link")
	_ = os.Symlink(b, link)
	home, _ := os.UserHomeDir()
	others := []Folder{{Root: "mine", Dir: a}}

	if got, err := checkFolder(b+"/", others); err != nil || got != b {
		t.Errorf("valid folder: %q %v", got, err)
	}
	for name, dir := range map[string]string{
		"relative": "documenten", "missing": filepath.Join(base, "nope"), "symlink": link,
		"inside another": filepath.Join(a, "sub"), "contains another": base, "home": home, "root": "/",
	} {
		if _, err := checkFolder(dir, others); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
