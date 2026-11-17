// Package auth signs the desktop app in to Cloud with OAuth 2.0 for native
// apps (RFC 8252): authorization code with PKCE (RFC 7636) in the system
// browser, the redirect caught on a loopback address. The refresh token is
// kept in the operating system's keyring; access tokens only in memory.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

// ClientID is the desktop app's public OIDC client on the server.
const ClientID = "cloud-desktop"

const keyringService = "cloud-desktop"

// ErrSignedOut means there is no (valid) sign-in; the user must sign in.
var ErrSignedOut = errors.New("not signed in")

type endpoints struct {
	Issuer        string `json:"issuer"`
	Authorization string `json:"authorization_endpoint"`
	Token         string `json:"token_endpoint"`
	Revocation    string `json:"revocation_endpoint"`
}

// Session holds the sign-in to one server and hands out access tokens.
type Session struct {
	server string
	ep     endpoints
	http   *http.Client

	mu      sync.Mutex
	access  string
	expires time.Time
}

// discover reads the server's OIDC configuration and checks that every
// endpoint is on the server itself: a tampered document must not be able to
// send codes or tokens elsewhere.
func discover(ctx context.Context, c *http.Client, server string, allowInsecure bool) (endpoints, error) {
	base, err := url.Parse(server)
	if err != nil || base.Host == "" || (base.Scheme != "https" && !(allowInsecure && base.Scheme == "http")) {
		return endpoints{}, fmt.Errorf("server %q must be an https address", server)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server+"/oidc/.well-known/openid-configuration", nil)
	resp, err := c.Do(req)
	if err != nil {
		return endpoints{}, fmt.Errorf("server not reachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return endpoints{}, fmt.Errorf("server discovery: status %d", resp.StatusCode)
	}
	var ep endpoints
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&ep); err != nil {
		return endpoints{}, fmt.Errorf("server discovery: %w", err)
	}
	if ep.Issuer != server+"/oidc" {
		return endpoints{}, fmt.Errorf("server discovery: issuer %q does not belong to %s", ep.Issuer, server)
	}
	for _, e := range []string{ep.Authorization, ep.Token, ep.Revocation} {
		u, err := url.Parse(e)
		if e != "" && (err != nil || u.Scheme != base.Scheme || u.Host != base.Host) {
			return endpoints{}, fmt.Errorf("server discovery: endpoint %q is not on %s", e, base.Host)
		}
	}
	return ep, nil
}

func newHTTP() *http.Client {
	return &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// SignIn runs the browser sign-in. openBrowser shows the given URL in the
// system browser; the user signs in there (with a passkey), and the server
// redirects to a one-time loopback listener with the code.
func SignIn(ctx context.Context, server string, allowInsecure bool, openBrowser func(string)) (*Session, error) {
	server = strings.TrimRight(server, "/")
	c := newHTTP()
	ep, err := discover(ctx, c, server, allowInsecure)
	if err != nil {
		return nil, err
	}
	// Loopback only (RFC 8252 §7.3); the port is picked by the system.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer ln.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)
	verifier, state := random(48), random(24)
	challenge := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"client_id": {ClientID}, "response_type": {"code"}, "redirect_uri": {redirect},
		"scope": {"openid profile offline_access"}, "state": {state},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"},
	}

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	var once sync.Once
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		got := r.URL.Query()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		var res result
		switch {
		case subtle.ConstantTimeCompare([]byte(got.Get("state")), []byte(state)) != 1:
			res.err = errors.New("sign-in answer does not belong to this request")
		case got.Get("error") != "":
			res.err = fmt.Errorf("sign-in refused: %s", got.Get("error"))
		default:
			res.code = got.Get("code")
		}
		if res.err != nil {
			_, _ = io.WriteString(w, "<p>Inloggen is niet gelukt. Ga terug naar de Cloud-app.</p>")
		} else {
			_, _ = io.WriteString(w, "<p>Je bent ingelogd. Je kunt dit venster sluiten en teruggaan naar de Cloud-app.</p>")
		}
		once.Do(func() { done <- res })
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	openBrowser(ep.Authorization + "?" + q.Encode())
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var res result
	select {
	case res = <-done:
	case <-ctx.Done():
		return nil, errors.New("sign-in took too long or was cancelled")
	}
	if res.err != nil {
		return nil, res.err
	}
	s := &Session{server: server, ep: ep, http: c}
	err = s.token(ctx, url.Values{
		"grant_type": {"authorization_code"}, "code": {res.code},
		"redirect_uri": {redirect}, "code_verifier": {verifier},
	})
	return s, err
}

// Restore resumes a sign-in from the keyring.
func Restore(ctx context.Context, server string, allowInsecure bool) (*Session, error) {
	server = strings.TrimRight(server, "/")
	if _, err := keyring.Get(keyringService, server); err != nil {
		return nil, ErrSignedOut
	}
	c := newHTTP()
	ep, err := discover(ctx, c, server, allowInsecure)
	if err != nil {
		return nil, err
	}
	return &Session{server: server, ep: ep, http: c}, nil
}

// token calls the token endpoint and keeps the result: the access token in
// memory, the (rotated) refresh token in the keyring.
func (s *Session) token(ctx context.Context, form url.Values) error {
	form.Set("client_id", ClientID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.ep.Token, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	if resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		if out.Error == "invalid_grant" || resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
			_ = keyring.Delete(keyringService, s.server)
			return ErrSignedOut
		}
		return fmt.Errorf("token request: status %d", resp.StatusCode)
	}
	if out.RefreshToken != "" {
		if err := keyring.Set(keyringService, s.server, out.RefreshToken); err != nil {
			return fmt.Errorf("store sign-in in the keyring: %w", err)
		}
	}
	s.access, s.expires = out.AccessToken, time.Now().Add(time.Duration(out.ExpiresIn)*time.Second)
	return nil
}

// Token returns a valid access token, refreshing it a minute before it ends.
func (s *Session) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.access != "" && time.Until(s.expires) > time.Minute {
		return s.access, nil
	}
	refresh, err := keyring.Get(keyringService, s.server)
	if err != nil {
		return "", ErrSignedOut
	}
	if err := s.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}); err != nil {
		return "", err
	}
	return s.access, nil
}

// SignOut revokes the refresh token on the server (RFC 7009), which also
// ends the access tokens issued with it, and forgets it locally. The local
// copy is removed even when the server cannot be reached, but then the
// error says so: the device may still be listed under "Apps" on the account
// page, where it can be revoked.
func (s *Session) SignOut(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var revokeErr error
	refresh, err := keyring.Get(keyringService, s.server)
	switch {
	case err != nil:
	case s.ep.Revocation == "":
		revokeErr = errors.New("the server offers no way to sign out this device")
	default:
		form := url.Values{"token": {refresh}, "token_type_hint": {"refresh_token"}, "client_id": {ClientID}}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.ep.Revocation, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := s.http.Do(req)
		if err != nil {
			revokeErr = fmt.Errorf("sign out at the server: %w", err)
		} else {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				revokeErr = fmt.Errorf("sign out at the server: status %d", resp.StatusCode)
			}
		}
	}
	s.access = ""
	if err := keyring.Delete(keyringService, s.server); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return revokeErr
}
