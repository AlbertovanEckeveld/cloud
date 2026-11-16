package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"
)

// fakeServer imitates the Cloud sync and file API for one root ("mine"),
// with a change log the client reads with a cursor.
type fakeServer struct {
	mu      sync.Mutex
	nodes   map[string]*RemoteItem
	content map[string][]byte
	log     []string // node IDs, in order of change
	// badSum makes downloads of this node carry the wrong content.
	badSum map[string]bool
}

func newFakeServer(t *testing.T) (*fakeServer, *Client) {
	f := &fakeServer{nodes: map[string]*RemoteItem{}, content: map[string][]byte{}, badSum: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, true, staticToken("tok"))
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// put creates or changes a node on the "server", as another device would.
func (f *fakeServer) put(parent *string, kind, name string, data []byte) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, n := range f.nodes {
		if ptrEq(n.ParentID, parent) && strings.EqualFold(n.Name, name) {
			f.setContent(id, data)
			return id
		}
	}
	id := uuid.NewString()
	f.nodes[id] = &RemoteItem{ID: id, ParentID: parent, Kind: kind, Name: name}
	if kind == "file" {
		f.setContent(id, data)
	} else {
		f.log = append(f.log, id)
	}
	return id
}

func (f *fakeServer) setContent(id string, data []byte) {
	f.content[id] = data
	f.nodes[id].Size, f.nodes[id].SHA256 = int64(len(data)), sum(data)
	f.log = append(f.log, id)
}

func (f *fakeServer) remove(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var drop func(string)
	drop = func(id string) {
		for cid, n := range f.nodes {
			if n.ParentID != nil && *n.ParentID == id {
				drop(cid)
			}
		}
		delete(f.nodes, id)
		f.log = append(f.log, id)
	}
	drop(id)
}

func (f *fakeServer) find(path string) *RemoteItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	var parent *string
	var cur *RemoteItem
	for _, part := range strings.Split(path, "/") {
		cur = nil
		for _, n := range f.nodes {
			if ptrEq(n.ParentID, parent) && n.Name == part {
				cur = n
			}
		}
		if cur == nil {
			return nil
		}
		parent = &cur.ID
	}
	return cur
}

func ptrEq(a, b *string) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	switch {
	case p == "/api/v1/drive/sync/tree":
		page := Page{Cursor: strconv.Itoa(len(f.log)), Items: []RemoteItem{}}
		for _, n := range f.nodes {
			page.Items = append(page.Items, *n)
		}
		_ = json.NewEncoder(w).Encode(page)
	case p == "/api/v1/drive/sync/changes":
		from, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
		page := Page{Cursor: strconv.Itoa(len(f.log)), Items: []RemoteItem{}}
		for _, id := range f.log[from:] {
			if n, ok := f.nodes[id]; ok {
				page.Items = append(page.Items, *n)
			} else {
				page.Items = append(page.Items, RemoteItem{ID: id, Gone: true})
			}
		}
		_ = json.NewEncoder(w).Encode(page)
	case strings.HasPrefix(p, "/api/v1/drive/files/") && strings.HasSuffix(p, "/content"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/drive/files/"), "/content")
		data, ok := f.content[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if f.badSum[id] {
			data = append([]byte("tampered "), data...)
		}
		_, _ = w.Write(data)
	case p == "/api/v1/drive/files" && r.Method == http.MethodPut:
		parent := parentOf(r.URL.Query().Get("parent"))
		data, _ := io.ReadAll(r.Body)
		name := norm.NFC.String(r.URL.Query().Get("name"))
		f.mu.Unlock()
		id := f.put(parent, "file", name, data)
		f.mu.Lock()
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
	case p == "/api/v1/drive/folders" && r.Method == http.MethodPost:
		var in struct{ ParentID, Name string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Unlock()
		id := f.put(parentOf(in.ParentID), "folder", norm.NFC.String(in.Name), nil)
		f.mu.Lock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
	case strings.HasPrefix(p, "/api/v1/drive/nodes/") && r.Method == http.MethodDelete:
		f.mu.Unlock()
		f.remove(strings.TrimPrefix(p, "/api/v1/drive/nodes/"))
		f.mu.Lock()
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func parentOf(s string) *string {
	if s == "" || s == "root" {
		return nil
	}
	return &s
}

type env struct {
	t      *testing.T
	srv    *fakeServer
	engine *Engine
	dir    string
}

func newEnv(t *testing.T) *env {
	srv, client := newFakeServer(t)
	state, err := OpenState(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &env{t: t, srv: srv, engine: NewEngine(client, state, "laptop", log), dir: t.TempDir()}
}

func (e *env) sync() Report {
	e.t.Helper()
	rep, err := e.engine.Sync(context.Background(), "mine", e.dir, false)
	if err != nil {
		e.t.Fatalf("sync: %v", err)
	}
	return rep
}

func (e *env) write(rel, data string) {
	e.t.Helper()
	p := filepath.Join(e.dir, rel)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) read(rel string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(e.dir, rel))
	return string(b), err == nil
}

// idle reports a run that did nothing.
func idle(r Report) bool {
	return r.Uploaded+r.Downloaded+r.FoldersCreated+r.DeletedLocal+r.TrashedRemote+r.Conflicts == 0 && len(r.Failed) == 0
}

func TestTwoWaySync(t *testing.T) {
	e := newEnv(t)
	docs := e.srv.put(nil, "folder", "Documenten", nil)
	e.srv.put(&docs, "file", "plan.txt", []byte("van de server"))

	e.sync()
	if got, _ := e.read("Documenten/plan.txt"); got != "van de server" {
		t.Fatalf("download: %q", got)
	}
	// Nothing changed: a second run does nothing at all.
	if rep := e.sync(); !idle(rep) {
		t.Errorf("idle run did something: %+v", rep)
	}

	e.write("Documenten/nieuw.txt", "van de laptop")
	e.write("Foto's/a.jpg", "jpeg")
	rep := e.sync()
	if n := e.srv.find("Documenten/nieuw.txt"); n == nil || e.srv.content[n.ID] == nil {
		t.Errorf("upload missing: %+v", rep)
	}
	if e.srv.find("Foto's/a.jpg") == nil {
		t.Error("new folder with file not uploaded")
	}

	// Edits on either side travel; the own upload does not come back.
	e.write("Documenten/plan.txt", "lokaal bewerkt")
	e.srv.put(&docs, "file", "nieuw.txt", []byte("op de server bewerkt"))
	e.sync()
	if string(e.srv.content[e.srv.find("Documenten/plan.txt").ID]) != "lokaal bewerkt" {
		t.Error("local edit not uploaded")
	}
	if got, _ := e.read("Documenten/nieuw.txt"); got != "op de server bewerkt" {
		t.Errorf("server edit not downloaded: %q", got)
	}
	if rep := e.sync(); !idle(rep) {
		t.Errorf("echo of own changes: %+v", rep)
	}

	// Deletions both ways; a local delete goes to the server's trash.
	_ = os.Remove(filepath.Join(e.dir, "Documenten/plan.txt"))
	e.srv.remove(e.srv.find("Foto's").ID)
	e.sync()
	if e.srv.find("Documenten/plan.txt") != nil {
		t.Error("local delete not sent")
	}
	if _, err := os.Stat(filepath.Join(e.dir, "Foto's")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("server delete not applied")
	}
}

func TestConflictsKeepBoth(t *testing.T) {
	e := newEnv(t)
	id := e.srv.put(nil, "file", "notities.txt", []byte("v1"))
	e.sync()
	e.write("notities.txt", "lokaal")
	e.srv.put(nil, "file", "notities.txt", []byte("server"))
	if rep := e.sync(); rep.Conflicts != 1 {
		t.Fatalf("conflict not detected: %+v", rep)
	}
	e.sync() // uploads the conflict copy
	if got, _ := e.read("notities.txt"); got != "server" {
		t.Errorf("server version not in place: %q", got)
	}
	entries, _ := os.ReadDir(e.dir)
	var copyName string
	for _, de := range entries {
		if strings.Contains(de.Name(), "(conflict laptop ") {
			copyName = de.Name()
		}
	}
	if got, _ := e.read(copyName); copyName == "" || got != "lokaal" {
		t.Fatalf("local version lost: %v", entries)
	}
	if e.srv.find(copyName) == nil {
		t.Error("conflict copy not uploaded")
	}
	// Deleted here, changed there: the server's version comes back.
	_ = os.Remove(filepath.Join(e.dir, "notities.txt"))
	e.srv.put(nil, "file", "notities.txt", []byte("server 2"))
	e.sync()
	if got, _ := e.read("notities.txt"); got != "server 2" {
		t.Errorf("changed file lost after local delete: %q", got)
	}
	// Deleted there, changed here: uploaded again.
	e.write("notities.txt", "lokaal 2")
	e.srv.remove(id)
	e.sync()
	if n := e.srv.find("notities.txt"); n == nil || string(e.srv.content[n.ID]) != "lokaal 2" {
		t.Error("local change lost after server delete")
	}
}

func TestFolderWithLocalChangeSurvivesServerDelete(t *testing.T) {
	e := newEnv(t)
	dir := e.srv.put(nil, "folder", "Project", nil)
	e.srv.put(&dir, "file", "a.txt", []byte("a"))
	e.sync()
	e.write("Project/b.txt", "nieuw werk")
	e.srv.remove(dir)
	e.sync()
	if got, ok := e.read("Project/b.txt"); !ok || got != "nieuw werk" {
		t.Fatal("unsynced local work deleted")
	}
	if e.srv.find("Project/b.txt") == nil {
		t.Error("folder and new file not re-created on the server")
	}
	if _, ok := e.read("Project/a.txt"); ok {
		t.Error("unchanged file of the deleted folder kept")
	}
}

func TestMaliciousServerCannotEscape(t *testing.T) {
	e := newEnv(t)
	outside := t.TempDir()
	for _, name := range []string{"../escape.txt", "..", "a/b.txt", `a\b.txt`, "", "line\nbreak"} {
		e.srv.put(nil, "file", name, []byte("evil"))
	}
	// A folder replaced by a symlink to elsewhere must not be written through.
	inside := e.srv.put(nil, "folder", "map", nil)
	e.sync()
	_ = os.RemoveAll(filepath.Join(e.dir, "map"))
	if err := os.Symlink(outside, filepath.Join(e.dir, "map")); err != nil {
		t.Fatal(err)
	}
	e.srv.put(&inside, "file", "inbraak.txt", []byte("evil"))
	e.sync()

	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Errorf("written outside the sync folder: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(e.dir), "escape.txt")); err == nil {
		t.Error("path traversal succeeded")
	}
	local, _ := os.ReadDir(e.dir)
	for _, de := range local {
		if de.Name() != "map" {
			t.Errorf("unsafe server name written: %q", de.Name())
		}
	}
	// The symlink itself is never uploaded or followed.
	if n := e.srv.find("map/inbraak.txt"); n == nil {
		t.Error("server file should remain on the server")
	}
}

func TestChecksumMismatchIsRejected(t *testing.T) {
	e := newEnv(t)
	id := e.srv.put(nil, "file", "rapport.pdf", []byte("echt"))
	e.srv.badSum[id] = true
	rep := e.sync()
	if _, ok := e.read("rapport.pdf"); ok || len(rep.Failed) != 1 {
		t.Errorf("tampered download placed: %+v", rep)
	}
	entries, _ := os.ReadDir(e.dir)
	if len(entries) != 0 {
		t.Errorf("leftovers: %v", entries)
	}
}

func TestMassDeleteBrake(t *testing.T) {
	e := newEnv(t)
	for i := range 30 {
		e.srv.put(nil, "file", fmt.Sprintf("f%02d.txt", i), []byte("x"))
	}
	e.sync()
	for i := range 30 {
		_ = os.Remove(filepath.Join(e.dir, fmt.Sprintf("f%02d.txt", i)))
	}
	_, err := e.engine.Sync(context.Background(), "mine", e.dir, false)
	var mass *MassDeleteError
	if !errors.As(err, &mass) || mass.Remote != 30 {
		t.Fatalf("brake did not engage: %v", err)
	}
	if e.srv.find("f00.txt") == nil {
		t.Fatal("deleted despite the brake")
	}
	if _, err := e.engine.Sync(context.Background(), "mine", e.dir, true); err != nil || e.srv.find("f00.txt") != nil {
		t.Errorf("confirmed run: %v", err)
	}
}

func TestDecomposedNamesAndClutter(t *testing.T) {
	e := newEnv(t)
	nfd := norm.NFD.String("café.txt") // as macOS writes it
	e.write(nfd, "koffie")
	e.write(".DS_Store", "rommel")
	e.write("~$plan.docx", "office lock")
	e.sync()
	if e.srv.find("café.txt") == nil {
		t.Fatal("decomposed name not uploaded")
	}
	if e.srv.find(".DS_Store") != nil || e.srv.find("~$plan.docx") != nil {
		t.Error("clutter uploaded")
	}
	if rep := e.sync(); !idle(rep) {
		t.Errorf("decomposed name re-synced every run: %+v", rep)
	}
}

func TestSymlinksAreNotSynced(t *testing.T) {
	e := newEnv(t)
	secret := filepath.Join(t.TempDir(), "id_rsa")
	_ = os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600)
	_ = os.Symlink(secret, filepath.Join(e.dir, "sleutel"))
	e.sync()
	if e.srv.find("sleutel") != nil {
		t.Error("symlink target uploaded")
	}
}

func TestStateIsPrivate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.db")
	s, err := OpenState(p)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if info, _ := os.Stat(p); info.Mode().Perm() != 0o600 {
		t.Errorf("state mode = %v", info.Mode().Perm())
	}
}

func TestClientRequiresHTTPS(t *testing.T) {
	if _, err := NewClient("http://cloud.example", false, staticToken("x")); err == nil {
		t.Error("plain http accepted")
	}
	if _, err := NewClient("https://user:pw@cloud.example", false, staticToken("x")); err == nil {
		t.Error("credentials in URL accepted")
	}
}
