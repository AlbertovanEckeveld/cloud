package syncer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// Mass-delete brake: a run that would delete more than this many items, and
// more than this share of everything synchronised, stops and asks first. It
// protects against ransomware, a wrongly mounted folder or a slip of the hand.
const (
	massDeleteMin   = 20
	massDeleteShare = 0.3
)

// MassDeleteError stops a run that would delete a lot at once.
type MassDeleteError struct{ Local, Remote, Total int }

func (e *MassDeleteError) Error() string {
	return fmt.Sprintf("sync paused: it would delete %d local and %d server items out of %d; confirm to continue", e.Local, e.Remote, e.Total)
}

// Engine synchronises roots with local folders.
type Engine struct {
	api   *Client
	state *State
	host  string
	log   *slog.Logger
	now   func() time.Time
}

// NewEngine returns an engine; host names this device in conflict copies.
func NewEngine(api *Client, state *State, host string, log *slog.Logger) *Engine {
	return &Engine{api: api, state: state, host: host, log: log, now: time.Now}
}

// Report says what a run did.
type Report struct {
	Uploaded, Downloaded, FoldersCreated, DeletedLocal, TrashedRemote, Conflicts int
	Failed                                                                       []string
}

type action int

const (
	actNone   action = iota
	actRecord        // both sides agree: remember it
	actForget        // gone on both sides: forget it
	actDownload
	actMkdirLocal
	actUpload
	actMkdirRemote
	actDeleteLocal
	actTrashRemote
	actConflict
)

type step struct {
	key   string
	act   action
	local *localEntry
	rem   *remotePath
	base  *baseEntry
}

// remotePath is a server node with its path inside the root.
type remotePath struct {
	remoteEntry
	rel string
}

// Sync runs one round for a root and a local folder. allowMassDelete
// confirms a run that the mass-delete brake stopped before.
func (e *Engine) Sync(ctx context.Context, rootID, dir string, allowMassDelete bool) (Report, error) {
	var rep Report
	// Never decide on stale server data: without a fresh view, do nothing.
	if err := e.pull(ctx, rootID); err != nil {
		return rep, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return rep, fmt.Errorf("open sync folder: %w", err)
	}
	defer root.Close()

	mirror, err := e.state.remote(ctx, rootID)
	if err != nil {
		return rep, err
	}
	remote := e.remotePaths(mirror)
	base, err := e.state.base(ctx, rootID)
	if err != nil {
		return rep, err
	}
	local, err := scanLocal(ctx, root, base)
	if err != nil {
		return rep, fmt.Errorf("scan: %w", err)
	}

	plan := e.plan(local, remote, base)
	if !allowMassDelete {
		var dl, dr int
		for _, s := range plan {
			switch s.act {
			case actDeleteLocal:
				dl++
			case actTrashRemote:
				dr++
			}
		}
		if n := dl + dr; n > massDeleteMin && float64(n) > massDeleteShare*float64(len(base)) {
			return rep, &MassDeleteError{Local: dl, Remote: dr, Total: len(base)}
		}
	}
	e.execute(ctx, root, rootID, plan, &rep)
	return rep, nil
}

// pull brings the server mirror up to date.
func (e *Engine) pull(ctx context.Context, rootID string) error {
	cursor, err := e.state.cursor(ctx, rootID)
	if err != nil {
		return err
	}
	for {
		var p Page
		full := cursor == ""
		if full {
			p, err = e.api.Tree(ctx, rootID)
		} else {
			p, err = e.api.Changes(ctx, rootID, cursor)
			if errors.Is(err, ErrCursorExpired) {
				cursor = ""
				continue
			}
		}
		if err != nil {
			return fmt.Errorf("read server changes: %w", err)
		}
		if err := e.state.applyRemote(ctx, rootID, p, full); err != nil {
			return fmt.Errorf("store server changes: %w", err)
		}
		cursor = p.Cursor
		if full || !p.More {
			return nil
		}
	}
}

// remotePaths turns the mirror into paths. Anything with an unsafe name,
// an unknown parent or a cycle is left out (and logged): a server must
// never be able to make the client write outside the folder.
func (e *Engine) remotePaths(mirror map[string]remoteEntry) map[string]*remotePath {
	out := map[string]*remotePath{}
	var resolve func(id string, depth int) (string, bool)
	memo := map[string]string{}
	resolve = func(id string, depth int) (string, bool) {
		if p, ok := memo[id]; ok {
			return p, p != ""
		}
		n, ok := mirror[id]
		if !ok || depth > 256 || !validName(n.name) || (n.kind != "file" && n.kind != "folder") {
			memo[id] = ""
			return "", false
		}
		rel := key(n.name)
		if n.parent != nil {
			parent, ok := resolve(*n.parent, depth+1)
			if !ok || mirror[*n.parent].kind != "folder" {
				memo[id] = ""
				return "", false
			}
			rel = parent + "/" + rel
		}
		memo[id] = rel
		return rel, true
	}
	for id, n := range mirror {
		rel, ok := resolve(id, 0)
		if !ok {
			e.log.Warn("ignoring server item with an unsafe or unknown path", "id", id)
			continue
		}
		if ignored(n.name) {
			continue
		}
		out[rel] = &remotePath{remoteEntry: n, rel: rel}
	}
	return out
}

func (e *Engine) plan(local map[string]localEntry, remote map[string]*remotePath, base map[string]baseEntry) []step {
	keys := map[string]bool{}
	for k := range local {
		keys[k] = true
	}
	for k := range remote {
		keys[k] = true
	}
	for k := range base {
		keys[k] = true
	}
	steps := map[string]*step{}
	for k := range keys {
		s := &step{key: k}
		if l, ok := local[k]; ok {
			s.local = &l
		}
		s.rem = remote[k]
		if b, ok := base[k]; ok {
			s.base = &b
		}
		s.act = decide(s.local, s.rem, s.base)
		steps[k] = s
	}
	// Keep the parents of whatever must survive: a folder that would be
	// deleted on one side is re-created there if something inside it stays.
	for k, s := range steps {
		for p := path.Dir(k); p != "."; p = path.Dir(p) {
			ps, ok := steps[p]
			if !ok {
				continue
			}
			switch {
			case (s.act == actUpload || s.act == actMkdirRemote || s.act == actConflict) && ps.act == actDeleteLocal:
				ps.act = actMkdirRemote
			case (s.act == actDownload || s.act == actMkdirLocal || s.act == actConflict) && ps.act == actTrashRemote:
				ps.act = actMkdirLocal
			}
		}
	}
	out := make([]step, 0, len(steps))
	for _, s := range steps {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// decide compares disk, server and base for one path.
func decide(l *localEntry, r *remotePath, b *baseEntry) action {
	same := func() bool { // disk and server hold the same thing
		return l.kind == r.kind && (l.kind == "folder" || l.sha == r.sha)
	}
	if b == nil {
		switch {
		case l == nil && r == nil:
			return actNone
		case r == nil:
			return create(l.kind, actUpload, actMkdirRemote)
		case l == nil:
			return create(r.kind, actDownload, actMkdirLocal)
		case same():
			return actRecord
		}
		return actConflict
	}
	lChanged := l == nil || l.kind != b.kind || (l.kind == "file" && l.sha != b.sha)
	rChanged := r == nil || r.kind != b.kind || r.id != b.id || (r.kind == "file" && r.sha != b.sha)
	switch {
	case !lChanged && !rChanged:
		return actNone
	case lChanged && !rChanged:
		if l == nil {
			return actTrashRemote
		}
		return create(l.kind, actUpload, actMkdirRemote)
	case rChanged && !lChanged:
		if r == nil {
			return actDeleteLocal
		}
		return create(r.kind, actDownload, actMkdirLocal)
	}
	// Changed on both sides. Nothing is ever lost:
	switch {
	case l == nil && r == nil:
		return actForget
	case l == nil: // deleted here, changed there: the server's version comes back
		return create(r.kind, actDownload, actMkdirLocal)
	case r == nil: // deleted there, changed here: upload it again
		return create(l.kind, actUpload, actMkdirRemote)
	case same():
		return actRecord
	}
	return actConflict
}

func create(kind string, file, folder action) action {
	if kind == "folder" {
		return folder
	}
	return file
}

// execute runs the plan: creations parents first, deletions children first.
func (e *Engine) execute(ctx context.Context, root *os.Root, rootID string, plan []step, rep *Report) {
	folders := map[string]string{} // path -> server ID of folders, also those made in this run
	parentID := func(k string) (string, bool) {
		dir := path.Dir(k)
		if dir == "." {
			if rootID == "mine" {
				return "root", true
			}
			return rootID, true
		}
		if id, ok := folders[dir]; ok {
			return id, true
		}
		return "", false
	}
	fail := func(s step, err error) {
		e.log.Warn("sync step failed", "path", s.key, "error", err)
		rep.Failed = append(rep.Failed, s.key)
	}
	// Server folders known before this run are parents too.
	for _, s := range plan {
		if s.rem != nil && s.rem.kind == "folder" {
			folders[s.key] = s.rem.id
		}
	}

	for _, s := range plan {
		if err := ctx.Err(); err != nil {
			return
		}
		var err error
		switch s.act {
		case actRecord:
			err = e.record(ctx, rootID, s)
		case actForget:
			err = e.state.dropBase(ctx, rootID, s.key)
		case actMkdirLocal:
			if err = root.Mkdir(s.rem.rel, 0o755); errors.Is(err, fs.ErrExist) {
				err = nil
			}
			if err == nil {
				err = e.state.setBase(ctx, rootID, s.key, baseEntry{id: s.rem.id, kind: "folder"})
				rep.FoldersCreated++
			}
		case actDownload:
			if err = e.download(ctx, root, rootID, s); err == nil {
				rep.Downloaded++
			}
		case actMkdirRemote:
			parent, ok := parentID(s.key)
			if !ok {
				err = fmt.Errorf("parent folder of %s is not on the server", s.key)
				break
			}
			var id string
			if id, err = e.api.Mkdir(ctx, parent, path.Base(s.key)); err == nil {
				folders[s.key] = id
				_ = e.state.upsertRemote(ctx, rootID, remoteEntry{id: id, kind: "folder", name: path.Base(s.key), parent: parentRef(parent, rootID)})
				err = e.state.setBase(ctx, rootID, s.key, baseEntry{id: id, kind: "folder"})
				rep.FoldersCreated++
			}
		case actUpload:
			parent, ok := parentID(s.key)
			if !ok {
				err = fmt.Errorf("parent folder of %s is not on the server", s.key)
				break
			}
			if err = e.upload(ctx, root, rootID, parent, s.key, s.local.rel); err == nil {
				rep.Uploaded++
			}
		case actConflict:
			if err = e.conflict(ctx, root, rootID, s); err == nil {
				rep.Conflicts++
			}
		}
		if err != nil {
			fail(s, err)
		}
	}

	// Deletions, deepest first, after everything that might still need a parent.
	trashed := map[string]bool{}
	for i := len(plan) - 1; i >= 0; i-- {
		s := plan[i]
		var err error
		switch s.act {
		case actDeleteLocal:
			if err = e.deleteLocal(root, s); err == nil {
				err = e.state.dropBase(ctx, rootID, s.key)
				rep.DeletedLocal++
			}
		case actTrashRemote:
			trashed[s.key] = true
		}
		if err != nil {
			fail(s, err)
		}
	}
	// Trash only the highest of what was removed: the rest goes with it.
	for _, s := range plan {
		if s.act != actTrashRemote || ancestorIn(s.key, trashed) {
			if s.act == actTrashRemote {
				_ = e.state.dropBase(ctx, rootID, s.key)
			}
			continue
		}
		err := e.api.Trash(ctx, s.base.id)
		if err == nil {
			err = e.state.dropBase(ctx, rootID, s.key)
			rep.TrashedRemote++
		}
		if err != nil {
			fail(s, err)
		}
	}
}

func ancestorIn(k string, set map[string]bool) bool {
	for p := path.Dir(k); p != "."; p = path.Dir(p) {
		if set[p] {
			return true
		}
	}
	return false
}

func parentRef(parent, rootID string) *string {
	if parent == "root" || parent == rootID {
		return nil
	}
	return &parent
}

func (e *Engine) record(ctx context.Context, rootID string, s step) error {
	b := baseEntry{id: s.rem.id, kind: s.rem.kind, sha: s.rem.sha}
	if s.local.kind == "file" {
		b.size, b.mtime = s.local.size, s.local.mtime
	}
	return e.state.setBase(ctx, rootID, s.key, b)
}

// download fetches a file into a temporary file next to its place, checks
// its SHA-256 against the server's, and only then moves it into place. A
// local file that changed since the scan is left alone (the next run sees
// a conflict).
func (e *Engine) download(ctx context.Context, root *os.Root, rootID string, s step) error {
	if !unchangedSinceScan(root, s.rem.rel, s.local) {
		return errors.New("changed on disk during sync; retrying next run")
	}
	tmp := path.Join(path.Dir(s.rem.rel), tempPrefix+randomSuffix())
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|noFollow, 0o644)
	if err != nil {
		return err
	}
	h := sha256.New()
	err = e.api.Download(ctx, s.rem.id, io.MultiWriter(f, h), s.rem.size)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != s.rem.sha {
		err = errors.New("downloaded content does not match its checksum")
	}
	if err != nil {
		_ = root.Remove(tmp)
		return err
	}
	if err := root.Rename(tmp, s.rem.rel); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	info, err := root.Lstat(s.rem.rel)
	if err != nil {
		return err
	}
	return e.state.setBase(ctx, rootID, s.key, baseEntry{id: s.rem.id, kind: "file", sha: s.rem.sha, size: info.Size(), mtime: info.ModTime().UnixNano()})
}

// unchangedSinceScan reports whether rel still is what the scan saw (or
// still is absent when the scan saw nothing).
func unchangedSinceScan(root *os.Root, rel string, seen *localEntry) bool {
	info, err := root.Lstat(rel)
	if seen == nil {
		return errors.Is(err, fs.ErrNotExist)
	}
	return err == nil && info.Mode().IsRegular() && info.Size() == seen.size && info.ModTime().UnixNano() == seen.mtime
}

// upload streams a local file to the server, hashing what is actually sent.
func (e *Engine) upload(ctx context.Context, root *os.Root, rootID, parent, k, rel string) error {
	f, err := root.OpenFile(rel, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	h := sha256.New()
	id, err := e.api.Upload(ctx, parent, path.Base(k), info.Size(), io.TeeReader(io.LimitReader(f, info.Size()), h))
	if err != nil {
		return err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	_ = e.state.upsertRemote(ctx, rootID, remoteEntry{id: id, kind: "file", name: path.Base(k), parent: parentRef(parent, rootID), size: info.Size(), sha: sum})
	return e.state.setBase(ctx, rootID, k, baseEntry{id: id, kind: "file", sha: sum, size: info.Size(), mtime: info.ModTime().UnixNano()})
}

// conflict keeps both versions: the local one is renamed to a conflict copy
// (uploaded in the next run as a new item), the server's takes its place.
func (e *Engine) conflict(ctx context.Context, root *os.Root, rootID string, s step) error {
	copyName := path.Join(path.Dir(s.local.rel), conflictName(path.Base(s.local.rel), e.host, e.now()))
	if err := root.Rename(s.local.rel, copyName); err != nil {
		return err
	}
	_ = e.state.dropBase(ctx, rootID, s.key)
	if s.rem.kind == "folder" {
		if err := root.Mkdir(s.rem.rel, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		return e.state.setBase(ctx, rootID, s.key, baseEntry{id: s.rem.id, kind: "folder"})
	}
	s.local = nil
	return e.download(ctx, root, rootID, s)
}

// deleteLocal removes what the server removed, but only if it is still what
// was last synced: a file changed in the meantime is kept (and uploaded in
// the next run). Folders are only removed when empty.
func (e *Engine) deleteLocal(root *os.Root, s step) error {
	if s.local.kind == "folder" {
		// Operating system clutter (.DS_Store and the like) goes with it.
		if f, err := root.Open(s.local.rel); err == nil {
			entries, _ := f.ReadDir(-1)
			f.Close()
			for _, de := range entries {
				if ignored(de.Name()) && !strings.HasPrefix(de.Name(), tempPrefix) && de.Type().IsRegular() {
					_ = root.Remove(path.Join(s.local.rel, de.Name()))
				}
			}
		}
		err := root.Remove(s.local.rel)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("folder not empty, kept: %w", err)
		}
		return nil
	}
	if !unchangedSinceScan(root, s.local.rel, s.local) {
		return errors.New("changed on disk during sync; kept")
	}
	return root.Remove(s.local.rel)
}

func randomSuffix() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return strings.ToLower(hex.EncodeToString(b))
}
