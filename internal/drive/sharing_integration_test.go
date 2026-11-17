//go:build integration

package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
)

// fakeSharing is an in-memory authz.Authorizer. The real one (identity) has
// its own integration tests; here it only has to behave like it.
type fakeSharing struct {
	mu      sync.Mutex
	grants  map[uuid.UUID]map[authz.Subject]authz.Permission
	members map[uuid.UUID][]uuid.UUID // group -> users
	names   map[uuid.UUID]string
}

func newFakeSharing() *fakeSharing {
	return &fakeSharing{grants: map[uuid.UUID]map[authz.Subject]authz.Permission{},
		members: map[uuid.UUID][]uuid.UUID{}, names: map[uuid.UUID]string{}}
}

func (f *fakeSharing) holds(userID uuid.UUID, sub authz.Subject) bool {
	return sub == authz.Subject{Type: authz.User, ID: userID} ||
		sub.Type == authz.Group && slices.Contains(f.members[sub.ID], userID)
}

func (f *fakeSharing) Permissions(_ context.Context, userID uuid.UUID, _ string, ids []uuid.UUID) (map[uuid.UUID]authz.Permission, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[uuid.UUID]authz.Permission{}
	for _, id := range ids {
		for sub, p := range f.grants[id] {
			if f.holds(userID, sub) {
				out[id] = authz.Max(out[id], p)
			}
		}
	}
	return out, nil
}

func (f *fakeSharing) Shared(_ context.Context, _ string, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[uuid.UUID]bool{}
	for _, id := range ids {
		if len(f.grants[id]) > 0 {
			out[id] = true
		}
	}
	return out, nil
}

func (f *fakeSharing) Grants(_ context.Context, _ string, id uuid.UUID) ([]authz.Grant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []authz.Grant{}
	for sub, p := range f.grants[id] {
		out = append(out, authz.Grant{Subject: sub, Name: f.names[sub.ID], Permission: p})
	}
	return out, nil
}

func (f *fakeSharing) Grant(_ context.Context, _ uuid.UUID, _ string, id uuid.UUID, to authz.Subject, p authz.Permission) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.names[to.ID]; !ok {
		return authz.ErrUnknownSubject
	}
	if f.grants[id] == nil {
		f.grants[id] = map[authz.Subject]authz.Permission{}
	}
	f.grants[id][to] = p
	return nil
}

func (f *fakeSharing) Revoke(_ context.Context, _ uuid.UUID, _ string, id uuid.UUID, from authz.Subject) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.grants[id][from]; !ok {
		return authz.ErrNoGrant
	}
	delete(f.grants[id], from)
	return nil
}

func (f *fakeSharing) RevokeAll(_ context.Context, _ string, ids []uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range ids {
		delete(f.grants, id)
	}
	return nil
}

func (f *fakeSharing) SharedWith(_ context.Context, userID uuid.UUID, _ string) ([]authz.Shared, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []authz.Shared
	for id, subs := range f.grants {
		var best authz.Permission
		for sub, p := range subs {
			if f.holds(userID, sub) {
				best = authz.Max(best, p)
			}
		}
		if best != authz.None {
			out = append(out, authz.Shared{ResourceID: id, Permission: best})
		}
	}
	return out, nil
}

func (f *fakeSharing) DisplayNames(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[uuid.UUID]string{}
	for _, id := range ids {
		if n, ok := f.names[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}

// person registers a user (with a display name) in the fake directory.
func (e *testEnv) person(t *testing.T, name string) uuid.UUID {
	id := e.newUser(t)
	e.sharing.mu.Lock()
	e.sharing.names[id] = name
	e.sharing.mu.Unlock()
	return id
}

func (e *testEnv) share(t *testing.T, actor, node, to uuid.UUID, p authz.Permission) int {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"subject": authz.Subject{Type: authz.User, ID: to}, "permission": p})
	resp, _ := e.request(t, actor, "PUT", "/nodes/"+node.String()+"/shares", bytes.NewReader(body), "Content-Type", "application/json")
	return resp.StatusCode
}

func (e *testEnv) status(t *testing.T, user uuid.UUID, method, path, body string) int {
	t.Helper()
	resp, _ := e.request(t, user, method, path, strings.NewReader(body), "Content-Type", "application/json")
	return resp.StatusCode
}

func TestShareView(t *testing.T) {
	e := newTestEnv(t)
	owner, viewer, stranger := e.person(t, "Eigenaar"), e.person(t, "Kijker"), e.person(t, "Vreemde")
	ctx := context.Background()
	outer, _ := e.svc.CreateFolder(ctx, owner, nil, "Privé")
	shared, _ := e.svc.CreateFolder(ctx, owner, &outer.ID, "Vakantie")
	sub, _ := e.svc.CreateFolder(ctx, owner, &shared.ID, "Dag 1")
	_, file := e.upload(t, owner, sub.ID.String(), "foto.jpg", []byte("jpeg"), false)

	if code := e.share(t, owner, shared.ID, viewer, authz.View); code != http.StatusOK {
		t.Fatalf("share: %d", code)
	}
	// The owner sees the folder as shared.
	view, _ := e.svc.List(ctx, owner, &outer.ID)
	if len(view.Children) != 1 || !view.Children[0].Shared || view.Children[0].Permission != authz.Manage {
		t.Errorf("owner listing: %+v", view.Children)
	}

	// The share is inherited by everything below it.
	resp, body := e.request(t, viewer, "GET", "/folders/"+sub.ID.String(), nil)
	var fv FolderView
	_ = json.Unmarshal(body, &fv)
	if resp.StatusCode != http.StatusOK || !fv.Shared || len(fv.Children) != 1 || fv.Children[0].Permission != authz.View {
		t.Fatalf("viewer lists subfolder: %d %s", resp.StatusCode, body)
	}
	// The path starts at the share: the owner's "Privé" stays private.
	if len(fv.Path) != 2 || fv.Path[0].ID != shared.ID || fv.Path[1].ID != sub.ID {
		t.Errorf("path = %+v", fv.Path)
	}
	resp, body = e.request(t, viewer, "GET", "/folders/"+shared.ID.String(), nil)
	_ = json.Unmarshal(body, &fv)
	if fv.Folder == nil || fv.Folder.ParentID != nil || strings.Contains(string(body), outer.ID.String()) {
		t.Errorf("the share reveals its parent: %s", body)
	}
	if resp, body := e.request(t, viewer, "GET", "/files/"+file.ID.String()+"/content", nil); resp.StatusCode != http.StatusOK || string(body) != "jpeg" {
		t.Errorf("viewer download: %d %q", resp.StatusCode, body)
	}

	// View does not allow changes, and gives 403 (the viewer knows the node).
	for _, req := range []struct{ method, path, body string }{
		{"PUT", "/files?parent=" + sub.ID.String() + "&name=nieuw.txt", "x"},
		{"POST", "/folders", `{"parent_id":"` + shared.ID.String() + `","name":"Nieuw"}`},
		{"PATCH", "/nodes/" + file.ID.String(), `{"name":"anders.jpg"}`},
		{"DELETE", "/nodes/" + file.ID.String(), ""},
		{"GET", "/nodes/" + shared.ID.String() + "/shares", ""},
		{"PUT", "/nodes/" + shared.ID.String() + "/shares", `{"subject":{"type":"user","id":"` + stranger.String() + `"},"permission":"view"}`},
	} {
		if code := e.status(t, viewer, req.method, req.path, req.body); code != http.StatusForbidden {
			t.Errorf("viewer %s %s: %d, want 403", req.method, req.path, code)
		}
	}
	// Above the share, and for others, nothing exists.
	for _, path := range []string{"/folders/" + outer.ID.String()} {
		if code := e.status(t, viewer, "GET", path, ""); code != http.StatusNotFound {
			t.Errorf("viewer GET %s: %d, want 404", path, code)
		}
	}
	if code := e.status(t, stranger, "GET", "/folders/"+sub.ID.String(), ""); code != http.StatusNotFound {
		t.Errorf("stranger: %d", code)
	}

	// "Shared with me" lists the share once, with the owner's name.
	resp, body = e.request(t, viewer, "GET", "/shared", nil)
	var list struct{ Nodes []SharedNode }
	_ = json.Unmarshal(body, &list)
	if len(list.Nodes) != 1 || list.Nodes[0].ID != shared.ID || list.Nodes[0].OwnerName != "Eigenaar" || list.Nodes[0].ParentID != nil {
		t.Errorf("shared with me: %s", body)
	}

	// The viewer can drop the share themselves.
	if code := e.status(t, viewer, "DELETE", "/nodes/"+shared.ID.String()+"/shares/user/"+viewer.String(), ""); code != http.StatusNoContent {
		t.Errorf("leave share: %d", code)
	}
	if code := e.status(t, viewer, "GET", "/folders/"+sub.ID.String(), ""); code != http.StatusNotFound {
		t.Errorf("after leaving: %d", code)
	}
}

func TestShareEdit(t *testing.T) {
	e := newTestEnv(t)
	owner, editor := e.person(t, "Eigenaar"), e.person(t, "Bewerker")
	ctx := context.Background()
	shared, _ := e.svc.CreateFolder(ctx, owner, nil, "Samen")
	mine, _ := e.svc.CreateFolder(ctx, editor, nil, "Van mij")
	if code := e.share(t, owner, shared.ID, editor, authz.Edit); code != http.StatusOK {
		t.Fatalf("share: %d", code)
	}

	// An upload into the shared folder belongs to (and is counted for) the owner.
	code, file := e.upload(t, editor, shared.ID.String(), "notulen.txt", []byte("v1"), false)
	if code != http.StatusCreated || file.Permission != authz.Edit {
		t.Fatalf("editor upload: %d %+v", code, file)
	}
	if u, _ := e.svc.Usage(ctx, owner); u.UsedBytes != 2 {
		t.Errorf("owner usage = %d", u.UsedBytes)
	}
	if u, _ := e.svc.Usage(ctx, editor); u.UsedBytes != 0 {
		t.Errorf("editor usage = %d", u.UsedBytes)
	}
	// The owner sees who changed it.
	if view, _ := e.svc.List(ctx, owner, &shared.ID); len(view.Children) != 1 || view.Children[0].UpdatedByName != "Bewerker" {
		t.Errorf("last changed by: %+v", view.Children)
	}
	versions, _ := e.svc.Versions(ctx, owner, file.ID)
	if len(versions) != 1 || versions[0].CreatedByName != "Bewerker" {
		t.Errorf("versions = %+v", versions)
	}
	// The owner's quota applies.
	limit := int64(3)
	_ = e.svc.SetQuota(ctx, owner, &limit)
	if code, _ := e.upload(t, editor, shared.ID.String(), "groot.txt", []byte("te groot"), false); code != http.StatusInsufficientStorage {
		t.Errorf("upload over the owner's quota: %d", code)
	}
	_ = e.svc.SetQuota(ctx, owner, nil)

	sub := `{"parent_id":"` + shared.ID.String() + `","name":"Map"}`
	if code := e.status(t, editor, "POST", "/folders", sub); code != http.StatusCreated {
		t.Errorf("editor creates folder: %d", code)
	}
	if code := e.status(t, editor, "PATCH", "/nodes/"+file.ID.String(), `{"name":"verslag.txt"}`); code != http.StatusOK {
		t.Errorf("editor renames: %d", code)
	}
	if view, _ := e.svc.List(ctx, owner, &shared.ID); view.Children[len(view.Children)-1].UpdatedBy == nil || *view.Children[len(view.Children)-1].UpdatedBy != editor {
		t.Errorf("rename did not record the editor: %+v", view.Children)
	}
	// Files never change owner: no moving into the editor's own folders.
	for _, target := range []string{"root", mine.ID.String()} {
		if code := e.status(t, editor, "PATCH", "/nodes/"+file.ID.String(), `{"parent_id":"`+target+`"}`); code != http.StatusUnprocessableEntity && code != http.StatusNotFound {
			t.Errorf("editor moves to %s: %d", target, code)
		}
	}
	// Renaming the share itself keeps it where it is in the owner's tree.
	if code := e.status(t, editor, "PATCH", "/nodes/"+shared.ID.String(), `{"name":"Samen 2"}`); code != http.StatusOK {
		t.Errorf("editor renames the share: %d", code)
	}
	if view, _ := e.svc.List(ctx, owner, nil); !slices.ContainsFunc(view.Children, func(n Node) bool { return n.Name == "Samen 2" }) {
		t.Errorf("the renamed share left the owner's top level: %+v", view.Children)
	}
	// Edit cannot share further, nor delete history.
	if code := e.share(t, editor, shared.ID, owner, authz.View); code != http.StatusForbidden {
		t.Errorf("editor shares: %d", code)
	}

	// Deleting goes to the owner's trash.
	if code := e.status(t, editor, "DELETE", "/nodes/"+file.ID.String(), ""); code != http.StatusNoContent {
		t.Errorf("editor trashes: %d", code)
	}
	if trash, _ := e.svc.ListTrash(ctx, owner); len(trash) != 1 || trash[0].ID != file.ID {
		t.Errorf("owner's trash = %+v", trash)
	}
	if trash, _ := e.svc.ListTrash(ctx, editor); len(trash) != 0 {
		t.Errorf("editor's trash = %+v", trash)
	}
	if code := e.status(t, editor, "POST", "/trash/"+file.ID.String()+"/restore", ""); code != http.StatusNotFound {
		t.Errorf("editor restores from the owner's trash: %d", code)
	}
}

func TestShareManage(t *testing.T) {
	e := newTestEnv(t)
	owner, manager, third := e.person(t, "Eigenaar"), e.person(t, "Beheerder"), e.person(t, "Derde")
	ctx := context.Background()
	folder, _ := e.svc.CreateFolder(ctx, owner, nil, "Team")
	_ = e.share(t, owner, folder.ID, manager, authz.Manage)

	if code := e.share(t, manager, folder.ID, third, authz.View); code != http.StatusOK {
		t.Errorf("manager shares further: %d", code)
	}
	for name, to := range map[string]uuid.UUID{"owner": owner, "self": manager, "unknown": uuid.New()} {
		if code := e.share(t, manager, folder.ID, to, authz.View); code != http.StatusUnprocessableEntity {
			t.Errorf("share with %s: %d", name, code)
		}
	}
	if code := e.share(t, owner, folder.ID, third, "admin"); code != http.StatusUnprocessableEntity {
		t.Errorf("invalid permission: %d", code)
	}
	resp, body := e.request(t, manager, "GET", "/nodes/"+folder.ID.String()+"/shares", nil)
	var shares struct{ Shares []authz.Grant }
	_ = json.Unmarshal(body, &shares)
	if resp.StatusCode != http.StatusOK || len(shares.Shares) != 2 {
		t.Errorf("shares: %d %s", resp.StatusCode, body)
	}
	if code := e.status(t, manager, "DELETE", "/nodes/"+folder.ID.String()+"/shares/user/"+third.String(), ""); code != http.StatusNoContent {
		t.Errorf("manager revokes: %d", code)
	}
	if code := e.status(t, manager, "DELETE", "/nodes/"+folder.ID.String()+"/shares/user/"+third.String(), ""); code != http.StatusNotFound {
		t.Errorf("revoke twice: %d", code)
	}

	// Group shares: membership counts at the moment of the request.
	group := uuid.New()
	e.sharing.mu.Lock()
	e.sharing.names[group] = "Familie"
	e.sharing.members[group] = []uuid.UUID{third}
	e.sharing.mu.Unlock()
	body, _ = json.Marshal(map[string]any{"subject": authz.Subject{Type: authz.Group, ID: group}, "permission": authz.View})
	if resp, _ := e.request(t, owner, "PUT", "/nodes/"+folder.ID.String()+"/shares", bytes.NewReader(body), "Content-Type", "application/json"); resp.StatusCode != http.StatusOK {
		t.Fatalf("group share: %d", resp.StatusCode)
	}
	if code := e.status(t, third, "GET", "/folders/"+folder.ID.String(), ""); code != http.StatusOK {
		t.Errorf("group member: %d", code)
	}
	// A group share cannot be left by one member.
	if code := e.status(t, third, "DELETE", "/nodes/"+folder.ID.String()+"/shares/user/"+third.String(), ""); code != http.StatusUnprocessableEntity {
		t.Errorf("leave a group share: %d", code)
	}
	e.sharing.mu.Lock()
	e.sharing.members[group] = nil
	e.sharing.mu.Unlock()
	if code := e.status(t, third, "GET", "/folders/"+folder.ID.String(), ""); code != http.StatusNotFound {
		t.Errorf("after leaving the group: %d", code)
	}

	// A share in the trash is invisible; purging it removes its shares.
	_ = e.svc.Trash(ctx, owner, folder.ID)
	if code := e.status(t, manager, "GET", "/folders/"+folder.ID.String(), ""); code != http.StatusNotFound {
		t.Errorf("trashed share: %d", code)
	}
	if list, _ := e.svc.SharedWithMe(ctx, manager); len(list) != 0 {
		t.Errorf("trashed share listed: %+v", list)
	}
	if err := e.svc.DeleteForever(ctx, owner, folder.ID); err != nil {
		t.Fatal(err)
	}
	if grants, _ := e.sharing.Grants(ctx, nodeType, folder.ID); len(grants) != 0 {
		t.Errorf("shares survived the purge: %+v", grants)
	}
}

func TestVersions(t *testing.T) {
	e := newTestEnv(t)
	owner, viewer, editor := e.person(t, "Eigenaar"), e.person(t, "Kijker"), e.person(t, "Bewerker")
	ctx := context.Background()
	_, file := e.upload(t, owner, "root", "plan.txt", []byte("versie een"), false)
	e.clock = e.clock.Add(time.Second)
	_, _ = e.upload(t, owner, "root", "plan.txt", []byte("versie twee!"), true)
	_ = e.share(t, owner, file.ID, viewer, authz.View)
	_ = e.share(t, owner, file.ID, editor, authz.Edit)

	resp, body := e.request(t, viewer, "GET", "/files/"+file.ID.String()+"/versions", nil)
	var list struct{ Versions []Version }
	_ = json.Unmarshal(body, &list)
	if resp.StatusCode != http.StatusOK || len(list.Versions) != 2 || !list.Versions[0].Current || list.Versions[1].Current {
		t.Fatalf("versions: %d %s", resp.StatusCode, body)
	}
	oldID := list.Versions[1].ID.String()
	base := "/files/" + file.ID.String() + "/versions/"
	if resp, body := e.request(t, viewer, "GET", base+oldID+"/content", nil); resp.StatusCode != http.StatusOK || string(body) != "versie een" ||
		resp.Header.Get("Content-Security-Policy") == "" || resp.Header.Get("ETag") != `"`+oldID+`"` {
		t.Errorf("old version download: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	if code := e.status(t, viewer, "POST", base+oldID+"/restore", ""); code != http.StatusForbidden {
		t.Errorf("viewer restores: %d", code)
	}
	if code := e.status(t, viewer, "GET", base+uuid.NewString()+"/content", ""); code != http.StatusNotFound {
		t.Errorf("unknown version: %d", code)
	}

	resp, body = e.request(t, editor, "POST", base+oldID+"/restore", nil)
	var restored Node
	_ = json.Unmarshal(body, &restored)
	if resp.StatusCode != http.StatusOK || restored.Size != int64(len("versie een")) {
		t.Fatalf("restore: %d %s", resp.StatusCode, body)
	}
	if _, body := e.request(t, owner, "GET", "/files/"+file.ID.String()+"/content", nil); string(body) != "versie een" {
		t.Errorf("after restore: %q", body)
	}

	// Deleting history needs manage, and never the current version.
	_, body = e.request(t, owner, "GET", "/files/"+file.ID.String()+"/versions", nil)
	_ = json.Unmarshal(body, &list)
	newer := list.Versions[0].ID.String() // now an old version
	if code := e.status(t, editor, "DELETE", base+newer, ""); code != http.StatusForbidden {
		t.Errorf("editor deletes a version: %d", code)
	}
	if code := e.status(t, owner, "DELETE", base+oldID, ""); code != http.StatusUnprocessableEntity {
		t.Errorf("delete current version: %d", code)
	}
	if code := e.status(t, owner, "DELETE", base+newer, ""); code != http.StatusNoContent {
		t.Errorf("delete old version: %d", code)
	}
	if u, _ := e.svc.Usage(ctx, owner); u.UsedBytes != int64(len("versie een")) {
		t.Errorf("usage after deleting a version = %d", u.UsedBytes)
	}
	var pending int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM drive.object_deletions WHERE object_key = $1`, "drive/"+newer).Scan(&pending)
	deadline := time.Now().Add(5 * time.Second)
	for pending != 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM drive.object_deletions WHERE object_key = $1`, "drive/"+newer).Scan(&pending)
	}
	if _, _, err := e.objects.Open(ctx, "drive/"+newer); err == nil {
		t.Error("the deleted version's object still exists")
	}
}
