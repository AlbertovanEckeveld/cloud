//go:build integration

package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"
)

// fakeApp records logout tokens posted to its back-channel endpoint.
type fakeApp struct {
	*httptest.Server
	mu     sync.Mutex
	tokens []string
	status int
}

func newFakeApp(t *testing.T) *fakeApp {
	t.Helper()
	a := &fakeApp{status: http.StatusOK}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		a.mu.Lock()
		defer a.mu.Unlock()
		a.tokens = append(a.tokens, r.PostForm.Get("logout_token"))
		w.WriteHeader(a.status)
	}))
	t.Cleanup(a.Close)
	return a
}

func (a *fakeApp) received() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.tokens...)
}

func (o *oidcEnv) appWithLogout(t *testing.T, backchannel string) (OIDCClient, string) {
	t.Helper()
	_, actor := o.newAdmin(t)
	c, secret, err := o.svc.CreateOIDCClient(context.Background(), o.client, actor, OIDCClientInput{
		Name: "Logout App", Confidential: true, RedirectURIs: []string{appRedirect},
		PostLogoutRedirectURIs: []string{"https://app.example.test/bye"}, BackchannelLogoutURI: backchannel,
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

// signInToApp runs the code flow and returns the tokens.
func (o *oidcEnv) signInToApp(t *testing.T, clientID, secret string) tokens {
	t.Helper()
	f := newFlow(clientID)
	loc, status, code := o.approve(t, requestID(t, o.authorize(t, f)))
	if status != http.StatusOK {
		t.Fatalf("approve: %d %s", status, code)
	}
	_, tk := o.token(t, clientID, secret, codeExchange(loc.Query().Get("code"), f))
	if tk.RefreshToken == "" {
		t.Fatalf("tokens: %+v", tk)
	}
	return tk
}

func TestBackchannelLogout(t *testing.T) {
	o := newOIDCEnv(t)
	_, admin := o.newAdmin(t)
	u := o.newUser(t)
	o.signIn(t, u)
	app := newFakeApp(t)
	client, secret := o.appWithLogout(t, app.URL+"/backchannel")
	o.signInToApp(t, client.ID, secret)
	ctx := context.Background()

	grants, err := o.svc.AppGrants(ctx, u.ID)
	if err != nil || len(grants) != 1 || grants[0].ClientID != client.ID {
		t.Fatalf("grants = %+v, %v", grants, err)
	}

	// Disabling the user queues a logout token; delivery posts it to the app.
	if err := o.svc.SetUserDisabled(ctx, o.client, o.as(t, admin), u.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := o.svc.deliverLogoutBatch(ctx); err != nil {
		t.Fatal(err)
	}
	got := app.received()
	if len(got) != 1 {
		t.Fatalf("app received %d logout tokens", len(got))
	}

	// Verify the logout token independently against the published keys.
	jws, err := jose.ParseSigned(got[0], []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		t.Fatal(err)
	}
	if typ := jws.Signatures[0].Protected.ExtraHeaders["typ"]; typ != "logout+jwt" {
		t.Errorf("typ = %v", typ)
	}
	resp, err := o.plain.Get(o.base + "/oidc/keys")
	if err != nil {
		t.Fatal(err)
	}
	var jwks jose.JSONWebKeySet
	_ = json.NewDecoder(resp.Body).Decode(&jwks)
	resp.Body.Close()
	payload, err := jws.Verify(jwks.Key(jws.Signatures[0].Header.KeyID)[0].Key)
	if err != nil {
		t.Fatalf("logout token signature: %v", err)
	}
	var claims map[string]any
	_ = json.Unmarshal(payload, &claims)
	events, _ := claims["events"].(map[string]any)
	if _, ok := events[backchannelEvent]; !ok || claims["iss"] != o.base+"/oidc" || claims["aud"] != client.ID ||
		claims["sub"] != u.ID.String() || claims["jti"] == nil || claims["nonce"] != nil {
		t.Fatalf("logout token claims = %v", claims)
	}

	if grants, _ := o.svc.AppGrants(ctx, u.ID); len(grants) != 0 {
		t.Errorf("grants after disable: %+v", grants)
	}
	var queued int
	_ = o.pool.QueryRow(ctx, `SELECT count(*) FROM identity.oidc_logout_outbox WHERE client_id = $1`, client.ID).Scan(&queued)
	if queued != 0 {
		t.Errorf("%d logout tokens still queued", queued)
	}
}

func TestBackchannelLogoutRetries(t *testing.T) {
	o := newOIDCEnv(t)
	u := o.newUser(t)
	token := o.signIn(t, u)
	app := newFakeApp(t)
	app.status = http.StatusInternalServerError
	client, secret := o.appWithLogout(t, app.URL+"/backchannel")
	o.signInToApp(t, client.ID, secret)
	ctx := context.Background()

	if err := o.svc.LogoutEverywhere(ctx, o.client, o.session(t, token)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.svc.Authenticate(ctx, token); err == nil {
		t.Error("Cloud session survived logout everywhere")
	}
	if err := o.svc.deliverLogoutBatch(ctx); err != nil {
		t.Fatal(err)
	}
	var attempts int
	var next time.Time
	if err := o.pool.QueryRow(ctx, `SELECT attempts, next_attempt_at FROM identity.oidc_logout_outbox WHERE client_id = $1`,
		client.ID).Scan(&attempts, &next); err != nil {
		t.Fatalf("failed delivery not kept for retry: %v", err)
	}
	if attempts != 1 || !next.After(o.clock) {
		t.Errorf("attempts=%d next=%v", attempts, next)
	}

	// Once the app answers again, the retry succeeds.
	app.mu.Lock()
	app.status = http.StatusOK
	app.mu.Unlock()
	o.advance(2 * time.Minute)
	if err := o.svc.deliverLogoutBatch(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(app.received()); n != 2 {
		t.Errorf("app received %d posts, want 2 (failed + retried)", n)
	}
}

func TestRPInitiatedLogout(t *testing.T) {
	o := newOIDCEnv(t)
	u := o.newUser(t)
	o.signIn(t, u)
	client, secret := o.appWithLogout(t, "")
	tk := o.signInToApp(t, client.ID, secret)

	end := func(redirect string) *http.Response {
		q := url.Values{"id_token_hint": {tk.IDToken}, "post_logout_redirect_uri": {redirect}, "state": {"st-1"}}
		resp, err := o.browser.Get(o.base + "/oidc/end_session?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	// An unregistered target is never redirected to.
	if loc := end("https://evil.example.test/").Header.Get("Location"); strings.Contains(loc, "evil.example.test") {
		t.Fatalf("redirected to unregistered target: %s", loc)
	}

	resp := end("https://app.example.test/bye")
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || loc.Path != "/signed-out" {
		t.Fatalf("end_session: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	id := loc.Query().Get("request")
	info, err := o.svc.logoutRequest(context.Background(), uuid.MustParse(id))
	if err != nil || info.AppName != "Logout App" || !strings.HasPrefix(info.Redirect, "https://app.example.test/bye") ||
		!strings.Contains(info.Redirect, "state=st-1") {
		t.Fatalf("logout request = %+v, %v", info, err)
	}
	// The app's tokens are revoked.
	if _, after := o.token(t, client.ID, secret, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tk.RefreshToken}}); after.Error == "" {
		t.Error("refresh token works after the app signed out")
	}
}

func TestBackchannelURIValidation(t *testing.T) {
	o := newOIDCEnv(t)
	_, admin := o.newAdmin(t)
	for _, uri := range []string{"http://app.example.test/logout", "ftp://x/y", "/relative"} {
		_, _, err := o.svc.CreateOIDCClient(context.Background(), o.client, admin, OIDCClientInput{
			Name: "x", RedirectURIs: []string{appRedirect}, BackchannelLogoutURI: uri})
		if err == nil {
			t.Errorf("backchannel URI %q accepted", uri)
		}
	}
}
