package httpserver

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// RealIP makes r.RemoteAddr the client's address when the platform runs
// behind trusted reverse proxies. Only a request whose direct peer is a
// trusted proxy is believed, and only for the right-most address in
// X-Forwarded-For that is not itself a proxy: earlier entries are whatever
// the client sent and could be forged. Without trusted proxies it does
// nothing. Rate limiting and the audit log rely on this address.
func RealIP(trusted []netip.Prefix) func(http.Handler) http.Handler {
	isProxy := func(a netip.Addr) bool {
		for _, p := range trusted {
			if p.Contains(a.Unmap()) {
				return true
			}
		}
		return false
	}
	return func(next http.Handler) http.Handler {
		if len(trusted) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			peer, perr := netip.ParseAddr(host)
			if err == nil && perr == nil && isProxy(peer) {
				hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
				for i := len(hops) - 1; i >= 0; i-- {
					a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
					if err != nil {
						break // malformed: keep the proxy's address
					}
					if !isProxy(a) {
						r.RemoteAddr = net.JoinHostPort(a.Unmap().String(), "0")
						break
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
