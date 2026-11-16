package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// fakeIssuer serves an OIDC discovery document; tweak lets a test tamper with it.
func fakeIssuer(t *testing.T, tweak func(base string, doc map[string]string)) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := srv.URL
		doc := map[string]string{
			"issuer":                 base + "/oidc",
			"authorization_endpoint": base + "/oidc/authorize",
			"token_endpoint":         base + "/oidc/oauth/token",
			"revocation_endpoint":    base + "/oidc/revoke",
		}
		if tweak != nil {
			tweak(base, doc)
		}
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDiscoveryMustStayOnTheServer(t *testing.T) {
	ctx := context.Background()
	ok := fakeIssuer(t, nil)
	if _, err := discover(ctx, newHTTP(), ok.URL, true); err != nil {
		t.Fatalf("valid discovery: %v", err)
	}
	if _, err := discover(ctx, newHTTP(), ok.URL, false); err == nil {
		t.Error("http accepted without permission")
	}
	for name, tweak := range map[string]func(string, map[string]string){
		"foreign token endpoint": func(_ string, d map[string]string) { d["token_endpoint"] = "https://evil.example/token" },
		"foreign issuer":         func(_ string, d map[string]string) { d["issuer"] = "https://evil.example/oidc" },
		"other port": func(b string, d map[string]string) {
			d["authorization_endpoint"] = strings.Replace(b, "127.0.0.1", "127.0.0.2", 1) + "/a"
		},
	} {
		srv := fakeIssuer(t, tweak)
		if _, err := discover(ctx, newHTTP(), srv.URL, true); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestSignInRejectsForeignAnswer(t *testing.T) {
	srv := fakeIssuer(t, nil)
	_, err := SignIn(context.Background(), srv.URL, true, func(authorize string) {
		u, _ := url.Parse(authorize)
		cb, _ := url.Parse(u.Query().Get("redirect_uri"))
		if cb.Hostname() != "127.0.0.1" {
			t.Errorf("redirect not on loopback: %s", cb)
		}
		if u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("code_challenge") == "" {
			t.Error("no PKCE")
		}
		// An attacker's page sends the browser to the listener with its own code.
		q := cb.Query()
		q.Set("code", "attacker-code")
		q.Set("state", "guessed")
		cb.RawQuery = q.Encode()
		go func() {
			resp, err := http.Get(cb.String())
			if err == nil {
				resp.Body.Close()
			}
		}()
	})
	if err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("forged callback accepted: %v", err)
	}
}
