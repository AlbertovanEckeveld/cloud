//go:build integration

package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/AlbertovanEckeveld/cloud/desktop/internal/auth"
	"github.com/AlbertovanEckeveld/cloud/desktop/internal/syncer"
)

// TestRealServer signs in to a running platform and syncs with it. It is
// started by the browser end-to-end test, which opens the sign-in URL
// printed as "OPEN <url>" in a browser that is already signed in.
func TestRealServer(t *testing.T) {
	server := os.Getenv("CLOUD_E2E_SERVER")
	if server == "" {
		t.Skip("CLOUD_E2E_SERVER not set")
	}
	keyring.MockInit() // never touch the real keyring in a test
	ctx := context.Background()

	sess, err := auth.SignIn(ctx, server, true, func(u string) { fmt.Println("OPEN " + u) })
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	client, err := syncer.NewClient(server, true, sess)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := client.Roots(ctx)
	if err != nil || len(roots) == 0 || roots[0].ID != "mine" {
		t.Fatalf("roots: %+v %v", roots, err)
	}
	state, err := syncer.OpenState(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	engine := syncer.NewEngine(client, state, "e2e", slog.New(slog.NewTextHandler(io.Discard, nil)))
	dir := t.TempDir()

	rep, err := engine.Sync(ctx, "mine", dir, false)
	if err != nil || rep.Downloaded == 0 || len(rep.Failed) > 0 {
		t.Fatalf("first sync: %+v %v", rep, err)
	}
	if info, err := os.Stat(filepath.Join(dir, "Projecten")); err != nil || !info.IsDir() {
		t.Errorf("server folder not synced: %v", err)
	}

	// Local -> server.
	_ = os.WriteFile(filepath.Join(dir, "van-desktop.txt"), []byte("geschreven op de desktop"), 0o644)
	if rep, err = engine.Sync(ctx, "mine", dir, false); err != nil || rep.Uploaded != 1 {
		t.Fatalf("upload: %+v %v", rep, err)
	}
	tree, _ := client.Tree(ctx, "mine")
	if !hasItem(tree, "van-desktop.txt") {
		t.Error("upload not on the server")
	}
	// The server's activity shows this upload as made by the desktop app.
	tok, _ := sess.Token(ctx)
	req, _ := http.NewRequestWithContext(ctx, "GET", server+"/api/v1/drive/activity", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	if resp, err := http.DefaultClient.Do(req); err != nil {
		t.Error(err)
	} else {
		var act struct {
			Items []struct{ Action, Name, Via string }
		}
		_ = json.NewDecoder(resp.Body).Decode(&act)
		resp.Body.Close()
		if len(act.Items) == 0 || act.Items[0].Name != "van-desktop.txt" || act.Items[0].Via != "desktop" {
			t.Errorf("activity on the server: %+v", act.Items)
		}
	}
	// Server -> local (another device uploads).
	if _, err := client.Upload(ctx, "root", "van-server.txt", 7, strings.NewReader("server!")); err != nil {
		t.Fatal(err)
	}
	if rep, err = engine.Sync(ctx, "mine", dir, false); err != nil || rep.Downloaded != 1 {
		t.Fatalf("download: %+v %v", rep, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "van-server.txt")); string(b) != "server!" {
		t.Errorf("downloaded content %q", b)
	}
	// Nothing changed: an idle run does nothing, also not echo its own upload.
	if rep, err = engine.Sync(ctx, "mine", dir, false); err != nil || rep.Uploaded+rep.Downloaded+rep.Conflicts != 0 {
		t.Errorf("idle run: %+v %v", rep, err)
	}
	// Local delete -> server trash.
	_ = os.Remove(filepath.Join(dir, "van-desktop.txt"))
	if rep, err = engine.Sync(ctx, "mine", dir, false); err != nil || rep.TrashedRemote != 1 {
		t.Errorf("delete: %+v %v", rep, err)
	}

	// Signing out revokes the device on the server: even the access token
	// issued before is refused (not just forgotten by the app).
	before, err := sess.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.SignOut(ctx); err != nil {
		t.Fatal(err)
	}
	old, _ := syncer.NewClient(server, true, staticToken(before))
	if _, err := old.Roots(ctx); !errors.Is(err, syncer.ErrUnauthorized) {
		t.Errorf("old access token after signing out: %v", err)
	}
	fmt.Println("RESULT ok: signed in, synced both ways, deleted, signed out")
}

type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

func hasItem(p syncer.Page, name string) bool {
	for _, it := range p.Items {
		if it.Name == name {
			return true
		}
	}
	return false
}
