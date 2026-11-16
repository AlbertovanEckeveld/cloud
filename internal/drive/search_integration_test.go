//go:build integration

package drive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
)

func (e *testEnv) search(t *testing.T, user uuid.UUID, q string) []SearchResult {
	t.Helper()
	resp, body := e.request(t, user, "GET", "/search?q="+url.QueryEscape(q), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("search %q: %d %s", q, resp.StatusCode, body)
	}
	var out struct{ Results []SearchResult }
	_ = json.Unmarshal(body, &out)
	return out.Results
}

func names(rs []SearchResult) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name + "@" + r.Location
	}
	return out
}

func TestSearch(t *testing.T) {
	e := newTestEnv(t)
	owner, friend, stranger := e.person(t, "Eigenaar"), e.person(t, "Vriend"), e.person(t, "Vreemde")
	ctx := context.Background()
	work, _ := e.svc.CreateFolder(ctx, owner, nil, "Werk")
	private, _ := e.svc.CreateFolder(ctx, owner, nil, "Privé")
	e.upload(t, owner, work.ID.String(), "Jaarrapport 2026.pdf", []byte("a"), false)
	e.upload(t, owner, "root", "rapport.txt", []byte("b"), false)
	e.upload(t, owner, private.ID.String(), "rapport-geheim.txt", []byte("c"), false)
	_, gone := e.upload(t, owner, work.ID.String(), "oud rapport.doc", []byte("d"), false)
	_ = e.svc.Trash(ctx, owner, gone.ID)
	e.upload(t, owner, "root", "100%_echt.txt", []byte("e"), false)

	got := e.search(t, owner, "RAPP")
	if want := []string{"rapport.txt@Mijn bestanden", "rapport-geheim.txt@Privé", "Jaarrapport 2026.pdf@Werk"}; !slices.Equal(names(got), want) {
		t.Errorf("owner: %v, want %v", names(got), want)
	}
	if got := e.search(t, owner, "%_"); len(got) != 1 || got[0].Name != "100%_echt.txt" {
		t.Errorf("wildcards are literal: %v", names(got))
	}
	if got := e.search(t, owner, "r"); len(got) != 0 {
		t.Errorf("one letter searches: %v", names(got))
	}

	// A friend finds only what is shared, located without the owner's folders.
	_ = e.share(t, owner, work.ID, friend, authz.View)
	got = e.search(t, friend, "rapport")
	if len(got) != 1 || got[0].Name != "Jaarrapport 2026.pdf" || got[0].Location != "Werk" || got[0].Permission != authz.View {
		t.Errorf("friend: %+v", got)
	}
	if got := e.search(t, friend, "werk"); len(got) != 1 || got[0].Location != "Gedeeld met mij" || got[0].ParentID != nil {
		t.Errorf("friend finds the share itself: %+v", got)
	}
	if got := e.search(t, stranger, "rapport"); len(got) != 0 {
		t.Errorf("stranger: %v", names(got))
	}
	// Trashing the share hides it from search too.
	_ = e.svc.Trash(ctx, owner, work.ID)
	if got := e.search(t, friend, "rapport"); len(got) != 0 {
		t.Errorf("trashed share still found: %v", names(got))
	}
}
