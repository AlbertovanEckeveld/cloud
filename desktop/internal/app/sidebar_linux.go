package app

import (
	"bufio"
	"bytes"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// sidebarSupported: Linux file managers read GTK bookmarks.
const sidebarSupported = true

// setSidebar adds or removes a synced folder in the GTK bookmarks, which
// Nautilus, GTK 4 apps and the file choosers show in their sidebar. Only
// this folder's line changes; other bookmarks are kept as they are.
func setSidebar(dir, label string, on bool) error {
	base, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	p := filepath.Join(base, "gtk-3.0", "bookmarks")
	old, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	uri := (&url.URL{Scheme: "file", Path: dir}).String()
	var out bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(old))
	for sc.Scan() {
		line := sc.Text()
		if line == uri || strings.HasPrefix(line, uri+" ") {
			continue
		}
		out.WriteString(line + "\n")
	}
	if on {
		label = strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' {
				return ' '
			}
			return r
		}, label)
		out.WriteString(uri + " " + label + "\n")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".cloud-tmp"
	if err := os.WriteFile(tmp, out.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
