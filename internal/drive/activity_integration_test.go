//go:build integration

package drive

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
)

func (e *testEnv) activity(t *testing.T, user uuid.UUID) []ActivityItem {
	t.Helper()
	resp, body := e.request(t, user, "GET", "/activity", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("activity: %d %s", resp.StatusCode, body)
	}
	var out struct{ Items []ActivityItem }
	_ = json.Unmarshal(body, &out)
	return out.Items
}

func lines(items []ActivityItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Action + " " + it.Name + " by " + it.ActorName + " via " + it.Via
	}
	return out
}

func TestActivity(t *testing.T) {
	e := newTestEnv(t)
	owner, editor := e.person(t, "Eigenaar"), e.person(t, "Bewerker")
	ctx := context.Background()
	shared, _ := e.svc.CreateFolder(ctx, owner, nil, "Samen")
	_ = e.share(t, owner, shared.ID, editor, authz.Edit)
	e.upload(t, owner, "root", "privé.txt", []byte("x"), false)
	e.upload(t, editor, shared.ID.String(), "verslag.txt", []byte("v1"), false)
	e.dav(t, owner, "PUT", "/files/Samen/via-dav.txt", strings.NewReader("dav"))
	_, file := e.upload(t, owner, shared.ID.String(), "weg.txt", []byte("x"), false)
	_ = e.status(t, owner, "DELETE", "/nodes/"+file.ID.String(), "")

	got := lines(e.activity(t, owner))
	want := []string{
		"trashed weg.txt by Eigenaar via web",
		"created weg.txt by Eigenaar via web",
		"created via-dav.txt by Eigenaar via webdav",
		"created verslag.txt by Bewerker via web",
		"created privé.txt by Eigenaar via web",
	}
	for i, w := range want {
		if i >= len(got) || got[i] != w {
			t.Fatalf("owner's activity:\n%s\nwant (first %d):\n%s", strings.Join(got, "\n"), len(want), strings.Join(want, "\n"))
		}
	}
	// The editor sees what happens in the shared folder only, and not what
	// went to the owner's trash.
	for _, l := range lines(e.activity(t, editor)) {
		if strings.Contains(l, "privé.txt") || strings.Contains(l, "weg.txt") {
			t.Errorf("editor sees %q", l)
		}
	}
	if items := e.activity(t, editor); len(items) == 0 || items[0].Name != "via-dav.txt" {
		t.Errorf("editor's activity: %v", lines(items))
	}
}
