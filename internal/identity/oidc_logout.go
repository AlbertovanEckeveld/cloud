package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/AlbertovanEckeveld/cloud/internal/core/httpserver"
)

const (
	logoutTokenTTL       = 2 * time.Minute
	logoutRequestTTL     = 10 * time.Minute
	logoutPollInterval   = 5 * time.Second
	logoutMaxAttempts    = 8
	logoutDeliverTimeout = 10 * time.Second
	backchannelEvent     = "http://schemas.openid.net/event/backchannel-logout"
)

// AppGrant is an app a user has signed in to.
type AppGrant struct {
	ClientID     string    `json:"client_id"`
	Name         string    `json:"name"`
	FirstLoginAt time.Time `json:"first_login_at"`
	LastLoginAt  time.Time `json:"last_login_at"`
}

// AppGrants lists the apps a user has signed in to, most recent first.
func (s *Service) AppGrants(ctx context.Context, userID uuid.UUID) ([]AppGrant, error) {
	rows, err := s.db.Query(ctx, `
		SELECT g.client_id, c.name, g.first_login_at, g.last_login_at
		FROM identity.oidc_grants g JOIN identity.oidc_clients c ON c.id = g.client_id
		WHERE g.user_id = $1 AND g.client_id <> $2 -- the desktop app is listed per device
		ORDER BY g.last_login_at DESC`, userID, DesktopClientID)
	if err != nil {
		return nil, fmt.Errorf("list app grants: %w", err)
	}
	grants, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AppGrant, error) {
		var g AppGrant
		err := row.Scan(&g.ClientID, &g.Name, &g.FirstLoginAt, &g.LastLoginAt)
		g.FirstLoginAt, g.LastLoginAt = g.FirstLoginAt.UTC(), g.LastLoginAt.UTC()
		return g, err
	})
	if err != nil {
		return nil, fmt.Errorf("read app grants: %w", err)
	}
	return grants, nil
}

// revokeAppAccess ends a user's access to one app (clientID) or to all apps
// (clientID nil): their tokens are deleted and every affected app with a
// back-channel logout URI is told, so it can end its own session too.
// skipNotify names an app that already knows, such as the one that asked
// for the logout.
func (s *Service) revokeAppAccess(ctx context.Context, tx pgx.Tx, userID uuid.UUID, clientID *string, skipNotify string) error {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT c.id, c.backchannel_logout_uri IS NOT NULL
		FROM identity.oidc_clients c
		WHERE ($2::text IS NULL OR c.id = $2) AND (
			EXISTS (SELECT 1 FROM identity.oidc_grants g WHERE g.client_id = c.id AND g.user_id = $1) OR
			EXISTS (SELECT 1 FROM identity.oidc_refresh_tokens r WHERE r.client_id = c.id AND r.user_id = $1) OR
			EXISTS (SELECT 1 FROM identity.oidc_access_tokens a WHERE a.client_id = c.id AND a.user_id = $1))`,
		userID, clientID)
	if err != nil {
		return fmt.Errorf("find signed-in apps: %w", err)
	}
	type app struct {
		id     string
		notify bool
	}
	apps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (app, error) {
		var a app
		return a, row.Scan(&a.id, &a.notify)
	})
	if err != nil {
		return fmt.Errorf("find signed-in apps: %w", err)
	}
	for _, a := range apps {
		for _, q := range []string{
			`DELETE FROM identity.oidc_refresh_tokens WHERE user_id = $1 AND client_id = $2`,
			`DELETE FROM identity.oidc_access_tokens WHERE user_id = $1 AND client_id = $2`,
			`DELETE FROM identity.oidc_grants WHERE user_id = $1 AND client_id = $2`,
		} {
			if _, err := tx.Exec(ctx, q, userID, a.id); err != nil {
				return fmt.Errorf("revoke app access: %w", err)
			}
		}
		if !a.notify || a.id == skipNotify {
			continue
		}
		token, err := s.logoutToken(ctx, a.id, userID)
		if err != nil {
			return err
		}
		now := s.now()
		if _, err := tx.Exec(ctx, `
			INSERT INTO identity.oidc_logout_outbox (id, client_id, logout_token, created_at, next_attempt_at)
			VALUES ($1, $2, $3, $4, $4)`, newID(), a.id, token, now); err != nil {
			return fmt.Errorf("queue logout token: %w", err)
		}
	}
	return nil
}

// logoutToken builds a signed logout token (OIDC Back-Channel Logout 1.0
// §2.4): like an ID token, but with an events claim, never a nonce, and its
// own JWT type so it cannot be mistaken for one.
func (s *Service) logoutToken(ctx context.Context, clientID string, userID uuid.UUID) (string, error) {
	keys, err := s.publishedKeys(ctx)
	if err != nil || len(keys) == 0 {
		return "", fmt.Errorf("no signing key for logout token: %w", err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: keys[0].key, KeyID: keys[0].id}},
		(&jose.SignerOptions{}).WithType("logout+jwt"))
	if err != nil {
		return "", fmt.Errorf("logout token signer: %w", err)
	}
	now := s.now()
	claims, err := json.Marshal(map[string]any{
		"iss":    s.oidcIssuer(),
		"aud":    clientID,
		"sub":    userID.String(),
		"iat":    now.Unix(),
		"exp":    now.Add(logoutTokenTTL).Unix(),
		"jti":    newID().String(),
		"events": map[string]any{backchannelEvent: map[string]any{}},
	})
	if err != nil {
		return "", err
	}
	signed, err := signer.Sign(claims)
	if err != nil {
		return "", fmt.Errorf("sign logout token: %w", err)
	}
	return signed.CompactSerialize()
}

// deliverLogouts sends queued logout tokens until ctx is cancelled.
func (s *Service) deliverLogouts(ctx context.Context) {
	ticker := time.NewTicker(logoutPollInterval)
	defer ticker.Stop()
	for {
		if err := s.deliverLogoutBatch(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("logout token delivery failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// logoutHTTP posts logout tokens. It never follows redirects: the token is
// for the registered URI only.
var logoutHTTP = &http.Client{
	Timeout:       logoutDeliverTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// deliverLogoutBatch sends due logout tokens, claiming them with
// FOR UPDATE SKIP LOCKED so several instances never send one twice.
func (s *Service) deliverLogoutBatch(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT o.id, o.attempts, o.logout_token, c.backchannel_logout_uri
			FROM identity.oidc_logout_outbox o JOIN identity.oidc_clients c ON c.id = o.client_id
			WHERE o.next_attempt_at <= $1 AND c.backchannel_logout_uri IS NOT NULL
			ORDER BY o.created_at LIMIT 20 FOR UPDATE OF o SKIP LOCKED`, s.now())
		if err != nil {
			return fmt.Errorf("claim logout tokens: %w", err)
		}
		type item struct {
			id       uuid.UUID
			attempts int
			token    string
			uri      string
		}
		items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (item, error) {
			var i item
			return i, row.Scan(&i.id, &i.attempts, &i.token, &i.uri)
		})
		if err != nil {
			return fmt.Errorf("claim logout tokens: %w", err)
		}
		for _, it := range items {
			sendErr := postLogoutToken(ctx, it.uri, it.token)
			attempts := it.attempts + 1
			switch {
			case sendErr == nil || attempts >= logoutMaxAttempts:
				if sendErr != nil {
					s.log.Error("logout token dropped after repeated failures", "id", it.id, "error", sendErr)
				}
				_, err = tx.Exec(ctx, `DELETE FROM identity.oidc_logout_outbox WHERE id = $1`, it.id)
			default:
				backoff := time.Minute << (attempts - 1)
				s.log.Warn("logout token not delivered, will retry", "id", it.id, "retry_in", backoff, "error", sendErr)
				_, err = tx.Exec(ctx, `UPDATE identity.oidc_logout_outbox SET attempts = $2, next_attempt_at = $3, last_error = $4 WHERE id = $1`,
					it.id, attempts, s.now().Add(backoff), truncate(sendErr.Error(), 500))
			}
			if err != nil {
				return fmt.Errorf("update logout outbox: %w", err)
			}
		}
		return nil
	})
}

func postLogoutToken(ctx context.Context, uri, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uri, strings.NewReader(url.Values{"logout_token": {token}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := logoutHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("app answered %s", resp.Status)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// --- RP-initiated logout ---

var _ op.CanTerminateSessionFromRequest = (*oidcStorage)(nil)

// TerminateSessionFromRequest handles a logout started by an app
// (end_session): that app's tokens are revoked, and the browser goes to our
// page where the user decides whether to sign out of Cloud as well.
func (st *oidcStorage) TerminateSessionFromRequest(ctx context.Context, req *op.EndSessionRequest) (string, error) {
	s := st.s
	page := s.opts.PublicURL.String() + "/signed-out"
	if req.ClientID == "" {
		return page, nil
	}
	id := newID()
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if userID, err := uuid.Parse(req.UserID); err == nil {
			if err := s.revokeAppAccess(ctx, tx, userID, &req.ClientID, req.ClientID); err != nil {
				return err
			}
		}
		// The redirect URI was validated against the app's registered list
		// by the library; we keep it so the page never takes it from a URL.
		_, err := tx.Exec(ctx, `
			INSERT INTO identity.oidc_logout_requests (id, client_id, redirect_uri, expires_at) VALUES ($1, $2, $3, $4)`,
			id, req.ClientID, req.RedirectURI, s.now().Add(logoutRequestTTL))
		return err
	})
	if err != nil {
		return "", fmt.Errorf("record logout: %w", err)
	}
	return page + "?request=" + id.String(), nil
}

// TerminateSession is required by the storage interface; logout goes
// through TerminateSessionFromRequest instead.
func (st *oidcStorage) TerminateSession(ctx context.Context, userID, clientID string) error {
	id, err := uuid.Parse(userID)
	if err != nil {
		return nil
	}
	return pgx.BeginFunc(ctx, st.s.db, func(tx pgx.Tx) error {
		return st.s.revokeAppAccess(ctx, tx, id, &clientID, clientID)
	})
}

// LogoutRequest is what the signed-out page shows.
type LogoutRequest struct {
	AppName  string `json:"app_name"`
	Redirect string `json:"redirect"`
}

func (s *Service) logoutRequest(ctx context.Context, id uuid.UUID) (LogoutRequest, error) {
	var lr LogoutRequest
	err := s.db.QueryRow(ctx, `
		SELECT c.name, l.redirect_uri FROM identity.oidc_logout_requests l JOIN identity.oidc_clients c ON c.id = l.client_id
		WHERE l.id = $1 AND l.expires_at > $2`, id, s.now()).Scan(&lr.AppName, &lr.Redirect)
	if errors.Is(err, pgx.ErrNoRows) {
		return LogoutRequest{}, ErrNotFound
	}
	return lr, err
}

// LogoutEverywhere ends the user's current Cloud session and their access to
// every app, notifying the apps that support back-channel logout.
func (s *Service) LogoutEverywhere(ctx context.Context, c Client, sess Session) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := s.revokeAppAccess(ctx, tx, sess.UserID, nil, ""); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM identity.sessions WHERE id = $1`, sess.ID); err != nil {
			return fmt.Errorf("delete session: %w", err)
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventLogoutEverywhere, actor: &sess.UserID})
	})
}

// RevokeApp ends the user's access to one app.
func (s *Service) RevokeApp(ctx context.Context, c Client, sess Session, clientID string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := s.revokeAppAccess(ctx, tx, sess.UserID, &clientID, ""); err != nil {
			return err
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventAppRevoked, actor: &sess.UserID, details: map[string]any{"app": clientID}})
	})
}

func (s *Service) handleLogoutRequest(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var lr LogoutRequest
	if err == nil {
		lr, err = s.logoutRequest(r.Context(), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, lr)
}

func (s *Service) handleAppGrants(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	grants, err := s.AppGrants(r.Context(), p.User.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Apps []AppGrant `json:"apps"`
	}{grants})
}

func (s *Service) handleRevokeApp(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	if err := s.RevokeApp(r.Context(), clientFrom(r), p.Session, chi.URLParam(r, "clientID")); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleLogoutEverywhere(w http.ResponseWriter, r *http.Request, sess Session, _ User) {
	if err := s.LogoutEverywhere(r.Context(), clientFrom(r), sess); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}
