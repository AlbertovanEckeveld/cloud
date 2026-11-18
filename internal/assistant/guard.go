package assistant

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Errors of the address checks. Their messages are safe to show.
var (
	errBlockedAddress = errors.New("dit adres is niet toegestaan")
	errExternal       = errors.New("dit adres ligt buiten het thuisnetwerk")
)

// guard decides where the platform may connect for an AI provider (ADR
// 0023, SSRF). Whoever sets a provider's URL makes the server connect
// there, so every connection is checked: after DNS, against the address
// actually dialled, so a name cannot point outside at the check and inside
// at the connection.
type guard struct {
	// services are the platform's own backends (host:port), never a provider.
	services []string
	resolver *net.Resolver
}

// cgnat is the shared address space NetBird (and Tailscale) use: internal.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// forbidden reports addresses no provider may have: unspecified, multicast
// and link-local, which includes cloud metadata services (169.254.169.254).
func forbidden(a netip.Addr) bool {
	a = a.Unmap()
	return !a.IsValid() || a.IsUnspecified() || a.IsMulticast() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast()
}

// internal reports addresses inside the home or NetBird network.
func internal(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate() || cgnat.Contains(a)
}

// resolve returns the addresses of host (an IP literal is its own address).
func (g *guard) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	return g.resolver.LookupNetIP(ctx, "ip", host)
}

// check resolves host and returns its addresses if every one is allowed.
// Without allowExternal, an address outside the home network is refused:
// a provider registered as local must stay local.
func (g *guard) check(ctx context.Context, host, port string, allowExternal bool) ([]netip.Addr, error) {
	addrs, err := g.resolve(ctx, host)
	if err != nil || len(addrs) == 0 {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	for _, a := range addrs {
		if forbidden(a) {
			return nil, errBlockedAddress
		}
		if !allowExternal && !internal(a) {
			return nil, errExternal
		}
		for _, svc := range g.services {
			sh, sp, err := net.SplitHostPort(svc)
			if err != nil || sp != port {
				continue
			}
			ips, err := g.resolve(ctx, sh)
			if err != nil {
				continue // a service we cannot resolve cannot be reached through us either
			}
			for _, ip := range ips {
				if ip.Unmap() == a.Unmap() {
					return nil, errBlockedAddress
				}
			}
		}
	}
	return addrs, nil
}

// isExternal reports whether host resolves to any address outside the home
// network; that makes a provider external (consent and warnings apply).
func (g *guard) isExternal(ctx context.Context, host string) (bool, error) {
	addrs, err := g.resolve(ctx, host)
	if err != nil || len(addrs) == 0 {
		return false, fmt.Errorf("resolve %s: %w", host, err)
	}
	for _, a := range addrs {
		if forbidden(a) {
			return false, errBlockedAddress
		}
		if !internal(a) {
			return true, nil
		}
	}
	return false, nil
}

// dialer returns a DialContext that only connects to checked addresses.
func (g *guard) dialer(allowExternal bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 10 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		addrs, err := g.check(ctx, host, port, allowExternal)
		if err != nil {
			return nil, err
		}
		var last error
		for _, a := range addrs {
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		return nil, last
	}
}

// parseBaseURL checks a provider address as typed by an administrator:
// http(s) only, a host, no credentials, query or fragment.
func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, &InputError{"Vul een adres in zoals http://localhost:11434/v1 of https://api.example.com/v1."}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

// portOf returns the port of u, the scheme's default when none is given.
func portOf(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}
