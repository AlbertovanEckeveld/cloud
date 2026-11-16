package drive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/net/webdav"
	"golang.org/x/text/unicode/norm"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
	"github.com/AlbertovanEckeveld/cloud/internal/core/storage"
)

// WebDAV (RFC 4918) for desktop and mobile clients, on three mounts:
//
//	/dav/files/   the user's own files
//	/dav/teams/   the user's team folders
//	/dav/shared/  what others shared with the user
//
// golang.org/x/net/webdav speaks the protocol (ADR 0014); davFS maps it onto
// the drive service, so permissions, quotas, versions and the trash work
// exactly as in the browser. Uploads (PUT) are handled here directly, to
// stream them to S3 and answer with the right status codes.

const (
	mountFiles  = "files"
	mountTeams  = "teams"
	mountShared = "shared"
)

// The router only routes methods it knows; WebDAV adds these (RFC 4918 §9).
func init() {
	for _, m := range []string{"PROPFIND", "PROPPATCH", "MKCOL", "COPY", "MOVE", "LOCK", "UNLOCK"} {
		chi.RegisterMethod(m)
	}
}

// DAVRoutes mounts WebDAV under /dav. auth authenticates with HTTP Basic
// (username + app password); userID returns the authenticated user.
func (s *Service) DAVRoutes(r chi.Router, auth func(http.Handler) http.Handler, userID func(*http.Request) uuid.UUID) {
	r.Use(auth)
	for _, m := range []string{mountFiles, mountTeams, mountShared} {
		h := s.davHandler(m, userID)
		r.Handle("/"+m, h)
		r.Handle("/"+m+"/*", h)
	}
}

// davLocks holds WebDAV locks per user and mount. Locks are advisory and
// short-lived (clients refresh them every few minutes); they are kept in
// memory per instance (ADR 0014).
type davLocks struct {
	mu sync.Mutex
	ls map[string]webdav.LockSystem
}

func (l *davLocks) get(key string) webdav.LockSystem {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ls == nil {
		l.ls = map[string]webdav.LockSystem{}
	}
	if l.ls[key] == nil {
		l.ls[key] = webdav.NewMemLS()
	}
	return l.ls[key]
}

func (s *Service) davHandler(mount string, userID func(*http.Request) uuid.UUID) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		deadline := time.Now().Add(transferDeadline)
		_ = rc.SetReadDeadline(deadline)
		_ = rc.SetWriteDeadline(deadline)
		// A browser opening a file here must never run it on this origin.
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			// Set before the library guesses one from the content.
			w.Header().Set("Content-Type", "application/octet-stream")
		}

		user := userID(r)
		fsys := &davFS{s: s, user: user, mount: mount}
		if r.Method == http.MethodPut {
			s.davPut(w, r, fsys, strings.TrimPrefix(r.URL.Path, "/dav/"+mount))
			return
		}
		h := &webdav.Handler{
			Prefix:     "/dav/" + mount,
			FileSystem: fsys,
			LockSystem: s.locks.get(user.String() + "/" + mount),
			Logger: func(r *http.Request, err error) {
				if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrPermission) && !errors.Is(err, os.ErrExist) {
					s.log.Warn("webdav request failed", "method", r.Method, "error", err)
				}
			},
		}
		h.ServeHTTP(w, r)
	})
}

// davPut stores an upload, streaming it to S3. Clients that send no
// Content-Length (macOS Finder) may announce the size in
// X-Expected-Entity-Length; otherwise the size is found while streaming.
func (s *Service) davPut(w http.ResponseWriter, r *http.Request, fsys *davFS, name string) {
	ctx := r.Context()
	dir, base := path.Split(path.Clean("/" + name))
	parent, err := fsys.resolve(ctx, dir)
	switch {
	case err != nil:
	case parent.virtual && fsys.mount != mountFiles:
		err = ErrForbidden // the list of team folders or shares itself
	case !parent.virtual && parent.node.Kind != "folder":
		err = ErrNotFolder
	}
	if err != nil {
		davStatus(w, err, http.StatusConflict)
		return
	}
	size := r.ContentLength
	if size < 0 {
		size = -1
		if v, perr := strconv.ParseInt(r.Header.Get("X-Expected-Entity-Length"), 10, 64); perr == nil && v >= 0 {
			size = v
		}
	}
	var existing error
	if parent.virtual {
		_, existing = s.childByName(ctx, s.db, fsys.user, nil, norm.NFC.String(base))
	} else {
		_, existing = s.childIn(ctx, parent.node.ID, base)
	}
	existed := existing == nil
	n, err := s.Store(ctx, fsys.user, Upload{ParentID: parent.parentRef(), Name: base, Size: size, Body: r.Body, Replace: true})
	if err != nil {
		davStatus(w, err, http.StatusConflict)
		return
	}
	w.Header().Set("ETag", `"`+n.currentVersionID.String()+`"`)
	if existed {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
}

// davStatus answers a failed request with a fitting status code.
func davStatus(w http.ResponseWriter, err error, notFound int) {
	_, input := errors.AsType[*InputError](err)
	switch {
	case errors.Is(err, ErrNotFound):
		http.Error(w, "not found", notFound)
	case errors.Is(err, ErrForbidden), input:
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, ErrNameTaken), errors.Is(err, ErrNotFolder):
		http.Error(w, "conflict", http.StatusConflict)
	case errors.Is(err, ErrQuotaExceeded):
		http.Error(w, "quota exceeded", http.StatusInsufficientStorage)
	case errors.Is(err, ErrTooLarge):
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
	case errors.Is(err, ErrIncomplete):
		http.Error(w, "incomplete upload", http.StatusBadRequest)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// davFS is the drive as a webdav.FileSystem, for one user and mount.
type davFS struct {
	s     *Service
	user  uuid.UUID
	mount string
}

// davEntry is a resolved path: the mount's root (virtual), or a node.
type davEntry struct {
	virtual bool // the mount root
	top     bool // a team folder or share at the top of its mount
	node    Node
}

// parentRef is the folder to create children in; nil for the own top level.
func (e davEntry) parentRef() *uuid.UUID {
	if e.virtual {
		return nil
	}
	return &e.node.ID
}

func davErr(err error) error {
	_, input := errors.AsType[*InputError](err)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return os.ErrNotExist
	case errors.Is(err, ErrForbidden), input:
		return os.ErrPermission
	case errors.Is(err, ErrNameTaken):
		return os.ErrExist
	}
	return err
}

// childIn finds a live child of a folder by (case-insensitive) name.
func (s *Service) childIn(ctx context.Context, parentID uuid.UUID, name string) (Node, error) {
	rows, err := s.db.Query(ctx, `SELECT `+nodeColumns+` FROM drive.nodes
		WHERE parent_id = $1 AND lower(name) = lower($2) AND trashed_at IS NULL`, parentID, norm.NFC.String(name))
	if err != nil {
		return Node{}, fmt.Errorf("find by name: %w", err)
	}
	n, err := pgx.CollectExactlyOneRow(rows, scanNode)
	if errors.Is(err, pgx.ErrNoRows) {
		return Node{}, ErrNotFound
	}
	return n, err
}

// tops lists what is at the top of the mount.
func (f *davFS) tops(ctx context.Context) ([]Node, error) {
	switch f.mount {
	case mountTeams:
		return f.s.TeamFoldersFor(ctx, f.user)
	case mountShared:
		shared, err := f.s.SharedWithMe(ctx, f.user)
		out := make([]Node, 0, len(shared))
		for _, sh := range shared {
			out = append(out, sh.Node)
		}
		return out, err
	}
	view, err := f.s.List(ctx, f.user, nil)
	return view.Children, err
}

// resolve walks a path from the mount root, then checks access to the
// result with the same authorization as everything else.
func (f *davFS) resolve(ctx context.Context, name string) (davEntry, error) {
	parts := strings.FieldsFunc(path.Clean("/"+name), func(r rune) bool { return r == '/' })
	if len(parts) == 0 {
		return davEntry{virtual: true}, nil
	}
	first := norm.NFC.String(parts[0])
	var cur Node
	var found bool
	var err error
	if f.mount == mountFiles {
		cur, err = f.s.childByName(ctx, f.s.db, f.user, nil, first)
		found = err == nil
	} else {
		tops, err := f.tops(ctx)
		if err != nil {
			return davEntry{}, err
		}
		for _, t := range tops {
			if strings.EqualFold(t.Name, first) {
				cur, found = t, true
				break
			}
		}
	}
	if !found {
		return davEntry{}, ErrNotFound
	}
	for _, p := range parts[1:] {
		if cur.Kind != "folder" {
			return davEntry{}, ErrNotFound
		}
		if cur, err = f.s.childIn(ctx, cur.ID, p); err != nil {
			return davEntry{}, err
		}
	}
	n, err := f.s.authorize(ctx, f.s.db, f.user, cur.ID, authz.View, false)
	if err != nil {
		return davEntry{}, err
	}
	return davEntry{node: n, top: len(parts) == 1 && f.mount != mountFiles}, nil
}

func (f *davFS) Mkdir(ctx context.Context, name string, _ os.FileMode) error {
	dir, base := path.Split(path.Clean("/" + name))
	parent, err := f.resolve(ctx, dir)
	if err != nil {
		return davErr(err)
	}
	if parent.virtual && f.mount != mountFiles {
		return os.ErrPermission
	}
	_, err = f.s.CreateFolder(ctx, f.user, parent.parentRef(), base)
	return davErr(err)
}

func (f *davFS) RemoveAll(ctx context.Context, name string) error {
	e, err := f.resolve(ctx, name)
	if err != nil {
		return davErr(err)
	}
	if e.virtual {
		return os.ErrPermission
	}
	return davErr(f.s.Trash(ctx, f.user, e.node.ID))
}

func (f *davFS) Rename(ctx context.Context, oldName, newName string) error {
	e, err := f.resolve(ctx, oldName)
	if err != nil {
		return davErr(err)
	}
	if e.virtual {
		return os.ErrPermission
	}
	dir, base := path.Split(path.Clean("/" + newName))
	target, err := f.resolve(ctx, dir)
	if err != nil {
		return davErr(err)
	}
	ch := Change{Name: &base}
	switch {
	case target.virtual && f.mount == mountFiles:
		ch.ToTop = true
	case target.virtual:
		return os.ErrPermission
	default:
		ch.Parent = &target.node.ID
	}
	_, err = f.s.Update(ctx, f.user, e.node.ID, ch)
	return davErr(err)
}

func (f *davFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	e, err := f.resolve(ctx, name)
	if err != nil {
		return nil, davErr(err)
	}
	return davInfo{e}, nil
}

func (f *davFS) OpenFile(ctx context.Context, name string, flag int, _ os.FileMode) (webdav.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC) != 0 {
		// Writes come from COPY and from LOCK on a new name; PUT has its own path.
		dir, base := path.Split(path.Clean("/" + name))
		parent, err := f.resolve(ctx, dir)
		if err != nil {
			return nil, davErr(err)
		}
		if parent.virtual && f.mount != mountFiles {
			return nil, os.ErrPermission
		}
		return &davWriter{ctx: ctx, f: f, parent: parent.parentRef(), name: base}, nil
	}
	e, err := f.resolve(ctx, name)
	if err != nil {
		return nil, davErr(err)
	}
	if e.virtual || e.node.Kind == "folder" {
		return &davDir{ctx: ctx, f: f, e: e}, nil
	}
	_, obj, err := f.s.Open(ctx, f.user, e.node.ID)
	if err != nil {
		return nil, davErr(err)
	}
	return &davFile{obj: obj, e: e}, nil
}

// davInfo describes a node (or the mount root) to the webdav library.
type davInfo struct{ e davEntry }

func (i davInfo) Name() string {
	if i.e.virtual {
		return "/"
	}
	return i.e.node.Name
}
func (i davInfo) Size() int64 { return i.e.node.Size }
func (i davInfo) Mode() fs.FileMode {
	if i.IsDir() {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (i davInfo) ModTime() time.Time { return i.e.node.UpdatedAt }
func (i davInfo) IsDir() bool        { return i.e.virtual || i.e.node.Kind == "folder" }
func (i davInfo) Sys() any           { return nil }

// ContentType is always generic: files are never rendered from /dav.
func (i davInfo) ContentType(context.Context) (string, error) { return "application/octet-stream", nil }

// ETag changes with every version (and is stable otherwise), so sync
// clients can tell exactly when a file changed.
func (i davInfo) ETag(context.Context) (string, error) {
	switch {
	case i.e.virtual:
		return `"root"`, nil
	case i.e.node.currentVersionID != nil:
		return `"` + i.e.node.currentVersionID.String() + `"`, nil
	}
	return fmt.Sprintf(`"%s-%d"`, i.e.node.ID, i.e.node.UpdatedAt.UnixNano()), nil
}

// davFile is an open file for reading. It wraps the object rather than
// embedding it, so io.Copy (COPY) always goes through davWriter.ReadFrom.
type davFile struct {
	obj storage.Object
	e   davEntry
}

func (f *davFile) Read(p []byte) (int, error)                { return f.obj.Read(p) }
func (f *davFile) Seek(off int64, whence int) (int64, error) { return f.obj.Seek(off, whence) }
func (f *davFile) Close() error                              { return f.obj.Close() }

func (f *davFile) Readdir(int) ([]fs.FileInfo, error) { return nil, os.ErrInvalid }
func (f *davFile) Stat() (fs.FileInfo, error)         { return davInfo{f.e}, nil }
func (f *davFile) Write([]byte) (int, error)          { return 0, os.ErrPermission }

// davDir is an open folder (or mount root) for listing.
type davDir struct {
	ctx  context.Context
	f    *davFS
	e    davEntry
	read bool
}

func (d *davDir) Close() error                   { return nil }
func (d *davDir) Read([]byte) (int, error)       { return 0, os.ErrInvalid }
func (d *davDir) Seek(int64, int) (int64, error) { return 0, os.ErrInvalid }
func (d *davDir) Write([]byte) (int, error)      { return 0, os.ErrPermission }
func (d *davDir) Stat() (fs.FileInfo, error)     { return davInfo{d.e}, nil }
func (d *davDir) Readdir(count int) ([]fs.FileInfo, error) {
	if d.read {
		if count > 0 {
			return nil, io.EOF
		}
		return nil, nil
	}
	d.read = true
	var nodes []Node
	var err error
	if d.e.virtual {
		nodes, err = d.f.tops(d.ctx)
	} else {
		var view FolderView
		view, err = d.f.s.List(d.ctx, d.f.user, &d.e.node.ID)
		nodes = view.Children
	}
	if err != nil {
		return nil, davErr(err)
	}
	out := make([]fs.FileInfo, len(nodes))
	for i, n := range nodes {
		out[i] = davInfo{davEntry{node: n}}
	}
	return out, nil
}

// davWriter receives content written by the webdav library (COPY, or LOCK
// on a name that does not exist yet, which creates an empty file).
type davWriter struct {
	ctx    context.Context
	f      *davFS
	parent *uuid.UUID
	name   string
	node   *Node
	err    error
}

// ReadFrom streams the source straight to storage; io.Copy uses it.
func (w *davWriter) ReadFrom(r io.Reader) (int64, error) {
	n, err := w.f.s.Store(w.ctx, w.f.user, Upload{ParentID: w.parent, Name: w.name, Size: -1, Body: r, Replace: true})
	if err != nil {
		w.err = davErr(err)
		return 0, w.err
	}
	w.node = &n
	return n.Size, nil
}

func (w *davWriter) Write([]byte) (int, error) {
	return 0, errors.New("webdav: write without ReadFrom")
}

func (w *davWriter) Close() error {
	if w.node == nil && w.err == nil {
		_, err := w.ReadFrom(strings.NewReader(""))
		return err
	}
	return w.err
}

func (w *davWriter) Stat() (fs.FileInfo, error) {
	if w.node == nil {
		if _, err := w.ReadFrom(strings.NewReader("")); err != nil {
			return nil, err
		}
	}
	return davInfo{davEntry{node: *w.node}}, nil
}

func (w *davWriter) Read([]byte) (int, error)           { return 0, os.ErrInvalid }
func (w *davWriter) Seek(int64, int) (int64, error)     { return 0, os.ErrInvalid }
func (w *davWriter) Readdir(int) ([]fs.FileInfo, error) { return nil, os.ErrInvalid }
