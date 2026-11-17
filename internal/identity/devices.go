package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/AlbertovanEckeveld/cloud/internal/core/httpserver"
)

// DesktopClientID is the OIDC client of the Cloud desktop app. Only its
// access tokens are accepted by the drive API (RequireUserOrDevice).
const DesktopClientID = "cloud-desktop"

// RequireUserOrDevice accepts a signed-in browser session or the access
// token of the desktop app (Authorization: Bearer). A device principal may
// use the drive API, but never administration or actions that need a recent
// sign-in: those check Principal.Device.
func (s *Service) RequireUserOrDevice(next http.Handler) http.Handler {
	session := s.RequireUser(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			session.ServeHTTP(w, r)
			return
		}
		u, err := s.authenticateDevice(r.Context(), strings.TrimPrefix(auth, "Bearer "))
		if err != nil {
			if !errors.Is(err, ErrNoSession) {
				s.log.Error("device token check failed", "request_id", httpserver.RequestIDFrom(r.Context()), "error", err)
			}
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			httpserver.WriteError(w, http.StatusUnauthorized, "unauthenticated", "Dit apparaat is niet (meer) gekoppeld.")
			return
		}
		ctx := context.WithValue(r.Context(), principalKey{}, Principal{User: u, Device: true})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authenticateDevice checks an access token issued to the desktop app: the
// OIDC provider's "tokenID:subject", encrypted with AES-256-GCM. It is only
// trusted when the ID names a live token in the database for exactly this
// subject and client, so revoking (or disabling the user) works at once.
func (s *Service) authenticateDevice(ctx context.Context, token string) (User, error) {
	plain, err := op.NewAES256GCMCrypto(s.opts.OIDCTokenKey, "").Decrypt(token)
	if err != nil {
		return User{}, ErrNoSession
	}
	tokenID, subject, ok := strings.Cut(plain, ":")
	id, err1 := uuid.Parse(tokenID)
	sub, err2 := uuid.Parse(subject)
	if !ok || err1 != nil || err2 != nil {
		return User{}, ErrNoSession
	}
	var u User
	err = s.db.QueryRow(ctx, `
		SELECT u.id, u.username, u.display_name, u.is_admin, COALESCE(u.email, '')
		FROM identity.oidc_access_tokens t JOIN identity.users u ON u.id = t.user_id
		WHERE t.id = $1 AND t.user_id = $2 AND t.client_id = $3 AND t.expires_at > $4 AND u.disabled_at IS NULL`,
		id, sub, DesktopClientID, s.now()).Scan(&u.ID, &u.Username, &u.DisplayName, &u.IsAdmin, &u.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNoSession
	}
	if err != nil {
		return User{}, fmt.Errorf("check device token: %w", err)
	}
	return u, nil
}
