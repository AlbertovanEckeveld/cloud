// Package syncer keeps a local folder and a Cloud folder tree in sync, in
// both directions (ADR 0016). Everything from the server is treated as
// untrusted input: names are checked here, and every file system operation
// goes through an os.Root, so nothing can be read or written outside the
// sync folder, not even through symlinks.
package syncer

import (
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	maxNameBytes = 255
	// tempPrefix marks partial downloads; they are never synchronised.
	tempPrefix = ".cloud-tmp-"
)

// validName reports whether a name from the server (or the disk) is a safe,
// single path element: no separators, no "." or "..", no control characters.
func validName(name string) bool {
	switch {
	case name == "" || name == "." || name == "..":
		return false
	case len(name) > maxNameBytes || !utf8.ValidString(name):
		return false
	case strings.ContainsAny(name, `/\`) || strings.ContainsFunc(name, unicode.IsControl):
		return false
	}
	return true
}

// key is how a path is compared: NFC, as the server stores names. macOS
// often stores decomposed (NFD) names; without this, every file with an
// accent would look new on every scan.
func key(p string) string { return norm.NFC.String(p) }

// ignored reports names that are never synchronised: operating system
// clutter, editors' lock files and this program's own temporary files.
func ignored(name string) bool {
	lower := strings.ToLower(name)
	switch {
	case lower == ".ds_store", lower == "thumbs.db", lower == "desktop.ini", lower == ".directory":
		return true
	case strings.HasPrefix(name, tempPrefix), strings.HasPrefix(name, "~$"), strings.HasPrefix(name, ".~lock."):
		return true
	case strings.HasPrefix(name, "._"): // macOS AppleDouble metadata
		return true
	}
	return false
}

// conflictName turns "plan.docx" into "plan (conflict laptop 2026-11-16 1430).docx".
func conflictName(name, host string, at time.Time) string {
	ext := path.Ext(name)
	if ext == name { // ".bashrc" has no extension
		ext = ""
	}
	stem := strings.TrimSuffix(name, ext)
	return stem + " (conflict " + host + " " + at.Format("2006-01-02 1504") + ")" + ext
}
