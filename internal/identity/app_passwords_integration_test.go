//go:build integration

package identity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAppPasswords(t *testing.T) {
	e := newTestEnv(t)
	u, sess := e.newAdmin(t)
	ctx := context.Background()

	if _, err := e.svc.CreateAppPassword(ctx, e.client, sess, " "); err == nil {
		t.Error("empty name accepted")
	}
	ap, err := e.svc.CreateAppPassword(ctx, e.client, sess, "Laptop WebDAV")
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.ReplaceAll(ap.Password, "-", "")) != 26 {
		t.Errorf("password %q", ap.Password)
	}
	var stored []byte
	_ = e.pool.QueryRow(ctx, `SELECT token_hash FROM identity.app_passwords WHERE id = $1`, ap.ID).Scan(&stored)
	if strings.Contains(string(stored), ap.Password) {
		t.Error("password stored in plain text")
	}

	// Typed without dashes and in capitals still works.
	got, err := e.svc.AuthenticateAppPassword(ctx, e.client, strings.ToUpper(u.Username), strings.ToUpper(strings.ReplaceAll(ap.Password, "-", "")))
	if err != nil || got.ID != u.ID {
		t.Fatalf("authenticate: %v %v", got, err)
	}
	list, _ := e.svc.ListAppPasswords(ctx, u.ID)
	if len(list) != 1 || list[0].LastUsedAt == nil || list[0].Password != "" {
		t.Errorf("list: %+v", list)
	}

	// The middleware: Basic auth with the app password, never the account password.
	h := e.svc.RequireAppPassword("Cloud")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFrom(r.Context())
		_, _ = w.Write([]byte(p.User.Username))
	}))
	call := func(user, pass string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PROPFIND", "/dav/files/", nil)
		r.RemoteAddr = e.client.IP + ":1234"
		if user != "" {
			r.SetBasicAuth(user, pass)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call(u.Username, ap.Password); w.Code != http.StatusOK || w.Body.String() != u.Username {
		t.Errorf("valid app password: %d %s", w.Code, w.Body)
	}
	if w := call("", ""); w.Code != http.StatusUnauthorized || !strings.Contains(w.Header().Get("WWW-Authenticate"), "Basic") {
		t.Errorf("no credentials: %d %v", w.Code, w.Header())
	}
	if w := call("someone-else", ap.Password); w.Code != http.StatusUnauthorized {
		t.Errorf("other username: %d", w.Code)
	}
	if w := call(u.Username, testPassword); w.Code != http.StatusUnauthorized {
		t.Errorf("account password accepted: %d", w.Code)
	}

	// Revoking ends it.
	if err := e.svc.RevokeAppPassword(ctx, e.client, u.ID, ap.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AuthenticateAppPassword(ctx, e.client, u.Username, ap.Password); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("revoked: %v", err)
	}

	// Creating needs a recent sign-in.
	e.advance(recentAuthWindow + time.Minute)
	if _, err := e.svc.CreateAppPassword(ctx, e.client, sess, "Te laat"); !errors.Is(err, ErrReauthRequired) {
		t.Errorf("stale session: %v", err)
	}
}

func TestAppPasswordThrottle(t *testing.T) {
	e := newTestEnv(t)
	u, _ := e.newAdmin(t)
	ctx := context.Background()
	for range throttleUser.threshold {
		_, _ = e.svc.AuthenticateAppPassword(ctx, e.client, u.Username, "verkeerd")
	}
	if _, err := e.svc.AuthenticateAppPassword(ctx, e.client, u.Username, "verkeerd"); err == nil || errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("after %d failures: %v, want throttled", throttleUser.threshold, err)
	}
}
