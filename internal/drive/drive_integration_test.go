//go:build integration

package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlbertovanEckeveld/cloud/internal/core/config"
	"github.com/AlbertovanEckeveld/cloud/internal/core/database"
	"github.com/AlbertovanEckeveld/cloud/internal/core/storage"
	"github.com/AlbertovanEckeveld/cloud/migrations"
)

type testEnv struct {
	svc     *Service
	sharing *fakeSharing
	audited []string
	pool    *pgxpool.Pool
	objects *storage.Client
	srv     *httptest.Server
	clock   time.Time
}

// newTestEnv runs drive against the test database and the real S3. A fake
// authentication layer takes the user from the X-Test-User header.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		t.Fatalf("integration config: %v", err)
	}
	pool, err := database.Open(ctx, cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fsys, _ := migrations.For("drive")
	if err := database.Migrate(ctx, pool, logger, database.Module{Name: "drive", FS: fsys}); err != nil {
		t.Fatal(err)
	}
	objects, err := storage.New(cfg.Storage)
	if err != nil {
		t.Fatal(err)
	}
	e := &testEnv{pool: pool, objects: objects, clock: time.Now(), sharing: newFakeSharing()}
	e.svc = New(pool, objects, Options{Logger: logger, MaxFileSize: 1 << 20, Sharing: e.sharing})
	e.svc.now = func() time.Time { return e.clock }

	r := chi.NewRouter()
	userFrom := func(r *http.Request) uuid.UUID { id, _ := uuid.Parse(r.Header.Get("X-Test-User")); return id }
	pass := func(next http.Handler) http.Handler { return next }
	r.Route("/api/v1/drive", func(r chi.Router) {
		e.svc.Routes(r, Auth{RequireUser: pass, RequireAdmin: pass, RequireRecentAuth: pass, UserID: userFrom,
			Audit: func(_ *http.Request, event string, _ map[string]any) { e.audited = append(e.audited, event) }})
	})
	r.Route("/dav", func(r chi.Router) { e.svc.DAVRoutes(r, pass, userFrom) })
	e.srv = httptest.NewServer(r)
	t.Cleanup(e.srv.Close)
	return e
}

// newUser returns a fresh owner ID; everything it owns is removed afterwards,
// from the database and from S3.
func (e *testEnv) newUser(t *testing.T) uuid.UUID {
	t.Helper()
	id := newID()
	t.Cleanup(func() {
		ctx := context.Background()
		rows, _ := e.pool.Query(ctx, `SELECT v.object_key FROM drive.versions v JOIN drive.nodes n ON n.id = v.node_id WHERE n.owner_id = $1`, id)
		var keys []string
		for rows.Next() {
			var k string
			_ = rows.Scan(&k)
			keys = append(keys, k)
		}
		rows.Close()
		for _, k := range keys {
			if err := e.objects.Delete(ctx, k); err != nil {
				t.Errorf("cleanup object: %v", err)
			}
		}
		for _, q := range []string{`DELETE FROM drive.nodes WHERE owner_id = $1`, `DELETE FROM drive.quotas WHERE user_id = $1`} {
			if _, err := e.pool.Exec(ctx, q, id); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
	})
	return id
}

func (e *testEnv) request(t *testing.T, user uuid.UUID, method, path string, body io.Reader, headers ...string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+"/api/v1/drive"+path, body)
	req.Header.Set("X-Test-User", user.String())
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func (e *testEnv) upload(t *testing.T, user uuid.UUID, parent, name string, content []byte, replace bool) (int, Node) {
	t.Helper()
	path := fmt.Sprintf("/files?parent=%s&name=%s&replace=%v", parent, urlEscape(name), replace)
	resp, body := e.request(t, user, "PUT", path, bytes.NewReader(content), "Content-Type", "application/octet-stream")
	var n Node
	_ = json.Unmarshal(body, &n)
	return resp.StatusCode, n
}

func urlEscape(s string) string { return url.QueryEscape(s) }

func TestFoldersAndNames(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	ctx := context.Background()

	docs, err := e.svc.CreateFolder(ctx, u, nil, "Documenten")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateFolder(ctx, u, nil, "documenten"); !errors.Is(err, ErrNameTaken) {
		t.Errorf("case-insensitive duplicate: err = %v", err)
	}
	if _, err := e.svc.CreateFolder(ctx, u, nil, "Café"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateFolder(ctx, u, nil, "Café"); !errors.Is(err, ErrNameTaken) {
		t.Errorf("NFC/NFD duplicate: err = %v", err)
	}
	sub, err := e.svc.CreateFolder(ctx, u, &docs.ID, "2026")
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := e.upload(t, u, "root", "b.txt", []byte("x"), false); st != http.StatusCreated {
		t.Fatalf("upload: %d", st)
	}

	view, err := e.svc.List(ctx, u, nil)
	if err != nil || len(view.Children) != 3 || view.Children[0].Kind != "folder" || view.Children[2].Name != "b.txt" {
		t.Fatalf("top level = %+v, %v", view.Children, err)
	}
	view, err = e.svc.List(ctx, u, &sub.ID)
	if err != nil || len(view.Path) != 2 || view.Path[0].Name != "Documenten" || view.Path[1].Name != "2026" {
		t.Fatalf("path = %+v, %v", view.Path, err)
	}
}

func TestUploadDownload(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	content := []byte(strings.Repeat("Hallo wereld! ", 1000))

	st, n := e.upload(t, u, "root", `rapport "2026".html`, content, false)
	if st != http.StatusCreated || n.Size != int64(len(content)) || n.ContentType != "text/html; charset=utf-8" {
		t.Fatalf("upload: %d %+v", st, n)
	}
	resp, body := e.request(t, u, "GET", "/files/"+n.ID.String()+"/content", nil)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, content) {
		t.Fatalf("download: %d, %d bytes", resp.StatusCode, len(body))
	}
	// An uploaded HTML page must never be rendered by the browser.
	h := resp.Header
	if h.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(h.Get("Content-Disposition"), "attachment;") ||
		!strings.Contains(h.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("download headers = %v", h)
	}

	// Range requests make downloads resumable.
	resp, body = e.request(t, u, "GET", "/files/"+n.ID.String()+"/content", nil, "Range", "bytes=0-4")
	if resp.StatusCode != http.StatusPartialContent || string(body) != "Hallo" {
		t.Errorf("range: %d %q", resp.StatusCode, body)
	}

	// Same name: refused, unless replacing, which adds a version.
	if st, _ := e.upload(t, u, "root", `RAPPORT "2026".html`, []byte("x"), false); st != http.StatusConflict {
		t.Errorf("duplicate upload: %d", st)
	}
	st, replaced := e.upload(t, u, "root", `rapport "2026".html`, []byte("nieuw"), true)
	if st != http.StatusCreated || replaced.ID != n.ID {
		t.Fatalf("replace: %d %+v", st, replaced)
	}
	if _, body = e.request(t, u, "GET", "/files/"+n.ID.String()+"/content", nil); string(body) != "nieuw" {
		t.Errorf("after replace: %q", body)
	}
	var versions int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM drive.versions WHERE node_id = $1`, n.ID).Scan(&versions)
	if versions != 2 {
		t.Errorf("versions = %d, want 2", versions)
	}
}

func TestUploadLimits(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	ctx := context.Background()

	// Over the maximum file size (1 MiB in this test).
	if st, _ := e.upload(t, u, "root", "big.bin", make([]byte, 1<<20+1), false); st != http.StatusRequestEntityTooLarge {
		t.Errorf("too large: %d", st)
	}
	// Without a declared length (chunked), the size cannot be checked up front.
	resp, _ := e.request(t, u, "PUT", "/files?parent=root&name=x.bin", io.NopCloser(strings.NewReader("abc")))
	if resp.StatusCode != http.StatusLengthRequired {
		t.Errorf("chunked upload: %d", resp.StatusCode)
	}
	// A body shorter than declared is rejected, and its object is cleaned up.
	_, err := e.svc.Store(ctx, u, Upload{Name: "short.bin", Size: 100, Body: strings.NewReader("only 17 bytes...!")})
	if !errors.Is(err, ErrIncomplete) {
		t.Errorf("short body: err = %v", err)
	}
	// Quota.
	limit := int64(10)
	if err := e.svc.SetQuota(ctx, u, &limit); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.upload(t, u, "root", "past.txt", []byte("12345"), false); st != http.StatusCreated {
		t.Fatalf("within quota: %d", st)
	}
	if st, _ := e.upload(t, u, "root", "past-niet.txt", []byte("123456"), false); st != http.StatusInsufficientStorage {
		t.Errorf("over quota: %d", st)
	}
	usage, _ := e.svc.Usage(ctx, u)
	if usage.UsedBytes != 5 || usage.LimitBytes == nil || *usage.LimitBytes != 10 {
		t.Errorf("usage = %+v", usage)
	}
}

func TestPreview(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	_, png := e.upload(t, u, "root", "foto.png", []byte("\x89PNG\r\n\x1a\nrest"), false)
	_, page := e.upload(t, u, "root", "aanval.html", []byte("<script>alert(1)</script>"), false)
	_, svg := e.upload(t, u, "root", "logo.svg", []byte("<svg onload=alert(1)/>"), false)
	_, fake := e.upload(t, u, "root", "nep.png", []byte("<html><script>alert(1)</script>"), false)

	check := func(n Node, wantStatus int, wantType string) {
		t.Helper()
		resp, _ := e.request(t, u, "GET", "/files/"+n.ID.String()+"/content?inline=1", nil)
		if resp.StatusCode != wantStatus || (wantType != "" && resp.Header.Get("Content-Type") != wantType) {
			t.Errorf("%s: %d %q", n.Name, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if wantStatus == http.StatusOK && (!strings.HasPrefix(resp.Header.Get("Content-Disposition"), "inline") ||
			!strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox")) {
			t.Errorf("%s: headers %v", n.Name, resp.Header)
		}
	}
	check(png, http.StatusOK, "image/png")
	check(page, http.StatusOK, "text/plain; charset=utf-8") // shown as source, never run
	check(svg, http.StatusUnsupportedMediaType, "")
	check(fake, http.StatusOK, "image/png") // nosniff keeps it an (invalid) image
}

func TestOtherUsersSeeNothing(t *testing.T) {
	e := newTestEnv(t)
	owner, other := e.newUser(t), e.newUser(t)
	folder, _ := e.svc.CreateFolder(context.Background(), owner, nil, "Privé")
	_, file := e.upload(t, owner, folder.ID.String(), "geheim.txt", []byte("geheim"), false)

	for _, req := range []struct{ method, path, body string }{
		{"GET", "/folders/" + folder.ID.String(), ""},
		{"GET", "/files/" + file.ID.String() + "/content", ""},
		{"PATCH", "/nodes/" + file.ID.String(), `{"name":"van mij"}`},
		{"DELETE", "/nodes/" + file.ID.String(), ""},
		{"PUT", "/files?parent=" + folder.ID.String() + "&name=inbraak.txt", "x"},
	} {
		var body io.Reader
		if req.body != "" {
			body = strings.NewReader(req.body)
		}
		resp, _ := e.request(t, other, req.method, req.path, body, "Content-Type", "application/json")
		// 404, not 403: whether the node exists is not revealed.
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s as other user: %d, want 404", req.method, req.path, resp.StatusCode)
		}
	}
}

func TestMoveAndRename(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	ctx := context.Background()
	a, _ := e.svc.CreateFolder(ctx, u, nil, "A")
	b, _ := e.svc.CreateFolder(ctx, u, &a.ID, "B")
	_, f := e.upload(t, u, "root", "f.txt", []byte("f"), false)

	if _, err := e.svc.Update(ctx, u, a.ID, Change{Parent: &b.ID}); !errors.Is(err, ErrCycle) {
		t.Errorf("move into own descendant: err = %v", err)
	}
	if _, err := e.svc.Update(ctx, u, a.ID, Change{Parent: &a.ID}); !errors.Is(err, ErrCycle) {
		t.Errorf("move into itself: err = %v", err)
	}
	if _, err := e.svc.Update(ctx, u, f.ID, Change{Parent: &f.ID}); !errors.Is(err, ErrNotFolder) {
		t.Errorf("move into a file: err = %v", err)
	}
	newName := "g.txt"
	moved, err := e.svc.Update(ctx, u, f.ID, Change{Name: &newName, Parent: &b.ID})
	if err != nil || moved.Name != "g.txt" || *moved.ParentID != b.ID {
		t.Fatalf("move+rename = %+v, %v", moved, err)
	}
	if _, err := e.svc.Update(ctx, u, b.ID, Change{ToTop: true}); err != nil {
		t.Fatal(err)
	}
	if view, _ := e.svc.List(ctx, u, nil); len(view.Children) != 2 {
		t.Errorf("top level after moving B up: %+v", view.Children)
	}
}

func TestTrash(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	ctx := context.Background()
	folder, _ := e.svc.CreateFolder(ctx, u, nil, "Oud")
	_, inside := e.upload(t, u, folder.ID.String(), "in.txt", []byte("binnen"), false)
	_, loose := e.upload(t, u, "root", "los.txt", []byte("los"), false)

	if err := e.svc.Trash(ctx, u, folder.ID); err != nil {
		t.Fatal(err)
	}
	// Everything inside a trashed folder is gone from view.
	if resp, _ := e.request(t, u, "GET", "/files/"+inside.ID.String()+"/content", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("file in trashed folder: %d", resp.StatusCode)
	}
	// The name is free again.
	if _, err := e.svc.CreateFolder(ctx, u, nil, "Oud"); err != nil {
		t.Fatalf("reuse trashed name: %v", err)
	}
	// Restoring with the name taken adds a suffix.
	restored, err := e.svc.Restore(ctx, u, folder.ID)
	if err != nil || restored.Name != "Oud (hersteld)" {
		t.Fatalf("restore = %+v, %v", restored, err)
	}

	// Deleting forever removes the S3 objects too.
	var key string
	_ = e.pool.QueryRow(ctx, `SELECT object_key FROM drive.versions WHERE node_id = $1`, loose.ID).Scan(&key)
	if err := e.svc.Trash(ctx, u, loose.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteForever(ctx, u, loose.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.deleteObjects(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.objects.Open(ctx, key); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("object still in S3 after deleting forever: %v", err)
	}

	// Trash older than 30 days is purged automatically.
	if err := e.svc.Trash(ctx, u, restored.ID); err != nil {
		t.Fatal(err)
	}
	e.clock = e.clock.Add(trashRetention + time.Hour)
	if err := e.svc.purgeExpiredTrash(ctx); err != nil {
		t.Fatal(err)
	}
	if trash, _ := e.svc.ListTrash(ctx, u); len(trash) != 0 {
		t.Errorf("expired trash not purged: %+v", trash)
	}
	if err := e.svc.deleteObjects(ctx); err != nil {
		t.Fatal(err)
	}
}
