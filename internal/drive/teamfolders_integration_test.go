//go:build integration

package drive

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
)

// cleanupTeamFolders removes the team folders an administrator created, with
// their files in S3, after the test.
func (e *testEnv) cleanupTeamFolders(t *testing.T, admin uuid.UUID) {
	t.Cleanup(func() {
		ctx := context.Background()
		rows, _ := e.pool.Query(ctx, `
			SELECT v.object_key FROM drive.versions v JOIN drive.nodes n ON n.id = v.node_id
			WHERE n.owner_id IN (SELECT id FROM drive.team_folders WHERE created_by = $1)`, admin)
		var keys []string
		for rows.Next() {
			var k string
			_ = rows.Scan(&k)
			keys = append(keys, k)
		}
		rows.Close()
		for _, k := range keys {
			_ = e.objects.Delete(ctx, k)
		}
		var teams []uuid.UUID
		rows, _ = e.pool.Query(ctx, `SELECT id FROM drive.team_folders WHERE created_by = $1`, admin)
		for rows.Next() {
			var id uuid.UUID
			_ = rows.Scan(&id)
			teams = append(teams, id)
		}
		rows.Close()
		for _, q := range []string{
			`DELETE FROM drive.nodes WHERE owner_id = ANY($1) AND parent_id IS NULL`,
			`DELETE FROM drive.changes WHERE owner_id = ANY($1)`,
		} {
			if _, err := e.pool.Exec(ctx, q, teams); err != nil {
				t.Errorf("cleanup team folders: %v", err)
			}
		}
	})
}

func (e *testEnv) group(name string, members ...uuid.UUID) uuid.UUID {
	id := uuid.New()
	e.sharing.mu.Lock()
	defer e.sharing.mu.Unlock()
	e.sharing.names[id] = name
	e.sharing.members[id] = members
	return id
}

func (e *testEnv) teamFolders(t *testing.T, user uuid.UUID) []Node {
	t.Helper()
	_, body := e.request(t, user, "GET", "/team-folders", nil)
	var list struct{ Nodes []Node }
	_ = json.Unmarshal(body, &list)
	return list.Nodes
}

func TestTeamFolders(t *testing.T) {
	e := newTestEnv(t)
	admin, editor, manager, outsider := e.person(t, "Beheerder"), e.person(t, "Bewerker"), e.person(t, "Regelaar"), e.person(t, "Buiten")
	e.cleanupTeamFolders(t, admin)
	ctx := context.Background()
	// Team folder names are unique system-wide: use one no other data has.
	name := "it-team-" + uuid.NewString()[:8]
	family := e.group("Familie", editor)
	leads := e.group("Leiding", manager)

	resp, body := e.request(t, admin, "POST", "/admin/team-folders", strings.NewReader(`{"name":"`+name+`"}`), "Content-Type", "application/json")
	var team TeamFolder
	_ = json.Unmarshal(body, &team)
	if resp.StatusCode != http.StatusCreated || team.Name != name {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	if code := e.status(t, admin, "POST", "/admin/team-folders", `{"name":"`+strings.ToUpper(name)+`"}`); code != http.StatusConflict {
		t.Errorf("duplicate name: %d", code)
	}
	// The test purges the team folder itself, so the cleanup above can no
	// longer find it: remove its change log here.
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM drive.changes WHERE owner_id = $1`, team.ID)
	})
	root := team.RootID.String()
	if len(e.teamFolders(t, editor)) != 0 {
		t.Error("team folder visible before any group was added")
	}
	for g, p := range map[uuid.UUID]string{family: "edit", leads: "manage"} {
		if code := e.status(t, admin, "PUT", "/admin/team-folders/"+team.ID.String()+"/groups/"+g.String(), `{"permission":"`+p+`"}`); code != http.StatusOK {
			t.Fatalf("add group: %d", code)
		}
	}
	if list := e.teamFolders(t, editor); len(list) != 1 || list[0].ID != team.RootID || list[0].Permission != authz.Edit {
		t.Fatalf("editor's team folders: %+v", list)
	}
	// Administrators manage team folders but do not see inside.
	if code := e.status(t, admin, "GET", "/folders/"+root, ""); code != http.StatusNotFound {
		t.Errorf("admin opens the team folder: %d", code)
	}

	// Files belong to the team folder and count towards its quota.
	code, file := e.upload(t, editor, root, "lijst.txt", []byte("melk"), false)
	if code != http.StatusCreated {
		t.Fatalf("upload: %d", code)
	}
	if u, _ := e.svc.Usage(ctx, team.ID); u.UsedBytes != 4 {
		t.Errorf("team usage = %d", u.UsedBytes)
	}
	if u, _ := e.svc.Usage(ctx, editor); u.UsedBytes != 0 {
		t.Errorf("editor usage = %d", u.UsedBytes)
	}
	if code := e.status(t, admin, "PUT", "/admin/team-folders/"+team.ID.String()+"/quota", `{"limit_bytes":5}`); code != http.StatusOK {
		t.Errorf("set quota: %d", code)
	}
	if code, _ := e.upload(t, editor, root, "groot.txt", []byte("te groot"), false); code != http.StatusInsufficientStorage {
		t.Errorf("upload over the team quota: %d", code)
	}
	_ = e.status(t, admin, "PUT", "/admin/team-folders/"+team.ID.String()+"/quota", `{"limit_bytes":null}`)

	resp, body = e.request(t, editor, "GET", "/folders/"+root, nil)
	var fv FolderView
	_ = json.Unmarshal(body, &fv)
	if fv.Team == nil || fv.Team.ID != team.ID || fv.Team.Name != name {
		t.Errorf("folder view team: %s", body)
	}

	// The root itself is managed by administrators only.
	for _, req := range []struct{ method, path, body string }{
		{"PATCH", "/nodes/" + root, `{"name":"Anders"}`},
		{"DELETE", "/nodes/" + root, ""},
		{"PUT", "/nodes/" + root + "/shares", `{"subject":{"type":"user","id":"` + outsider.String() + `"},"permission":"view"}`},
		{"DELETE", "/nodes/" + root + "/shares/group/" + family.String(), ""},
		{"PATCH", "/nodes/" + file.ID.String(), `{"parent_id":"root"}`},
	} {
		if code := e.status(t, manager, req.method, req.path, req.body); code != http.StatusUnprocessableEntity {
			t.Errorf("%s %s: %d, want 422", req.method, req.path, code)
		}
	}

	// Members with manage can share inside the team folder.
	sub := `{"parent_id":"` + root + `","name":"Recepten"}`
	resp, body = e.request(t, editor, "POST", "/folders", strings.NewReader(sub), "Content-Type", "application/json")
	var recipes Node
	_ = json.Unmarshal(body, &recipes)
	if code := e.share(t, manager, recipes.ID, outsider, authz.View); code != http.StatusOK {
		t.Errorf("manager shares a subfolder: %d", code)
	}
	shared, _ := e.svc.SharedWithMe(ctx, outsider)
	if len(shared) != 1 || shared[0].OwnerName != "Teammap "+name {
		t.Errorf("outsider's shared list: %+v", shared)
	}
	if list, _ := e.svc.SharedWithMe(ctx, editor); len(list) != 0 {
		t.Errorf("the team folder is listed as shared: %+v", list)
	}

	// The team folder has its own trash.
	if code := e.status(t, editor, "DELETE", "/nodes/"+file.ID.String(), ""); code != http.StatusNoContent {
		t.Fatalf("trash: %d", code)
	}
	trashPath := "/team-folders/" + team.ID.String() + "/trash"
	_, body = e.request(t, editor, "GET", trashPath, nil)
	if !strings.Contains(string(body), "lijst.txt") {
		t.Errorf("team trash: %s", body)
	}
	if trash, _ := e.svc.ListTrash(ctx, editor); len(trash) != 0 {
		t.Errorf("personal trash has team files: %+v", trash)
	}
	if code := e.status(t, outsider, "GET", trashPath, ""); code != http.StatusNotFound {
		t.Errorf("outsider sees the team trash: %d", code)
	}
	if code := e.status(t, editor, "POST", "/trash/"+file.ID.String()+"/restore", ""); code != http.StatusOK {
		t.Errorf("editor restores: %d", code)
	}
	_ = e.status(t, editor, "DELETE", "/nodes/"+file.ID.String(), "")
	if code := e.status(t, editor, "DELETE", "/trash/"+file.ID.String(), ""); code != http.StatusForbidden {
		t.Errorf("editor deletes for good: %d", code)
	}
	if code := e.status(t, editor, "DELETE", trashPath, ""); code != http.StatusForbidden {
		t.Errorf("editor empties the trash: %d", code)
	}
	if code := e.status(t, manager, "DELETE", trashPath, ""); code != http.StatusNoContent {
		t.Errorf("manager empties the trash: %d", code)
	}
	if list := e.teamFolders(t, editor); len(list) != 1 {
		t.Error("emptying the trash removed the team folder")
	}

	// The administrator's overview: name, groups and usage, no contents.
	if code := e.status(t, admin, "PATCH", "/admin/team-folders/"+team.ID.String(), `{"name":"`+name+`-2"}`); code != http.StatusOK {
		t.Errorf("rename: %d", code)
	}
	_, body = e.request(t, admin, "GET", "/admin/team-folders/", nil)
	var overview struct {
		TeamFolders []TeamFolder `json:"team_folders"`
	}
	_ = json.Unmarshal(body, &overview)
	i := slices.IndexFunc(overview.TeamFolders, func(f TeamFolder) bool { return f.ID == team.ID })
	if i < 0 || overview.TeamFolders[i].Name != name+"-2" || len(overview.TeamFolders[i].Groups) != 2 || overview.TeamFolders[i].DeletedAt != nil {
		t.Errorf("overview: %s", body)
	}

	// Removing a group ends its access.
	if code := e.status(t, admin, "DELETE", "/admin/team-folders/"+team.ID.String()+"/groups/"+family.String(), ""); code != http.StatusOK {
		t.Errorf("remove group: %d", code)
	}
	if code := e.status(t, editor, "GET", "/folders/"+root, ""); code != http.StatusNotFound {
		t.Errorf("after removing the group: %d", code)
	}

	// Deleting is two steps: to the trash (restorable), then for good.
	if code := e.status(t, admin, "POST", "/admin/team-folders/"+team.ID.String()+"/purge", ""); code != http.StatusUnprocessableEntity {
		t.Errorf("purge without deleting first: %d", code)
	}
	if code := e.status(t, admin, "DELETE", "/admin/team-folders/"+team.ID.String(), ""); code != http.StatusOK {
		t.Errorf("delete: %d", code)
	}
	if code := e.status(t, manager, "GET", "/folders/"+recipes.ID.String(), ""); code != http.StatusNotFound {
		t.Errorf("deleted team folder still open: %d", code)
	}
	if code := e.status(t, admin, "POST", "/admin/team-folders/"+team.ID.String()+"/restore", ""); code != http.StatusOK {
		t.Errorf("restore: %d", code)
	}
	if code := e.status(t, manager, "GET", "/folders/"+recipes.ID.String(), ""); code != http.StatusOK {
		t.Errorf("restored team folder: %d", code)
	}
	_ = e.status(t, admin, "DELETE", "/admin/team-folders/"+team.ID.String(), "")
	if code := e.status(t, admin, "POST", "/admin/team-folders/"+team.ID.String()+"/purge", ""); code != http.StatusNoContent {
		t.Errorf("purge: %d", code)
	}
	var left int
	_ = e.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM drive.nodes WHERE owner_id = $1) + (SELECT count(*) FROM drive.team_folders WHERE id = $1)`, team.ID).Scan(&left)
	if left != 0 {
		t.Errorf("%d rows left after purge", left)
	}
	if grants, _ := e.sharing.Grants(ctx, nodeType, team.RootID); len(grants) != 0 {
		t.Errorf("shares left after purge: %+v", grants)
	}
	if grants, _ := e.sharing.Grants(ctx, nodeType, recipes.ID); len(grants) != 0 {
		t.Errorf("subfolder shares left after purge: %+v", grants)
	}
	for _, ev := range []string{"teamfolder.created", "teamfolder.quota_changed", "teamfolder.deleted", "teamfolder.restored", "teamfolder.purged"} {
		if !slices.Contains(e.audited, ev) {
			t.Errorf("not audited: %s (got %v)", ev, e.audited)
		}
	}
}
