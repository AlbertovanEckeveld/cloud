//go:build integration

package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestDesktopSignIn runs the desktop app's sign-in: authorization code with
// PKCE, redirect to a loopback address on a port of the app's choosing
// (RFC 8252 §7.3), then the access token on the drive API.
func TestDesktopSignIn(t *testing.T) {
	o := newOIDCEnv(t)
	u, sess := o.newAdmin(t)
	o.signIn(t, u)
	loopback := "http://127.0.0.1:53817/callback" // any port

	f := newFlow(DesktopClientID)
	f.extra.Set("redirect_uri", loopback)
	loc, status, code := o.approve(t, requestID(t, o.authorize(t, f)))
	if status != http.StatusOK || loc == nil || loc.Host != "127.0.0.1:53817" || loc.Query().Get("code") == "" {
		t.Fatalf("approve: %d %s %v", status, code, loc)
	}
	form := codeExchange(loc.Query().Get("code"), f)
	form.Set("redirect_uri", loopback)
	st, tk := o.token(t, DesktopClientID, "", form)
	if st != http.StatusOK || tk.AccessToken == "" || tk.RefreshToken == "" {
		t.Fatalf("token: %d %+v", st, tk)
	}

	// The access token works on routes guarded by RequireUserOrDevice...
	var seen Principal
	files := o.svc.RequireUserOrDevice(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = PrincipalFrom(r.Context())
	}))
	call := func(h http.Handler, token string) int {
		r := httptest.NewRequest("GET", "/api/v1/drive/folders/root", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if code := call(files, tk.AccessToken); code != http.StatusOK || seen.User.ID != u.ID || !seen.Device {
		t.Fatalf("device token: %d %+v", code, seen)
	}
	// ...but never for administration, even for an administrator.
	admin := o.svc.RequireUserOrDevice(RequirePermission(PermUsersView)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
	if code := call(admin, tk.AccessToken); code != http.StatusForbidden {
		t.Errorf("device token on admin route: %d", code)
	}
	// Tampered tokens and tokens of other apps are refused.
	if code := call(files, tk.AccessToken[:len(tk.AccessToken)-2]+"AA"); code != http.StatusUnauthorized {
		t.Errorf("tampered token: %d", code)
	}
	other, _ := o.newClient(t, false, nil)
	g := newFlow(other.ID)
	loc, _, _ = o.approve(t, requestID(t, o.authorize(t, g)))
	_, otherTk := o.token(t, other.ID, "", codeExchange(loc.Query().Get("code"), g))
	if code := call(files, otherTk.AccessToken); code != http.StatusUnauthorized {
		t.Errorf("another app's token on the drive API: %d", code)
	}

	// Revoking the refresh token (RFC 7009, what "sign out" in the app does)
	// ends it and every access token issued with it.
	f2 := newFlow(DesktopClientID)
	f2.extra.Set("redirect_uri", loopback)
	loc, _, _ = o.approve(t, requestID(t, o.authorize(t, f2)))
	form2 := codeExchange(loc.Query().Get("code"), f2)
	form2.Set("redirect_uri", loopback)
	_, tk2 := o.token(t, DesktopClientID, "", form2)
	req, _ := http.NewRequest("POST", o.base+"/oidc/revoke", strings.NewReader(url.Values{
		"token": {tk2.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {DesktopClientID}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if resp, err := o.plain.Do(req); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: %v %v", resp, err)
	}
	if code := call(files, tk2.AccessToken); code != http.StatusUnauthorized {
		t.Errorf("access token after revoking its refresh token: %d", code)
	}
	if st, _ := o.token(t, DesktopClientID, "", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tk2.RefreshToken}}); st == http.StatusOK {
		t.Error("revoked refresh token still works")
	}

	// Revoking the app on the account page ends the device's access.
	if err := o.svc.RevokeApp(context.Background(), o.client, sess, DesktopClientID); err != nil {
		t.Fatal(err)
	}
	if code := call(files, tk.AccessToken); code != http.StatusUnauthorized {
		t.Errorf("after revoking: %d", code)
	}
}

// linkDesktop signs the desktop app in for the browser's user and returns
// its tokens.
func (o *oidcEnv) linkDesktop(t *testing.T) tokens {
	t.Helper()
	loopback := "http://127.0.0.1:53817/callback"
	f := newFlow(DesktopClientID)
	f.extra.Set("redirect_uri", loopback)
	loc, status, code := o.approve(t, requestID(t, o.authorize(t, f)))
	if status != http.StatusOK || loc == nil {
		t.Fatalf("approve: %d %s", status, code)
	}
	form := codeExchange(loc.Query().Get("code"), f)
	form.Set("redirect_uri", loopback)
	st, tk := o.token(t, DesktopClientID, "", form)
	if st != http.StatusOK || tk.RefreshToken == "" {
		t.Fatalf("token: %d %+v", st, tk)
	}
	return tk
}

// reportDevice calls PUT /api/v1/device as the app (bearer) or, with an
// empty token, as the browser session.
func (o *oidcEnv) reportDevice(t *testing.T, bearer, name, platform string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "platform": platform})
	req, _ := http.NewRequest(http.MethodPut, o.base+"/api/v1/device", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	client := o.browser
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
		client = o.plain
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestLinkedDevices(t *testing.T) {
	o := newOIDCEnv(t)
	ctx := context.Background()
	u, sess := o.newAdmin(t)
	o.signIn(t, u)
	tk := o.linkDesktop(t)

	// The app names itself; nonsense and browser sessions are refused.
	if st := o.reportDevice(t, tk.AccessToken, "  werk-laptop  ", "linux"); st != http.StatusNoContent {
		t.Fatalf("report: %d", st)
	}
	for _, bad := range [][2]string{{"", "linux"}, {"evil\x1b[31m", "linux"}, {strings.Repeat("x", 65), "linux"}, {"ok", "plan9"}} {
		if st := o.reportDevice(t, tk.AccessToken, bad[0], bad[1]); st != http.StatusUnprocessableEntity {
			t.Errorf("report %q/%q: %d, want 422", bad[0], bad[1], st)
		}
	}
	if st := o.reportDevice(t, "", "browser", "linux"); st != http.StatusForbidden {
		t.Errorf("report from a browser session: %d, want 403", st)
	}

	devices, err := o.svc.Devices(ctx, u.ID)
	if err != nil || len(devices) != 1 {
		t.Fatalf("devices: %+v %v", devices, err)
	}
	d := devices[0]
	if d.Name == nil || *d.Name != "werk-laptop" || d.Platform == nil || *d.Platform != "linux" || d.LastSeenAt == nil {
		t.Fatalf("device: %+v", d)
	}

	// A refresh keeps the same device; the app is not listed as an app.
	st, renewed := o.token(t, DesktopClientID, "", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tk.RefreshToken}})
	if st != http.StatusOK {
		t.Fatalf("refresh: %d", st)
	}
	if again, _ := o.svc.Devices(ctx, u.ID); len(again) != 1 || again[0].ID != d.ID {
		t.Fatalf("after refresh: %+v", again)
	}
	if apps, _ := o.svc.AppGrants(ctx, u.ID); len(apps) != 0 {
		t.Errorf("desktop app listed among apps: %+v", apps)
	}

	// Someone else cannot unlink it; its owner can, and that ends it at once.
	other := o.newUser(t)
	if err := o.svc.UnlinkDevice(ctx, o.client, other.ID, d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unlink by another user: %v", err)
	}
	if err := o.svc.UnlinkDevice(ctx, o.client, sess.UserID, d.ID); err != nil {
		t.Fatal(err)
	}
	if st := o.reportDevice(t, renewed.AccessToken, "werk-laptop", "linux"); st != http.StatusUnauthorized {
		t.Errorf("token of an unlinked device: %d", st)
	}
	if left, _ := o.svc.Devices(ctx, u.ID); len(left) != 0 {
		t.Errorf("still listed: %+v", left)
	}
	events, _ := o.svc.Activity(ctx, u.ID, nil, 10)
	if len(events) == 0 || events[0].Event != EventDeviceUnlinked {
		t.Errorf("unlink not audited: %+v", events)
	}
}

func TestActivityPaging(t *testing.T) {
	o := newOIDCEnv(t)
	u := o.newUser(t)
	o.signIn(t, u)
	for range 5 {
		if err := o.svc.audit(context.Background(), o.svc.db, o.client, auditEntry{event: EventPasswordChanged, actor: &u.ID}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := o.svc.Activity(context.Background(), u.ID, nil, 50)
	if err != nil || len(all) < 6 {
		t.Fatalf("activity: %d %v", len(all), err)
	}

	type page struct {
		Events  []AuditEvent `json:"events"`
		HasMore bool         `json:"has_more"`
	}
	var seen []AuditEvent
	before := ""
	for i := 0; ; i++ {
		q := "?limit=2"
		if before != "" {
			q += "&before=" + before
		}
		resp, err := o.browser.Get(o.base + "/api/v1/account/activity" + q)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("page %d: %v %v", i, resp, err)
		}
		var p page
		err = json.NewDecoder(resp.Body).Decode(&p)
		resp.Body.Close()
		if err != nil || len(p.Events) == 0 || len(p.Events) > 2 {
			t.Fatalf("page %d: %+v %v", i, p, err)
		}
		seen = append(seen, p.Events...)
		if !p.HasMore {
			break
		}
		before = p.Events[len(p.Events)-1].ID.String()
	}
	if len(seen) != len(all) {
		t.Fatalf("paged %d events, want %d", len(seen), len(all))
	}
	for i := range all {
		if seen[i].ID != all[i].ID {
			t.Fatalf("event %d: %s, want %s (newest first, no gaps)", i, seen[i].ID, all[i].ID)
		}
	}
	for _, bad := range []string{"?limit=0", "?limit=51", "?before=nope"} {
		if resp, _ := o.browser.Get(o.base + "/api/v1/account/activity" + bad); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, resp.StatusCode)
		}
	}
}
