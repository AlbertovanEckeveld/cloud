//go:build integration

package drive

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
)

// dav sends a WebDAV request as a client would.
func (e *testEnv) dav(t *testing.T, user uuid.UUID, method, path string, body io.Reader, headers ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+"/dav"+path, body)
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
	return resp, string(b)
}

// chunked hides the length, like macOS Finder's uploads.
type chunked struct{ io.Reader }

func TestWebDAV(t *testing.T) {
	e := newTestEnv(t)
	u, friend := e.person(t, "Eigenaar"), e.person(t, "Vriend")
	ctx := context.Background()

	if resp, _ := e.dav(t, u, "MKCOL", "/files/Documenten", nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("MKCOL: %d", resp.StatusCode)
	}
	resp, _ := e.dav(t, u, "PUT", "/files/Documenten/notities.txt", strings.NewReader("versie 1"))
	if resp.StatusCode != http.StatusCreated || resp.Header.Get("ETag") == "" {
		t.Fatalf("PUT new: %d", resp.StatusCode)
	}
	etag1 := resp.Header.Get("ETag")
	// Unknown length (chunked), replacing the file: a new version.
	resp, _ = e.dav(t, u, "PUT", "/files/Documenten/notities.txt", chunked{strings.NewReader("versie 2, langer")})
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("ETag") == etag1 {
		t.Fatalf("PUT replace (chunked): %d %s", resp.StatusCode, resp.Header.Get("ETag"))
	}
	resp, body := e.dav(t, u, "GET", "/files/Documenten/notities.txt", nil)
	if resp.StatusCode != http.StatusOK || body != "versie 2, langer" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") ||
		resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("GET: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	view, _ := e.svc.List(ctx, u, nil)
	if len(view.Children) != 1 {
		t.Fatalf("browser view: %+v", view.Children)
	}
	if versions, _ := e.svc.Versions(ctx, u, mustChild(t, e, u, view.Children[0].ID, "notities.txt").ID); len(versions) != 2 {
		t.Errorf("versions = %d", len(versions))
	}

	resp, body = e.dav(t, u, "PROPFIND", "/files/Documenten", nil, "Depth", "1")
	if resp.StatusCode != http.StatusMultiStatus || !strings.Contains(body, "notities.txt") || !strings.Contains(body, "getetag") {
		t.Errorf("PROPFIND: %d %s", resp.StatusCode, body)
	}

	// MOVE (rename), COPY, DELETE (to the trash).
	if resp, _ := e.dav(t, u, "MOVE", "/files/Documenten/notities.txt", nil, "Destination", e.srv.URL+"/dav/files/Documenten/notitie.txt"); resp.StatusCode != http.StatusCreated {
		t.Errorf("MOVE: %d", resp.StatusCode)
	}
	if resp, _ := e.dav(t, u, "COPY", "/files/Documenten/notitie.txt", nil, "Destination", e.srv.URL+"/dav/files/kopie.txt"); resp.StatusCode != http.StatusCreated {
		t.Errorf("COPY: %d", resp.StatusCode)
	}
	if _, body := e.dav(t, u, "GET", "/files/kopie.txt", nil); body != "versie 2, langer" {
		t.Errorf("copied content %q", body)
	}
	if resp, _ := e.dav(t, u, "DELETE", "/files/kopie.txt", nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE: %d", resp.StatusCode)
	}
	if trash, _ := e.svc.ListTrash(ctx, u); len(trash) != 1 || trash[0].Name != "kopie.txt" {
		t.Errorf("trash after DELETE: %+v", trash)
	}

	// LOCK on a new name creates an empty file (as Office and Finder expect).
	lockBody := `<?xml version="1.0"?><D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype></D:lockinfo>`
	resp, _ = e.dav(t, u, "LOCK", "/files/Documenten/nieuw.docx", strings.NewReader(lockBody), "Timeout", "Second-60")
	token := resp.Header.Get("Lock-Token")
	if resp.StatusCode != http.StatusCreated || token == "" {
		t.Fatalf("LOCK: %d", resp.StatusCode)
	}
	if resp, _ := e.dav(t, u, "UNLOCK", "/files/Documenten/nieuw.docx", nil, "Lock-Token", token); resp.StatusCode != http.StatusNoContent {
		t.Errorf("UNLOCK: %d", resp.StatusCode)
	}

	// Quota: an upload over the limit is 507.
	limit := int64(30)
	_ = e.svc.SetQuota(ctx, u, &limit)
	if resp, _ := e.dav(t, u, "PUT", "/files/groot.bin", strings.NewReader(strings.Repeat("x", 40))); resp.StatusCode != http.StatusInsufficientStorage {
		t.Errorf("over quota: %d", resp.StatusCode)
	}
	_ = e.svc.SetQuota(ctx, u, nil)

	// Shared: the friend sees the share with view, can read but not write.
	docs := view.Children[0]
	_ = e.share(t, u, docs.ID, friend, authz.View)
	resp, body = e.dav(t, friend, "PROPFIND", "/shared/", nil, "Depth", "1")
	if resp.StatusCode != http.StatusMultiStatus || !strings.Contains(body, "Documenten") {
		t.Errorf("friend PROPFIND /shared: %d %s", resp.StatusCode, body)
	}
	if _, body := e.dav(t, friend, "GET", "/shared/Documenten/notitie.txt", nil); body != "versie 2, langer" {
		t.Errorf("friend GET: %q", body)
	}
	if resp, _ := e.dav(t, friend, "PUT", "/shared/Documenten/x.txt", strings.NewReader("x")); resp.StatusCode != http.StatusForbidden {
		t.Errorf("friend PUT with view: %d", resp.StatusCode)
	}
	if resp, _ := e.dav(t, friend, "DELETE", "/shared/Documenten/notitie.txt", nil); resp.StatusCode == http.StatusNoContent {
		t.Errorf("friend DELETE with view succeeded")
	}
	// Nothing of the owner's is visible in the friend's own files.
	if _, body := e.dav(t, friend, "PROPFIND", "/files/", nil, "Depth", "1"); strings.Contains(body, "Documenten") {
		t.Errorf("owner's folder in friend's files: %s", body)
	}
	if resp, _ := e.dav(t, friend, "GET", "/files/Documenten/notitie.txt", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("path guessing: %d", resp.StatusCode)
	}
}

func mustChild(t *testing.T, e *testEnv, user uuid.UUID, folder uuid.UUID, name string) Node {
	t.Helper()
	view, err := e.svc.List(context.Background(), user, &folder)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range view.Children {
		if n.Name == name {
			return n
		}
	}
	t.Fatalf("%s not found", name)
	return Node{}
}
