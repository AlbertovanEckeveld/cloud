//go:build integration

package drive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
)

func (e *testEnv) syncGet(t *testing.T, user uuid.UUID, path string) (int, SyncPage) {
	t.Helper()
	resp, body := e.request(t, user, "GET", path, nil)
	var p SyncPage
	_ = json.Unmarshal(body, &p)
	return resp.StatusCode, p
}

func itemByID(p SyncPage, id uuid.UUID) (SyncItem, bool) {
	for _, it := range p.Items {
		if it.ID == id {
			return it, true
		}
	}
	return SyncItem{}, false
}

func TestSync(t *testing.T) {
	e := newTestEnv(t)
	u, friend := e.person(t, "Eigenaar"), e.person(t, "Vriend")
	ctx := context.Background()
	docs, _ := e.svc.CreateFolder(ctx, u, nil, "Documenten")
	_, a := e.upload(t, u, docs.ID.String(), "a.txt", []byte("aaa"), false)

	code, tree := e.syncGet(t, u, "/sync/tree?root=mine")
	if code != http.StatusOK || len(tree.Items) != 2 || tree.Cursor == "" {
		t.Fatalf("tree: %d %+v", code, tree)
	}
	if it, _ := itemByID(tree, a.ID); it.ParentID == nil || *it.ParentID != docs.ID || it.SHA256 == "" || it.Version == nil {
		t.Errorf("file in tree: %+v", it)
	}
	if it, _ := itemByID(tree, docs.ID); it.ParentID != nil {
		t.Errorf("top-level folder has a parent: %+v", it)
	}

	// Nothing changed: no items, a fresh cursor.
	_, ch := e.syncGet(t, u, "/sync/changes?root=mine&cursor="+url.QueryEscape(tree.Cursor))
	if len(ch.Items) != 0 {
		t.Errorf("no changes expected: %+v", ch.Items)
	}

	// Upload, rename, new folder, trash: each shows up as current state.
	e.upload(t, u, docs.ID.String(), "a.txt", []byte("bbb!"), true)
	_, b := e.upload(t, u, "root", "b.txt", []byte("b"), false)
	newName := "c.txt"
	_, _ = e.svc.Update(ctx, u, b.ID, Change{Name: &newName})
	_ = e.svc.Trash(ctx, u, docs.ID)
	code, ch = e.syncGet(t, u, "/sync/changes?root=mine&cursor="+url.QueryEscape(ch.Cursor))
	if code != http.StatusOK {
		t.Fatalf("changes: %d", code)
	}
	if it, ok := itemByID(ch, b.ID); !ok || it.Name != "c.txt" || it.Gone {
		t.Errorf("renamed file: %+v", it)
	}
	if it, ok := itemByID(ch, docs.ID); !ok || !it.Gone {
		t.Errorf("trashed folder: %+v", it)
	}

	// Other users' changes are never listed.
	e.upload(t, friend, "root", "van-vriend.txt", []byte("x"), false)
	_, other := e.syncGet(t, u, "/sync/changes?root=mine&cursor="+url.QueryEscape(ch.Cursor))
	if len(other.Items) != 0 {
		t.Errorf("someone else's change listed: %+v", other.Items)
	}

	// A shared folder as root: the friend syncs it; leaving the share or a
	// move out of it makes items "gone".
	_, _ = e.svc.Restore(ctx, u, docs.ID)
	_ = e.share(t, u, docs.ID, friend, authz.Edit)
	code, roots := func() (int, []SyncRoot) {
		resp, body := e.request(t, friend, "GET", "/sync/roots", nil)
		var out struct{ Roots []SyncRoot }
		_ = json.Unmarshal(body, &out)
		return resp.StatusCode, out.Roots
	}()
	if code != http.StatusOK || len(roots) != 2 || roots[1].ID != docs.ID.String() || roots[1].Kind != "shared" {
		t.Fatalf("friend's roots: %d %+v", code, roots)
	}
	_, ftree := e.syncGet(t, friend, "/sync/tree?root="+docs.ID.String())
	if it, ok := itemByID(ftree, a.ID); !ok || it.ParentID != nil {
		t.Errorf("file directly in the shared root: %+v %v", it, ok)
	}
	_, _ = e.svc.Update(ctx, u, a.ID, Change{ToTop: true}) // the owner moves it out
	_, fch := e.syncGet(t, friend, "/sync/changes?root="+docs.ID.String()+"&cursor="+url.QueryEscape(ftree.Cursor))
	if it, ok := itemByID(fch, a.ID); !ok || !it.Gone {
		t.Errorf("moved out of the share: %+v", fch.Items)
	}
	if code, _ := e.syncGet(t, e.person(t, "Vreemde"), "/sync/tree?root="+docs.ID.String()); code != http.StatusNotFound {
		t.Errorf("stranger syncs the share: %d", code)
	}

	// A cursor that is too old means: read the whole tree again.
	if code, _ := e.syncGet(t, u, "/sync/changes?root=mine&cursor=1.00000000-0000-0000-0000-000000000000.1"); code != http.StatusGone {
		t.Errorf("expired cursor: %d", code)
	}
	if code, _ := e.syncGet(t, u, "/sync/changes?root=mine&cursor=rommel"); code != http.StatusUnprocessableEntity {
		t.Errorf("bad cursor: %d", code)
	}
}
