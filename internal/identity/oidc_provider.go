package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/AlbertovanEckeveld/cloud/internal/core/httpserver"
)

// ErrAppAccessDenied means the user may not sign in to this app.
var ErrAppAccessDenied = errors.New("no access to this app")

// ErrPasskeyRequired means the app asked for a phishing-resistant sign-in
// (acr_values phr or phrh) and the session was made otherwise.
var ErrPasskeyRequired = errors.New("app requires a passkey sign-in")

// acrRank orders the acr values this provider issues by strength.
var acrRank = map[string]int{acrMFA: 1, acrPhishingResistant: 2, acrPhishingResistantHardware: 3}

// acrSatisfies reports whether a session with acr have meets a request's
// acr_values. Values this provider does not know are ignored; the request
// is met if any known requested value is (OIDC Core §3.1.2.1).
func acrSatisfies(have string, requested []string) bool {
	known := false
	for _, r := range requested {
		rank, ok := acrRank[r]
		if !ok {
			continue
		}
		known = true
		if acrRank[have] >= rank {
			return true
		}
	}
	return !known
}

// OIDCIssuerPath is where the provider is mounted below PUBLIC_URL.
const OIDCIssuerPath = "/oidc"

func (s *Service) oidcIssuer() string { return s.opts.PublicURL.String() + OIDCIssuerPath }

// OIDCHandler builds the OpenID Connect provider. Mount it at OIDCIssuerPath;
// it serves discovery, authorize, token, userinfo, keys, revoke and logout.
func (s *Service) OIDCHandler(ctx context.Context) (http.Handler, error) {
	if err := s.ensureSigningKey(ctx); err != nil {
		return nil, err
	}
	config := &op.Config{
		CryptoKey:                s.opts.OIDCTokenKey,
		DefaultLogoutRedirectURI: s.opts.PublicURL.String() + "/",
		CodeMethodS256:           true,
		AuthMethodPost:           true,
		GrantTypeRefreshToken:    true,
		// Logout tokens carry sub, not sid: apps sign the user out everywhere.
		BackChannelLogoutSupported: true,
		SupportedScopes: []string{oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail,
			oidc.ScopeOfflineAccess, scopeGroups},
		SupportedClaims: []string{"sub", "aud", "exp", "iat", "iss", "auth_time", "nonce", "at_hash", "c_hash",
			"amr", "acr", "name", "given_name", "family_name", "preferred_username", "picture", "locale",
			"updated_at", "email", "email_verified", "groups"},
	}
	opts := []op.Option{op.WithLogger(s.log)}
	if s.opts.PublicURL.Scheme == "http" {
		// Only possible in development: production config requires https.
		opts = append(opts, op.WithAllowInsecure())
	}
	provider, err := op.NewProvider(config, &oidcStorage{s: s}, op.StaticIssuer(s.oidcIssuer()), opts...)
	if err != nil {
		return nil, fmt.Errorf("create OIDC provider: %w", err)
	}
	s.oidcProvider = provider
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+oidc.DiscoveryEndpoint, s.discovery(provider))
	mux.Handle("/", provider)
	return http.StripPrefix(OIDCIssuerPath, mux), nil
}

// discovery serves the provider metadata, corrected to advertise only what
// this provider allows. The library's document is static and also lists
// the implicit flow, device flow and JWT bearer grant, which clients here
// cannot use; a client that believed it would fail in confusing ways.
func (s *Service) discovery(p *op.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := op.ContextWithIssuer(r.Context(), s.oidcIssuer())
		cfg := op.CreateDiscoveryConfig(ctx, p, p.Storage())
		cfg.ResponseTypesSupported = []string{string(oidc.ResponseTypeCode)}
		cfg.ResponseModesSupported = []string{"query"}
		cfg.GrantTypesSupported = []oidc.GrantType{oidc.GrantTypeCode, oidc.GrantTypeRefreshToken}
		cfg.DeviceAuthorizationEndpoint = ""
		cfg.ACRValuesSupported = []string{acrMFA, acrPhishingResistant, acrPhishingResistantHardware}
		op.Discover(w, cfg)
	}
}

// OIDCRequestInfo is what the sign-in page shows about a pending request.
type OIDCRequestInfo struct {
	AppName string `json:"app_name"`
	// PromptNone: the app asked not to show any page; when the user is not
	// signed in, the page must answer login_required at once.
	PromptNone bool `json:"prompt_none"`
}

func (s *Service) pendingRequest(ctx context.Context, id uuid.UUID) (*authRequest, error) {
	a, err := (&oidcStorage{s: s}).loadAuthRequest(ctx, "id = $1 AND user_id IS NULL", id)
	if _, ok := errors.AsType[*oidc.Error](err); ok {
		return nil, ErrNotFound
	}
	return a, err
}

// OIDCRequest describes a pending authorization request.
func (s *Service) OIDCRequest(ctx context.Context, id uuid.UUID) (OIDCRequestInfo, error) {
	a, err := s.pendingRequest(ctx, id)
	if err != nil {
		return OIDCRequestInfo{}, err
	}
	c, err := s.loadClient(ctx, a.req.ClientID)
	if err != nil {
		return OIDCRequestInfo{}, err
	}
	return OIDCRequestInfo{AppName: c.Name, PromptNone: slices.Contains(a.req.Prompt, oidc.PromptNone)}, nil
}

// ApproveOIDCRequest completes a request for the signed-in user and returns
// where the browser goes next (the provider issues the code there).
func (s *Service) ApproveOIDCRequest(ctx context.Context, c Client, sess Session, id uuid.UUID) (string, error) {
	if sess.State != StateAuthenticated {
		return "", ErrNoSession
	}
	a, err := s.pendingRequest(ctx, id)
	if err != nil {
		return "", err
	}
	// prompt=login asks for a sign-in during this request; max_age bounds how
	// long ago the user signed in (OIDC Core §3.1.2.1). max_age=0 means the
	// same as prompt=login, and the library turns prompt=login into it.
	signedInAfter := time.Time{}
	if a.req.MaxAge != nil {
		signedInAfter = s.now().Add(-time.Duration(*a.req.MaxAge) * time.Second)
	}
	if slices.Contains(a.req.Prompt, oidc.PromptLogin) || (a.req.MaxAge != nil && *a.req.MaxAge == 0) {
		signedInAfter = a.createdAt
	}
	if sess.AuthenticatedAt.Before(signedInAfter) {
		return "", ErrReauthRequired
	}
	if !acrSatisfies(sess.ACR, a.req.ACRValues) {
		return "", ErrPasskeyRequired
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := s.mayUseClient(ctx, tx, sess.UserID, a.req.ClientID); err != nil {
			if errors.Is(err, errAccessDenied) {
				return ErrAppAccessDenied
			}
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE identity.oidc_auth_requests SET user_id = $2, auth_time = $3, amr = $4, acr = $5
			WHERE id = $1 AND user_id IS NULL`, id, sess.UserID, sess.AuthenticatedAt, sess.AMR, sess.ACR)
		if err != nil {
			return fmt.Errorf("complete auth request: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO identity.oidc_grants (user_id, client_id, first_login_at, last_login_at) VALUES ($1, $2, $3, $3)
			ON CONFLICT (user_id, client_id) DO UPDATE SET last_login_at = EXCLUDED.last_login_at`,
			sess.UserID, a.req.ClientID, s.now()); err != nil {
			return fmt.Errorf("record app sign-in: %w", err)
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventOIDCLogin, actor: &sess.UserID, details: map[string]any{"app": a.req.ClientID}})
	})
	if errors.Is(err, ErrAppAccessDenied) {
		s.auditBestEffort(ctx, c, auditEntry{event: EventOIDCDenied, subject: &sess.UserID, details: map[string]any{"app": a.req.ClientID}})
	}
	if err != nil {
		return "", err
	}
	return s.oidcIssuer() + "/authorize/callback?id=" + id.String(), nil
}

func (s *Service) oidcRoutes(r chi.Router) {
	r.Get("/requests/{id}", s.handleOIDCRequest)
	r.With(s.RequireUser).Post("/requests/{id}/approve", s.handleOIDCApprove)
	r.Get("/requests/{id}/deny", s.handleOIDCDeny)
	r.Get("/logout/{id}", s.handleLogoutRequest)
}

func (s *Service) handleOIDCRequest(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var info OIDCRequestInfo
	if err == nil {
		info, err = s.OIDCRequest(r.Context(), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, info)
}

func (s *Service) handleOIDCApprove(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var redirect string
	if err == nil {
		p, _ := PrincipalFrom(r.Context())
		redirect, err = s.ApproveOIDCRequest(r.Context(), clientFrom(r), p.Session, id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Redirect string `json:"redirect"`
	}{redirect})
}

// handleOIDCDeny ends a request without signing in: the browser is sent
// back to the app with an OAuth error. It is a navigation, not an API call.
func (s *Service) handleOIDCDeny(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var a *authRequest
	if err == nil {
		a, err = s.pendingRequest(r.Context(), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	reason := oidc.ErrAccessDenied()
	if r.URL.Query().Get("error") == "login_required" {
		reason = oidc.ErrLoginRequired()
	}
	if err := (&oidcStorage{s: s}).DeleteAuthRequest(r.Context(), id.String()); err != nil {
		s.writeError(w, r, err)
		return
	}
	op.AuthRequestError(w, r, a, reason, s.oidcProvider)
}
