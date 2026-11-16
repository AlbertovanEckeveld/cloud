package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
)

// localEntry is a path on disk as the scan found it.
type localEntry struct {
	rel   string // the path as stored on disk (may be NFD on macOS)
	kind  string // "file" or "folder"
	size  int64
	mtime int64 // Unix nanoseconds
	sha   string
}

// scanLocal walks the sync folder through root, so it never leaves it.
// Symlinks, devices, sockets and ignored names are skipped: they are never
// synchronised. A file's hash is only computed when its size or time differs
// from the base, which keeps rescans of a large folder cheap.
func scanLocal(ctx context.Context, root *os.Root, base map[string]baseEntry) (map[string]localEntry, error) {
	out := map[string]localEntry{}
	var walk func(dir string) error
	walk = func(dir string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := root.Open(dirOrDot(dir))
		if err != nil {
			return err
		}
		entries, err := f.ReadDir(-1)
		f.Close()
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, de := range entries {
			name := de.Name()
			if ignored(name) || !validName(name) {
				continue
			}
			rel := path.Join(dir, name)
			info, err := root.Lstat(rel)
			if err != nil {
				continue // vanished while scanning; the next run sees it
			}
			switch {
			case info.Mode()&fs.ModeSymlink != 0, !info.Mode().IsRegular() && !info.IsDir():
				continue
			case info.IsDir():
				out[key(rel)] = localEntry{rel: rel, kind: "folder"}
				if err := walk(rel); err != nil {
					return err
				}
			default:
				e := localEntry{rel: rel, kind: "file", size: info.Size(), mtime: info.ModTime().UnixNano()}
				if b, ok := base[key(rel)]; ok && b.kind == "file" && b.size == e.size && b.mtime == e.mtime {
					e.sha = b.sha
				} else if e.sha, err = hashFile(root, rel); err != nil {
					continue
				}
				out[key(rel)] = e
			}
		}
		return nil
	}
	return out, walk("")
}

func dirOrDot(dir string) string {
	if dir == "" {
		return "."
	}
	return dir
}

func hashFile(root *os.Root, rel string) (string, error) {
	f, err := root.OpenFile(rel, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
