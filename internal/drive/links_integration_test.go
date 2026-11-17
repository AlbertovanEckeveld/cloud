//go:build integration

package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func (e *testEnv) openLink(t *testing.T, body map[string]string) (int, PublicView) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(e.srv.URL+"/api/v1/drive/public/links/open", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var v PublicView
	_ = json.NewDecoder(resp.Body).Decode(&v)
	return resp.StatusCode, v
}

func (e *testEnv) linkDownload(t *testing.T, form url.Values) (int, string, http.Header) {
	t.Helper()
	resp, err := http.PostForm(e.srv.URL+"/api/v1/drive/public/links/download", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestShareLinks(t *testing.T) {
	e := newTestEnv(t)
	owner, other := e.person(t, "Eigenaar"), e.person(t, "Ander")
	ctx := context.Background()
	album, _ := e.svc.CreateFolder(ctx, owner, nil, "Album")
	day, _ := e.svc.CreateFolder(ctx, owner, &album.ID, "Dag 1")
	private, _ := e.svc.CreateFolder(ctx, owner, nil, "Privé")
	_, photo := e.upload(t, owner, day.ID.String(), "foto.jpg", []byte("jpeg"), false)
	_, secret := e.upload(t, owner, private.ID.String(), "geheim.txt", []byte("geheim"), false)

	if code := e.status(t, other, "POST", "/nodes/"+album.ID.String()+"/links", `{}`); code != http.StatusNotFound {
		t.Errorf("stranger creates a link: %d", code)
	}
	if code := e.status(t, owner, "POST", "/nodes/"+album.ID.String()+"/links", `{"password":"kort"}`); code != http.StatusUnprocessableEntity {
		t.Errorf("short password: %d", code)
	}
	expires := e.clock.Add(48 * time.Hour).UTC().Format(time.RFC3339)
	resp, body := e.request(t, owner, "POST", "/nodes/"+album.ID.String()+"/links",
		strings.NewReader(`{"password":"vakantie2026","expires_at":"`+expires+`"}`), "Content-Type", "application/json")
	var link Link
	_ = json.Unmarshal(body, &link)
	if resp.StatusCode != http.StatusCreated || len(link.Token) < 40 || !link.Password {
		t.Fatalf("create link: %d %s", resp.StatusCode, body)
	}
	var stored []byte
	_ = e.pool.QueryRow(ctx, `SELECT token_hash FROM drive.links WHERE id = $1`, link.ID).Scan(&stored)
	if bytes.Contains(stored, []byte(link.Token)) || len(stored) != 32 {
		t.Error("token stored in plain text")
	}
	_, body = e.request(t, owner, "GET", "/nodes/"+album.ID.String()+"/links", nil)
	if strings.Contains(string(body), link.Token) {
		t.Error("link list reveals the token")
	}

	if code, _ := e.openLink(t, map[string]string{"token": "verkeerd"}); code != http.StatusNotFound {
		t.Errorf("unknown token: %d", code)
	}
	if code, _ := e.openLink(t, map[string]string{"token": link.Token}); code != http.StatusUnauthorized {
		t.Errorf("no password: %d", code)
	}
	if code, _ := e.openLink(t, map[string]string{"token": link.Token, "password": "fout"}); code != http.StatusUnauthorized {
		t.Errorf("wrong password: %d", code)
	}
	ok := map[string]string{"token": link.Token, "password": "vakantie2026"}
	code, view := e.openLink(t, ok)
	if code != http.StatusOK || view.Node.ID != album.ID || len(view.Children) != 1 || view.Node.ParentID != nil {
		t.Fatalf("open: %d %+v", code, view)
	}
	ok["folder_id"] = day.ID.String()
	if code, view = e.openLink(t, ok); code != http.StatusOK || len(view.Path) != 2 || view.Path[0].ID != album.ID {
		t.Errorf("browse into subfolder: %d %+v", code, view.Path)
	}
	ok["folder_id"] = private.ID.String()
	if code, _ := e.openLink(t, ok); code != http.StatusNotFound {
		t.Errorf("folder outside the link: %d", code)
	}

	form := url.Values{"token": {link.Token}, "password": {"vakantie2026"}, "file_id": {photo.ID.String()}}
	code, content, h := e.linkDownload(t, form)
	if code != http.StatusOK || content != "jpeg" || !strings.HasPrefix(h.Get("Content-Disposition"), "attachment") ||
		!strings.Contains(h.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("download: %d %q %v", code, content, h)
	}
	form.Set("file_id", secret.ID.String())
	if code, _, _ := e.linkDownload(t, form); code != http.StatusNotFound {
		t.Errorf("file outside the link: %d", code)
	}
	var downloads int
	_ = e.pool.QueryRow(ctx, `SELECT downloads FROM drive.links WHERE id = $1`, link.ID).Scan(&downloads)
	if downloads != 1 {
		t.Errorf("downloads = %d", downloads)
	}

	// Ten wrong passwords lock the link, also for the right one.
	for range linkMaxFailures {
		e.openLink(t, map[string]string{"token": link.Token, "password": "fout"})
	}
	delete(ok, "folder_id")
	if code, _ := e.openLink(t, ok); code != http.StatusTooManyRequests {
		t.Errorf("after 10 failures: %d", code)
	}
	e.clock = e.clock.Add(linkLockout + time.Minute)
	if code, _ := e.openLink(t, ok); code != http.StatusOK {
		t.Errorf("after the lockout: %d", code)
	}

	// Expired, trashed and revoked links look like links that never existed.
	e.clock = e.clock.Add(48 * time.Hour)
	if code, _ := e.openLink(t, ok); code != http.StatusNotFound {
		t.Errorf("expired: %d", code)
	}
	e.clock = time.Now()
	resp, body = e.request(t, owner, "POST", "/nodes/"+photo.ID.String()+"/links", strings.NewReader(`{}`), "Content-Type", "application/json")
	var open Link
	_ = json.Unmarshal(body, &open)
	if code, view := e.openLink(t, map[string]string{"token": open.Token}); code != http.StatusOK || view.Node.Name != "foto.jpg" || view.Folder != nil {
		t.Errorf("file link without password: %d %+v", code, view)
	}
	_ = e.svc.Trash(ctx, owner, day.ID)
	if code, _ := e.openLink(t, map[string]string{"token": open.Token}); code != http.StatusNotFound {
		t.Errorf("trashed: %d", code)
	}
	_, _ = e.svc.Restore(ctx, owner, day.ID)
	if code := e.status(t, other, "DELETE", "/links/"+open.ID.String(), ""); code != http.StatusNotFound {
		t.Errorf("stranger revokes: %d", code)
	}
	if code := e.status(t, owner, "DELETE", "/links/"+open.ID.String(), ""); code != http.StatusNoContent {
		t.Errorf("revoke: %d", code)
	}
	if code, _ := e.openLink(t, map[string]string{"token": open.Token}); code != http.StatusNotFound {
		t.Errorf("revoked: %d", code)
	}
}
