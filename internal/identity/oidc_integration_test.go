//go:build integration

package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"
)

const appRedirect = "https://app.example.test/callback"

// oidcEnv runs the identity API and the OIDC provider on a real HTTP server
// whose address is the issuer, so discovery and redirects are genuine.
type oidcEnv struct {
	*testEnv
	base    string
	browser *http.Client // has the user's session cookie, never follows redirects
	plain   *http.Client // what an app's backend uses
}

func newOIDCEnv(t *testing.T) *oidcEnv {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("http://localhost:%d", ln.Addr().(*net.TCPAddr).Port)
	u, _ := url.Parse(base)
	e := newTestEnvURL(t, u)
	e.client.IP = "127.0.0.1"

	provider, err := e.svc.OIDCHandler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Route("/api/v1", e.svc.Routes)
	r.Mount("/oidc", provider)
	r.Get("/avatars/{file}", e.svc.AvatarHandler)
	srv := httptest.NewUnstartedServer(r)
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &oidcEnv{testEnv: e, base: base,
		browser: &http.Client{Jar: jar, CheckRedirect: noRedirect},
		plain:   &http.Client{CheckRedirect: noRedirect}}
}

// signIn gives the browser a full session for u and returns its token.
func (o *oidcEnv) signIn(t *testing.T, u User) string {
	t.Helper()
	_, token := o.activate(t, u)
	base, _ := url.Parse(o.base)
	o.browser.Jar.SetCookies(base, []*http.Cookie{{Name: "session", Value: token, Path: "/"}})
	return token
}

func (o *oidcEnv) newClient(t *testing.T, confidential bool, group *uuid.UUID) (OIDCClient, string) {
	t.Helper()
	_, actor := o.newAdmin(t)
	c, secret, err := o.svc.CreateOIDCClient(context.Background(), o.client, actor, OIDCClientInput{
		Name: "Test App", Confidential: confidential, RedirectURIs: []string{appRedirect}, AllowedGroupID: group,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := o.pool.Exec(context.Background(), `DELETE FROM identity.oidc_clients WHERE id = $1`, c.ID); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	return c, secret
}

func pkce() (verifier, challenge string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

type flow struct {
	clientID, verifier, state, nonce string
	extra                            url.Values
}

func newFlow(clientID string) *flow {
	f := &flow{clientID: clientID, state: "state-" + uuid.NewString(), nonce: "nonce-" + uuid.NewString(), extra: url.Values{}}
	f.verifier, _ = pkce()
	return f
}

// authorize starts the flow and returns the sign-in page location.
func (o *oidcEnv) authorize(t *testing.T, f *flow) *http.Response {
	t.Helper()
	_, challenge := func() (string, string) {
		sum := sha256.Sum256([]byte(f.verifier))
		return "", base64.RawURLEncoding.EncodeToString(sum[:])
	}()
	q := url.Values{
		"client_id": {f.clientID}, "redirect_uri": {appRedirect}, "response_type": {"code"},
		"scope": {"openid profile email groups offline_access"}, "state": {f.state}, "nonce": {f.nonce},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}
	for k, v := range f.extra {
		q[k] = v
	}
	resp, err := o.browser.Get(o.base + "/oidc/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// requestID extracts the request from the redirect to the sign-in page.
func requestID(t *testing.T, resp *http.Response) string {
	t.Helper()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound || loc.Path != "/connect" {
		t.Fatalf("expected redirect to /connect, got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	return loc.Query().Get("request")
}

// approve signs the request in and follows the provider's callback; it
// returns the app redirect (with code or error) and the approve status.
func (o *oidcEnv) approve(t *testing.T, id string) (*url.URL, int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", o.base+"/api/v1/oidc/requests/"+id+"/approve", nil)
	resp, err := o.browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Redirect string
		Error    struct{ Code string }
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, body.Error.Code
	}
	cb, err := o.browser.Get(body.Redirect)
	if err != nil {
		t.Fatal(err)
	}
	cb.Body.Close()
	loc, _ := url.Parse(cb.Header.Get("Location"))
	return loc, resp.StatusCode, ""
}

type tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	Error        string `json:"error"`
}

func (o *oidcEnv) token(t *testing.T, clientID, secret string, form url.Values) (int, tokens) {
	t.Helper()
	if secret == "" {
		form.Set("client_id", clientID) // public client: identifies itself, no secret
	}
	req, _ := http.NewRequest("POST", o.base+"/oidc/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if secret != "" {
		req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(secret))
	}
	resp, err := o.plain.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out tokens
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func codeExchange(code string, f *flow) url.Values {
	return url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {appRedirect}, "code_verifier": {f.verifier}}
}

// verifyIDToken checks the signature against the published JWKS and
// returns the claims, independently of the provider library.
func (o *oidcEnv) verifyIDToken(t *testing.T, raw string) map[string]any {
	t.Helper()
	resp, err := o.plain.Get(o.base + "/oidc/keys")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var jwks jose.JSONWebKeySet
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		t.Fatal(err)
	}
	sig, err := jose.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		t.Fatalf("parse id token: %v", err)
	}
	keys := jwks.Key(sig.Signatures[0].Header.KeyID)
	if len(keys) != 1 {
		t.Fatalf("signing key %q not in JWKS", sig.Signatures[0].Header.KeyID)
	}
	payload, err := sig.Verify(keys[0].Key)
	if err != nil {
		t.Fatalf("id token signature invalid: %v", err)
	}
	var claims map[string]any
	_ = json.Unmarshal(payload, &claims)
	return claims
}

func (o *oidcEnv) userinfo(t *testing.T, accessToken string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("GET", o.base+"/oidc/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := o.plain.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var claims map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&claims)
	return resp.StatusCode, claims
}

func TestOIDCCodeFlow(t *testing.T) {
	o := newOIDCEnv(t)
	_, admin := o.newAdmin(t)
	g := o.newGroup(t, admin)
	u, email := o.newMailUser(t)
	if err := o.svc.SetGroupMember(context.Background(), o.client, o.as(t, admin), g.ID, u.ID, true); err != nil {
		t.Fatal(err)
	}
	o.signIn(t, u)
	client, secret := o.newClient(t, true, nil)

	// Discovery points at this server.
	resp, err := o.plain.Get(o.base + "/oidc/.well-known/openid-configuration")
	if err != nil {
		t.Fatal(err)
	}
	var disc map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&disc)
	resp.Body.Close()
	if disc["issuer"] != o.base+"/oidc" || disc["token_endpoint"] != o.base+"/oidc/oauth/token" {
		t.Fatalf("discovery = %v", disc)
	}
	// Discovery advertises only what clients can actually use.
	if fmt.Sprint(disc["response_types_supported"]) != "[code]" ||
		fmt.Sprint(disc["grant_types_supported"]) != "[authorization_code refresh_token]" ||
		disc["device_authorization_endpoint"] != nil ||
		fmt.Sprint(disc["acr_values_supported"]) != "[mfa phr phrh]" {
		t.Errorf("discovery promises unsupported features: %v", disc)
	}

	f := newFlow(client.ID)
	id := requestID(t, o.authorize(t, f))
	loc, status, _ := o.approve(t, id)
	if status != http.StatusOK || loc.Host != "app.example.test" || loc.Query().Get("state") != f.state {
		t.Fatalf("callback = %v (%d)", loc, status)
	}
	code := loc.Query().Get("code")

	// Wrong secret and wrong PKCE verifier are both refused.
	if st, _ := o.token(t, client.ID, "wrong-secret", codeExchange(code, f)); st != http.StatusUnauthorized && st != http.StatusBadRequest {
		t.Errorf("wrong secret: status %d", st)
	}
	bad := codeExchange(code, f)
	bad.Set("code_verifier", "x"+f.verifier)
	if _, tk := o.token(t, client.ID, secret, bad); tk.Error != "invalid_grant" {
		t.Errorf("wrong verifier: %+v", tk)
	}

	st, tk := o.token(t, client.ID, secret, codeExchange(code, f))
	if st != http.StatusOK || tk.AccessToken == "" || tk.RefreshToken == "" || tk.IDToken == "" {
		t.Fatalf("token exchange: %d %+v", st, tk)
	}
	// An authorization code works once.
	if _, again := o.token(t, client.ID, secret, codeExchange(code, f)); again.Error == "" {
		t.Error("authorization code accepted twice")
	}

	claims := o.verifyIDToken(t, tk.IDToken)
	groups, _ := claims["groups"].([]any)
	// "aud" may be a string or an array (RFC 7519 §4.1.3); it must name this client.
	aud, _ := claims["aud"].([]any)
	if claims["iss"] != o.base+"/oidc" || len(aud) != 1 || aud[0] != client.ID || claims["nonce"] != f.nonce ||
		claims["sub"] != u.ID.String() || claims["preferred_username"] != u.Username ||
		claims["email"] != email || claims["email_verified"] != true || len(groups) != 1 || groups[0] != g.Name {
		t.Fatalf("id token claims = %v", claims)
	}
	// How the user signed in: password plus authenticator code.
	if fmt.Sprint(claims["amr"]) != "[pwd otp mfa]" || claims["acr"] != acrMFA {
		t.Errorf("amr/acr = %v / %v", claims["amr"], claims["acr"])
	}

	if st, info := o.userinfo(t, tk.AccessToken); st != http.StatusOK || info["sub"] != u.ID.String() || info["email"] != email {
		t.Fatalf("userinfo: %d %v", st, info)
	}

	// Refresh tokens rotate: the old one stops working.
	refresh := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tk.RefreshToken}}
	st, rotated := o.token(t, client.ID, secret, refresh)
	if st != http.StatusOK || rotated.RefreshToken == "" || rotated.RefreshToken == tk.RefreshToken {
		t.Fatalf("refresh: %d %+v", st, rotated)
	}
	if _, reused := o.token(t, client.ID, secret, refresh); reused.Error != "invalid_grant" {
		t.Errorf("old refresh token reused: %+v", reused)
	}
	if st, _ := o.userinfo(t, tk.AccessToken); st == http.StatusOK {
		t.Error("access token from before the refresh still valid")
	}
	if refreshed := o.verifyIDToken(t, rotated.IDToken); fmt.Sprint(refreshed["amr"]) != "[pwd otp mfa]" {
		t.Errorf("amr lost on refresh: %v", refreshed["amr"])
	}

	// Disabling the user revokes every app token.
	if err := o.svc.SetUserDisabled(context.Background(), o.client, o.as(t, admin), u.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, after := o.token(t, client.ID, secret, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rotated.RefreshToken}}); after.Error == "" {
		t.Error("refresh works for a disabled user")
	}
	if st, _ := o.userinfo(t, rotated.AccessToken); st == http.StatusOK {
		t.Error("userinfo works for a disabled user")
	}
}

func TestOIDCRejectsBadRequests(t *testing.T) {
	o := newOIDCEnv(t)
	client, _ := o.newClient(t, true, nil)

	// An unregistered redirect URI must never be redirected to.
	f := newFlow(client.ID)
	f.extra.Set("redirect_uri", "https://evil.example.test/steal")
	resp := o.authorize(t, f)
	if strings.Contains(resp.Header.Get("Location"), "evil.example.test") || resp.StatusCode == http.StatusFound && strings.HasPrefix(resp.Header.Get("Location"), "/connect") {
		t.Errorf("unregistered redirect_uri accepted: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	// The implicit flow is not allowed for our clients.
	f = newFlow(client.ID)
	f.extra.Set("response_type", "id_token token")
	resp = o.authorize(t, f)
	if strings.HasPrefix(resp.Header.Get("Location"), "/connect") {
		t.Errorf("implicit flow accepted: %s", resp.Header.Get("Location"))
	}

	// Unknown clients get no sign-in page.
	f = newFlow("app-does-not-exist")
	if strings.HasPrefix(o.authorize(t, f).Header.Get("Location"), "/connect") {
		t.Error("unknown client accepted")
	}
}

func TestOIDCPublicClientNeedsPKCE(t *testing.T) {
	o := newOIDCEnv(t)
	u := o.newUser(t)
	o.signIn(t, u)
	client, secret := o.newClient(t, false, nil)
	if secret != "" {
		t.Fatal("public client got a secret")
	}

	// Without a code challenge the request is refused.
	q := url.Values{"client_id": {client.ID}, "redirect_uri": {appRedirect}, "response_type": {"code"}, "scope": {"openid"}, "state": {"s"}}
	resp, err := o.browser.Get(o.base + "/oidc/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if strings.HasPrefix(resp.Header.Get("Location"), "/connect") {
		t.Fatal("public client without PKCE accepted")
	}

	f := newFlow(client.ID)
	loc, _, _ := o.approve(t, requestID(t, o.authorize(t, f)))
	st, tk := o.token(t, client.ID, "", codeExchange(loc.Query().Get("code"), f))
	if st != http.StatusOK || tk.IDToken == "" {
		t.Fatalf("public client token exchange: %d %+v", st, tk)
	}
}

func TestOIDCGroupRestriction(t *testing.T) {
	o := newOIDCEnv(t)
	_, admin := o.newAdmin(t)
	g := o.newGroup(t, admin)
	u := o.newUser(t)
	o.signIn(t, u)
	client, secret := o.newClient(t, true, &g.ID)

	if _, status, code := o.approve(t, requestID(t, o.authorize(t, newFlow(client.ID)))); status != http.StatusForbidden || code != "app_access_denied" {
		t.Fatalf("non-member approve: %d %s", status, code)
	}

	if err := o.svc.SetGroupMember(context.Background(), o.client, o.as(t, admin), g.ID, u.ID, true); err != nil {
		t.Fatal(err)
	}
	f := newFlow(client.ID)
	loc, status, _ := o.approve(t, requestID(t, o.authorize(t, f)))
	if status != http.StatusOK {
		t.Fatalf("member approve: %d", status)
	}
	_, tk := o.token(t, client.ID, secret, codeExchange(loc.Query().Get("code"), f))
	if tk.RefreshToken == "" {
		t.Fatalf("member tokens: %+v", tk)
	}

	// Leaving the group ends access at the next refresh.
	if err := o.svc.SetGroupMember(context.Background(), o.client, o.as(t, admin), g.ID, u.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, after := o.token(t, client.ID, secret, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tk.RefreshToken}}); after.Error == "" {
		t.Error("refresh works after leaving the allowed group")
	}

	// A group that guards an app cannot be deleted.
	if err := o.svc.DeleteGroup(context.Background(), o.client, o.as(t, admin), g.ID); err != ErrGroupInUse {
		t.Errorf("delete guarding group: err = %v", err)
	}
}

func TestOIDCPromptLoginAndDeny(t *testing.T) {
	o := newOIDCEnv(t)
	u := o.newUser(t)
	o.signIn(t, u)
	client, _ := o.newClient(t, true, nil)

	// prompt=login: a session from before the request is not enough.
	f := newFlow(client.ID)
	f.extra.Set("prompt", "login")
	id := requestID(t, o.authorize(t, f))
	if _, status, code := o.approve(t, id); status != http.StatusForbidden || code != "reauth_required" {
		t.Fatalf("prompt=login with old session: %d %s", status, code)
	}
	// After signing in again (a new session), the same request succeeds.
	fresh := newFlow(client.ID)
	fresh.extra.Set("prompt", "login")
	freshID := requestID(t, o.authorize(t, fresh))
	o.advance(time.Second)
	o.signIn(t, o.newUser(t))
	if _, status, code := o.approve(t, freshID); status != http.StatusOK {
		t.Fatalf("prompt=login after a fresh sign-in: %d %s", status, code)
	}
	// max_age: a session older than allowed must sign in again.
	aged := newFlow(client.ID)
	aged.extra.Set("max_age", "60")
	o.advance(2 * time.Minute)
	if _, status, code := o.approve(t, requestID(t, o.authorize(t, aged))); status != http.StatusForbidden || code != "reauth_required" {
		t.Fatalf("max_age exceeded: %d %s", status, code)
	}

	// An app that asks for a phishing-resistant sign-in gets it only with a passkey.
	phr := newFlow(client.ID)
	phr.extra.Set("acr_values", "phr")
	if _, status, code := o.approve(t, requestID(t, o.authorize(t, phr))); status != http.StatusForbidden || code != "passkey_required" {
		t.Fatalf("acr_values=phr with a password session: %d %s", status, code)
	}

	// Denying sends the browser back to the app with an OAuth error.
	resp, err := o.browser.Get(o.base + "/api/v1/oidc/requests/" + id + "/deny")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if loc.Host != "app.example.test" || loc.Query().Get("error") != "access_denied" || loc.Query().Get("state") != f.state {
		t.Fatalf("deny redirect = %v", loc)
	}
	// The request is gone afterwards.
	if _, status, _ := o.approve(t, id); status != http.StatusNotFound {
		t.Errorf("approve after deny: %d", status)
	}
}

func TestOIDCClientValidation(t *testing.T) {
	o := newOIDCEnv(t)
	_, admin := o.newAdmin(t)
	for name, uris := range map[string][]string{
		"http non-loopback": {"http://app.example.test/cb"},
		"fragment":          {"https://app.example.test/cb#x"},
		"relative":          {"/cb"},
		"javascript":        {"javascript:alert(1)"},
		"none":              {},
	} {
		if _, _, err := o.svc.CreateOIDCClient(context.Background(), o.client, admin, OIDCClientInput{Name: "x", RedirectURIs: uris}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// Loopback http is fine for native apps (RFC 8252).
	c, _, err := o.svc.CreateOIDCClient(context.Background(), o.client, admin, OIDCClientInput{Name: "Native", RedirectURIs: []string{"http://127.0.0.1:53682/cb"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := o.svc.DeleteOIDCClient(context.Background(), o.client, admin, c.ID); err != nil {
		t.Fatal(err)
	}
}
