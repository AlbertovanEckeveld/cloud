//go:build integration

package drive

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
)

func TestVersionHistory(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	owner, editor, viewer := e.person(t, "Eigenaar"), e.person(t, "Bewerker"), e.person(t, "Kijker")
	contents := []string{"een", "versie twee", "de derde versie"}
	var file Node
	for i, c := range contents {
		e.clock = e.clock.Add(time.Second)
		_, file = e.upload(t, owner, "root", "plan.txt", []byte(c), i > 0)
	}
	_ = e.share(t, owner, file.ID, editor, authz.Edit)
	_ = e.share(t, owner, file.ID, viewer, authz.View)
	base := "/files/" + file.ID.String() + "/versions"

	list := func() []Version {
		_, body := e.request(t, owner, "GET", base, nil)
		var out struct{ Versions []Version }
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		return out.Versions
	}
	vs := list()
	if len(vs) != 3 || vs[2].CreatedVia == nil || *vs[2].CreatedVia != ViaWeb {
		t.Fatalf("versions: %+v", vs)
	}
	first, second := vs[2].ID.String(), vs[1].ID.String()

	if u, _ := e.svc.Usage(ctx, owner); u.OldVersionsBytes != int64(len(contents[0])+len(contents[1])) {
		t.Errorf("old versions use %d bytes", u.OldVersionsBytes)
	}

	// An old version in the viewer: inline, sandboxed, its own content.
	resp, body := e.request(t, viewer, "GET", base+"/"+first+"/content?inline=1", nil)
	if resp.StatusCode != http.StatusOK || string(body) != "een" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "inline") ||
		!strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("inline old version: %d %q %v", resp.StatusCode, body, resp.Header)
	}

	// Naming needs edit; names are 1 to 80 characters.
	label := func(user uuid.UUID, id, body string) int {
		return e.status(t, user, "PATCH", base+"/"+id, body)
	}
	if st := label(viewer, first, `{"label":"ingeleverd"}`); st != http.StatusForbidden {
		t.Errorf("viewer names a version: %d", st)
	}
	for _, bad := range []string{`{"label":""}`, `{"label":"   "}`, `{"label":"` + strings.Repeat("x", 81) + `"}`} {
		if st := label(editor, first, bad); st != http.StatusUnprocessableEntity {
			t.Errorf("label %s: %d", bad, st)
		}
	}
	if st := label(editor, first, `{"label":"  ingeleverd "}`); st != http.StatusNoContent {
		t.Fatalf("label: %d", st)
	}
	if v := list()[2]; v.Label == nil || *v.Label != "ingeleverd" {
		t.Fatalf("label not stored: %+v", v)
	}

	// Cleaning up needs manage and keeps the current and named versions.
	if st := e.status(t, editor, "POST", base+"/prune", ""); st != http.StatusForbidden {
		t.Errorf("editor prunes: %d", st)
	}
	resp, body = e.request(t, owner, "POST", base+"/prune", nil)
	var pruned Pruned
	_ = json.Unmarshal(body, &pruned)
	if resp.StatusCode != http.StatusOK || pruned.Versions != 1 || pruned.Bytes != int64(len(contents[1])) {
		t.Fatalf("prune: %d %s", resp.StatusCode, body)
	}
	if vs := list(); len(vs) != 2 || !vs[0].Current || vs[1].ID.String() != first {
		t.Fatalf("after prune: %+v", vs)
	}

	// Without its name, the old version goes too.
	if st := label(editor, first, `{"label":null}`); st != http.StatusNoContent {
		t.Fatalf("remove label: %d", st)
	}
	if _, body := e.request(t, owner, "POST", base+"/prune", nil); !strings.Contains(string(body), `"versions":1`) {
		t.Fatalf("second prune: %s", body)
	}
	if vs := list(); len(vs) != 1 || !vs[0].Current {
		t.Fatalf("after second prune: %+v", vs)
	}
	waitForObjectDeletions(t, e, first, second)
}

// waitForObjectDeletions waits until the background deletion of these
// versions' objects has run, so the test leaves nothing behind in S3.
func waitForObjectDeletions(t *testing.T, e *testEnv, versionIDs ...string) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(10 * time.Second)
	for _, id := range versionIDs {
		key := "drive/" + id
		for {
			var pending int
			_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM drive.object_deletions WHERE object_key = $1`, key).Scan(&pending)
			if pending == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("object %s not deleted", key)
			}
			time.Sleep(50 * time.Millisecond)
		}
		if _, _, err := e.objects.Open(ctx, key); err == nil {
			t.Errorf("object %s still exists", key)
		}
	}
}
