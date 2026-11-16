//go:build integration

package drive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
)

// fakeCollabora serves a discovery document like Collabora's.
func fakeCollabora(t *testing.T) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<wopi-discovery><net-zone name="external-http">
			<app name="writer"><action name="edit" ext="docx" urlsrc="%[1]s/browser/x/cool.html?"/></app>
			<app name="calc"><action name="view" ext="csv" urlsrc="%[1]s/browser/x/cool.html?"/></app>
		</net-zone></wopi-discovery>`, srv.URL)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (e *testEnv) wopiCall(t *testing.T, method, path, token string, body io.Reader, headers ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+"/wopi"+path+"?access_token="+url.QueryEscape(token), body)
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

func TestWOPI(t *testing.T) {
	e := newTestEnv(t)
	col := fakeCollabora(t)
	colURL, _ := url.Parse(col.URL)
	pubURL, _ := url.Parse(e.srv.URL)
	e.svc.office = &office{cfg: Office{URL: colURL, PublicURL: pubURL, Key: [32]byte{7}}}

	owner, viewer := e.person(t, "Eigenaar"), e.person(t, "Lezer")
	ctx := context.Background()
	_, doc := e.upload(t, owner, "root", "plan.docx", []byte("versie 1"), false)
	_, other := e.upload(t, owner, "root", "ander.docx", []byte("x"), false)
	_, csv := e.upload(t, owner, "root", "data.csv", []byte("a,b"), false)
	_, zip := e.upload(t, owner, "root", "archief.zip", []byte("PK"), false)
	_ = e.share(t, owner, doc.ID, viewer, authz.View)

	sess, err := e.svc.OpenInOffice(ctx, owner, doc.ID)
	if err != nil || !sess.Edit || !strings.HasPrefix(sess.URL, col.URL+"/browser/x/cool.html?") || !strings.Contains(sess.URL, "WOPISrc="+url.QueryEscape(e.srv.URL+"/wopi/files/"+doc.ID.String())) {
		t.Fatalf("open: %+v %v", sess, err)
	}
	if s, _ := e.svc.OpenInOffice(ctx, viewer, doc.ID); s.Edit {
		t.Error("viewer got an edit session")
	}
	if s, _ := e.svc.OpenInOffice(ctx, owner, csv.ID); s.Edit {
		t.Error("view-only type got an edit session")
	}
	if _, err := e.svc.OpenInOffice(ctx, owner, zip.ID); err != ErrNoOffice {
		t.Errorf("zip: %v", err)
	}
	if _, err := e.svc.OpenInOffice(ctx, viewer, other.ID); err != ErrNotFound {
		t.Errorf("unshared file: %v", err)
	}

	// CheckFileInfo and GetFile, as Collabora calls them.
	base := "/files/" + doc.ID.String()
	resp, body := e.wopiCall(t, "GET", base, sess.AccessToken, nil)
	var info map[string]any
	_ = json.Unmarshal([]byte(body), &info)
	if resp.StatusCode != http.StatusOK || info["BaseFileName"] != "plan.docx" || info["UserCanWrite"] != true || info["UserFriendlyName"] != "Eigenaar" {
		t.Fatalf("CheckFileInfo: %d %s", resp.StatusCode, body)
	}
	if _, body := e.wopiCall(t, "GET", base+"/contents", sess.AccessToken, nil); body != "versie 1" {
		t.Errorf("GetFile: %q", body)
	}
	// The token is for one file only, and must be intact.
	if resp, _ := e.wopiCall(t, "GET", "/files/"+other.ID.String(), sess.AccessToken, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("token on another file: %d", resp.StatusCode)
	}
	if resp, _ := e.wopiCall(t, "GET", base, sess.AccessToken+"x", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("tampered token: %d", resp.StatusCode)
	}

	// PutFile saves a new version, made by the user.
	stamp := info["LastModifiedTime"].(string)
	e.clock = e.clock.Add(time.Minute) // the user edits for a while
	resp, body = e.wopiCall(t, "POST", base+"/contents", sess.AccessToken, strings.NewReader("versie 2"),
		"X-WOPI-Override", "PUT", "X-COOL-WOPI-Timestamp", stamp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "LastModifiedTime") {
		t.Fatalf("PutFile: %d %s", resp.StatusCode, body)
	}
	if versions, _ := e.svc.Versions(ctx, owner, doc.ID); len(versions) != 2 || versions[0].CreatedByName != "Eigenaar" {
		t.Errorf("versions after save: %+v", versions)
	}
	// Saving on top of a version the editor did not load is a conflict.
	resp, body = e.wopiCall(t, "POST", base+"/contents", sess.AccessToken, strings.NewReader("oud"),
		"X-WOPI-Override", "PUT", "X-COOL-WOPI-Timestamp", stamp)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "1010") {
		t.Errorf("stale save: %d %s", resp.StatusCode, body)
	}
	// A read-only session cannot save.
	ro, _ := e.svc.OpenInOffice(ctx, viewer, doc.ID)
	if resp, _ := e.wopiCall(t, "POST", base+"/contents", ro.AccessToken, strings.NewReader("hack"), "X-WOPI-Override", "PUT"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("viewer save: %d", resp.StatusCode)
	}
	// Losing access ends the session, even with a valid token.
	_ = e.status(t, owner, "DELETE", "/nodes/"+doc.ID.String()+"/shares/user/"+viewer.String(), "")
	if resp, _ := e.wopiCall(t, "GET", base, ro.AccessToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("after unsharing: %d", resp.StatusCode)
	}
}

func TestWOPIThroughPublicLink(t *testing.T) {
	e := newTestEnv(t)
	col := fakeCollabora(t)
	colURL, _ := url.Parse(col.URL)
	pubURL, _ := url.Parse(e.srv.URL)
	e.svc.office = &office{cfg: Office{URL: colURL, PublicURL: pubURL, Key: [32]byte{7}}}
	owner := e.person(t, "Eigenaar")
	ctx := context.Background()
	folder, _ := e.svc.CreateFolder(ctx, owner, nil, "Gedeeld")
	_, doc := e.upload(t, owner, folder.ID.String(), "verslag.docx", []byte("inhoud"), false)
	_, private := e.upload(t, owner, "root", "geheim.docx", []byte("geheim"), false)
	link, err := e.svc.CreateLink(ctx, owner, folder.ID, nil, "wachtwoord1")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := e.svc.OpenLinkInOffice(ctx, link.Token, "fout", doc.ID); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("wrong password: %v", err)
	}
	if _, err := e.svc.OpenLinkInOffice(ctx, link.Token, "wachtwoord1", private.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("file outside the link: %v", err)
	}
	sess, err := e.svc.OpenLinkInOffice(ctx, link.Token, "wachtwoord1", doc.ID)
	if err != nil || sess.Edit {
		t.Fatalf("open through link: %+v %v", sess, err)
	}
	base := "/files/" + doc.ID.String()
	_, body := e.wopiCall(t, "GET", base, sess.AccessToken, nil)
	var info map[string]any
	_ = json.Unmarshal([]byte(body), &info)
	if info["UserCanWrite"] != false || info["UserFriendlyName"] != "Gast" {
		t.Errorf("CheckFileInfo for a link: %s", body)
	}
	if _, body := e.wopiCall(t, "GET", base+"/contents", sess.AccessToken, nil); body != "inhoud" {
		t.Errorf("GetFile: %q", body)
	}
	if resp, _ := e.wopiCall(t, "POST", base+"/contents", sess.AccessToken, strings.NewReader("vandalisme"), "X-WOPI-Override", "PUT"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("save through a link: %d", resp.StatusCode)
	}
	// Revoking the link ends the open session.
	if err := e.svc.DeleteLink(ctx, owner, link.ID); err != nil {
		t.Fatal(err)
	}
	if resp, _ := e.wopiCall(t, "GET", base, sess.AccessToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("after revoking the link: %d", resp.StatusCode)
	}
}
