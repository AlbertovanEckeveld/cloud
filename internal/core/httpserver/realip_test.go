package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestRealIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("172.20.0.0/16")}
	var seen string
	h := RealIP(trusted)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r.RemoteAddr }))
	for name, tc := range map[string]struct{ peer, xff, want string }{
		"behind the proxy":   {"172.20.0.5:4000", "100.80.1.2", "100.80.1.2:0"},
		"forged first hop":   {"172.20.0.5:4000", "6.6.6.6, 100.80.1.2", "100.80.1.2:0"},
		"not from the proxy": {"100.80.9.9:4000", "6.6.6.6", "100.80.9.9:4000"},
		"malformed":          {"172.20.0.5:4000", "rommel", "172.20.0.5:4000"},
		"no header":          {"172.20.0.5:4000", "", "172.20.0.5:4000"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.peer
		if tc.xff != "" {
			r.Header.Set("X-Forwarded-For", tc.xff)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
		if seen != tc.want {
			t.Errorf("%s: %q, want %q", name, seen, tc.want)
		}
	}
}
