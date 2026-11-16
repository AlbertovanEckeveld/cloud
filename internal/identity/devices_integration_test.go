//go:build integration

package identity

import (
	"context"
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
	admin := o.svc.RequireUserOrDevice(RequireAdmin(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
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
