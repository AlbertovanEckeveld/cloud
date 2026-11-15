//go:build integration

package identity

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// httpEnv runs the identity routes on a test server with a cookie jar, like a browser.
type httpEnv struct {
	*testEnv
	srv    *httptest.Server
	client *http.Client
}

func newHTTPEnv(t *testing.T, publicURL string) *httpEnv {
	t.Helper()
	e := newTestEnv(t)
	u, err := url.Parse(publicURL)
	if err != nil {
		t.Fatal(err)
	}
	e.svc.opts.PublicURL = u
	r := chi.NewRouter()
	r.Route("/api/v1", e.svc.Routes)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	// The handlers see the test server's loopback address as client IP.
	e.client.IP = "127.0.0.1"
	return &httpEnv{testEnv: e, srv: srv, client: &http.Client{Jar: jar}}
}

func (h *httpEnv) do(t *testing.T, method, path string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.srv.URL+"/api/v1"+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s %s: non-JSON response %q", method, path, raw)
		}
	}
	return resp, out
}

func TestHTTPLoginFlow(t *testing.T) {
	h := newHTTPEnv(t, "http://localhost:8080")
	u := h.newUser(t)
	secret, _ := h.activate(t, u)

	if resp, _ := h.do(t, "GET", "/auth/session", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session without cookie: %d", resp.StatusCode)
	}
	if resp, _ := h.do(t, "GET", "/account/activity", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("account without login: %d", resp.StatusCode)
	}

	resp, body := h.do(t, "POST", "/auth/login", map[string]string{"username": u.Username, "password": "wrong password!!"})
	if resp.StatusCode != http.StatusUnauthorized || body["error"].(map[string]any)["code"] != "invalid_credentials" {
		t.Fatalf("wrong password: %d %v", resp.StatusCode, body)
	}

	resp, body = h.do(t, "POST", "/auth/login", map[string]string{"username": u.Username, "password": testPassword})
	if resp.StatusCode != http.StatusOK || body["state"] != string(StateMFARequired) {
		t.Fatalf("login: %d %v", resp.StatusCode, body)
	}
	cookie := findCookie(resp, "session")
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Fatalf("session cookie attributes wrong: %+v", cookie)
	}

	// A partial session reveals nothing about the user and grants no access.
	_, body = h.do(t, "GET", "/auth/session", nil)
	if body["state"] != string(StateMFARequired) || body["user"] != nil {
		t.Fatalf("partial session response leaks: %v", body)
	}
	if resp, _ := h.do(t, "GET", "/account/activity", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("account with partial session: %d", resp.StatusCode)
	}

	resp, body = h.do(t, "POST", "/auth/login/totp", map[string]string{"code": h.code(secret)})
	if resp.StatusCode != http.StatusOK || body["state"] != string(StateAuthenticated) {
		t.Fatalf("totp: %d %v", resp.StatusCode, body)
	}
	if full := findCookie(resp, "session"); full == nil || full.Value == cookie.Value {
		t.Fatal("session token not rotated after second factor")
	}
	_, body = h.do(t, "GET", "/auth/session", nil)
	if user, _ := body["user"].(map[string]any); user["username"] != u.Username {
		t.Fatalf("session after login: %v", body)
	}
	if resp, body := h.do(t, "GET", "/account/activity", nil); resp.StatusCode != http.StatusOK || len(body["events"].([]any)) == 0 {
		t.Fatalf("activity: %d %v", resp.StatusCode, body)
	}

	resp, _ = h.do(t, "POST", "/auth/logout", map[string]string{})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if c := findCookie(resp, "session"); c == nil || c.MaxAge >= 0 {
		t.Fatalf("logout did not clear cookie: %+v", c)
	}
	if resp, _ := h.do(t, "GET", "/auth/session", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session after logout: %d", resp.StatusCode)
	}
}

func TestHTTPSecureCookieForHTTPS(t *testing.T) {
	h := newHTTPEnv(t, "https://cloud.example.com")
	u := h.newUser(t)
	h.activate(t, u)
	resp, _ := h.do(t, "POST", "/auth/login", map[string]string{"username": u.Username, "password": testPassword})
	c := findCookie(resp, "__Host-session")
	if c == nil || !c.Secure || c.Domain != "" || c.Path != "/" {
		t.Fatalf("expected __Host- prefixed Secure cookie, got %+v", resp.Cookies())
	}
}

func TestHTTPThrottledResponse(t *testing.T) {
	h := newHTTPEnv(t, "http://localhost:8080")
	u := h.newUser(t)
	h.activate(t, u)
	for range throttleUser.threshold {
		h.do(t, "POST", "/auth/login", map[string]string{"username": u.Username, "password": "wrong password!!"})
	}
	resp, body := h.do(t, "POST", "/auth/login", map[string]string{"username": u.Username, "password": testPassword})
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("status = %d, Retry-After = %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if msg := body["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "minuten") {
		t.Errorf("message = %q", msg)
	}
}

func TestHTTPActivationFlow(t *testing.T) {
	h := newHTTPEnv(t, "http://localhost:8080")
	u := h.newUser(t)
	link, err := h.svc.IssueActivation(t.Context(), h.testEnv.client, u.Username)
	if err != nil {
		t.Fatal(err)
	}
	token := tokenFromLink(t, link.Link)

	resp, body := h.do(t, "POST", "/auth/activate", map[string]string{"token": token, "password": "short"})
	if resp.StatusCode != http.StatusUnprocessableEntity || body["error"].(map[string]any)["code"] != "weak_password" {
		t.Fatalf("weak password: %d %v", resp.StatusCode, body)
	}
	resp, body = h.do(t, "POST", "/auth/activate", map[string]string{"token": token, "password": testPassword})
	if resp.StatusCode != http.StatusOK || body["state"] != string(StateMFAEnrollmentRequired) {
		t.Fatalf("activate: %d %v", resp.StatusCode, body)
	}
	resp, body = h.do(t, "POST", "/auth/totp/enrollment", nil)
	if resp.StatusCode != http.StatusOK || body["secret"] == "" || !strings.HasPrefix(body["uri"].(string), "otpauth://") {
		t.Fatalf("begin enrollment: %d %v", resp.StatusCode, body)
	}
	secret, _ := base32NoPad.DecodeString(body["secret"].(string))
	resp, body = h.do(t, "POST", "/auth/totp/enrollment/confirm", map[string]string{"code": h.code(secret)})
	if resp.StatusCode != http.StatusOK || body["state"] != string(StateAuthenticated) {
		t.Fatalf("confirm: %d %v", resp.StatusCode, body)
	}
	h.advance(totpPeriod * time.Second)

	// After activation, enrollment endpoints are closed.
	if resp, _ := h.do(t, "POST", "/auth/totp/enrollment", nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("enrollment after activation: %d", resp.StatusCode)
	}
}

func findCookie(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestHTTPAdminRoutesRequireAdmin(t *testing.T) {
	h := newHTTPEnv(t, "http://localhost:8080")
	u := h.newUser(t)
	secret, _ := h.activate(t, u)

	// Sign in over HTTP as a regular user.
	h.do(t, "POST", "/auth/login", map[string]string{"username": u.Username, "password": testPassword})
	if resp, _ := h.do(t, "POST", "/auth/login/totp", map[string]string{"code": h.code(secret)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	for _, req := range []struct{ method, path string }{
		{"GET", "/admin/users"},
		{"POST", "/admin/users"},
		{"GET", "/admin/groups"},
		{"GET", "/admin/audit"},
		{"POST", "/admin/users/" + u.ID.String() + "/disable"},
	} {
		var body any
		if req.method == "POST" {
			body = map[string]any{}
		}
		if resp, _ := h.do(t, req.method, req.path, body); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s as regular user: %d, want 403", req.method, req.path, resp.StatusCode)
		}
	}
	// Their own groups are visible to them.
	if resp, body := h.do(t, "GET", "/account/groups", nil); resp.StatusCode != http.StatusOK || body["groups"] == nil {
		t.Errorf("own groups: %d %v", resp.StatusCode, body)
	}
}
