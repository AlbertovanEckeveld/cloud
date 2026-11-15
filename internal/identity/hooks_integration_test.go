//go:build integration

package identity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestHooksForOtherModules covers what identity offers other modules for
// their administrative endpoints: a recent-sign-in guard and the audit log.
func TestHooksForOtherModules(t *testing.T) {
	e := newTestEnv(t)
	u, sess := e.newAdmin(t)
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.svc.AuditRequest(r, "test.hook", map[string]any{"thing": "x"})
		w.WriteHeader(http.StatusNoContent)
	})
	guarded := e.svc.RequireRecentAuth(ok)
	call := func() int {
		r := httptest.NewRequest("POST", "/", nil)
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, Principal{User: u, Session: sess}))
		w := httptest.NewRecorder()
		guarded.ServeHTTP(w, r)
		return w.Code
	}

	if code := call(); code != http.StatusNoContent {
		t.Fatalf("fresh session: %d", code)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM identity.audit_events WHERE actor_id = $1 AND event = 'test.hook' AND details->>'thing' = 'x'`, u.ID).Scan(&n)
	if n != 1 {
		t.Errorf("audit events = %d", n)
	}

	e.advance(recentAuthWindow + time.Minute)
	if code := call(); code != http.StatusForbidden {
		t.Errorf("stale session: %d", code)
	}
	// Without a principal (RequireUser did not run) nothing gets through.
	w := httptest.NewRecorder()
	guarded.ServeHTTP(w, httptest.NewRequest("POST", "/", nil))
	if w.Code == http.StatusNoContent {
		t.Error("request without a principal passed")
	}
}
