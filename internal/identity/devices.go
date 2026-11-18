package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
		u, deviceID, err := s.authenticateDevice(r.Context(), strings.TrimPrefix(auth, "Bearer "))
		if err != nil {
			if !errors.Is(err, ErrNoSession) {
				s.log.Error("device token check failed", "request_id", httpserver.RequestIDFrom(r.Context()), "error", err)
			}
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			httpserver.WriteError(w, http.StatusUnauthorized, "unauthenticated", "Dit apparaat is niet (meer) gekoppeld.")
			return
		}
		ctx := context.WithValue(r.Context(), principalKey{}, Principal{User: u, Device: true, DeviceID: deviceID})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authenticateDevice checks an access token issued to the desktop app: the
// OIDC provider's "tokenID:subject", encrypted with AES-256-GCM. It is only
// trusted when the ID names a live token in the database for exactly this
// subject and client, so revoking (or disabling the user) works at once.
// It returns the user and the device: the refresh token the access token
// was issued with.
func (s *Service) authenticateDevice(ctx context.Context, token string) (User, uuid.UUID, error) {
	plain, err := op.NewAES256GCMCrypto(s.opts.OIDCTokenKey, "").Decrypt(token)
	if err != nil {
		return User{}, uuid.Nil, ErrNoSession
	}
	tokenID, subject, ok := strings.Cut(plain, ":")
	id, err1 := uuid.Parse(tokenID)
	sub, err2 := uuid.Parse(subject)
	if !ok || err1 != nil || err2 != nil {
		return User{}, uuid.Nil, ErrNoSession
	}
	var u User
	var device *uuid.UUID
	now := s.now()
	err = s.db.QueryRow(ctx, `
		SELECT u.id, u.username, u.display_name, u.is_admin, COALESCE(u.email, ''), t.refresh_token_id
		FROM identity.oidc_access_tokens t JOIN identity.users u ON u.id = t.user_id
		WHERE t.id = $1 AND t.user_id = $2 AND t.client_id = $3 AND t.expires_at > $4 AND u.disabled_at IS NULL`,
		id, sub, DesktopClientID, now).Scan(&u.ID, &u.Username, &u.DisplayName, &u.IsAdmin, &u.Email, &device)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, uuid.Nil, ErrNoSession
	}
	if err != nil {
		return User{}, uuid.Nil, fmt.Errorf("check device token: %w", err)
	}
	if device == nil {
		return u, uuid.Nil, nil
	}
	// "Last seen" for the device list; at most one write a minute per device.
	if _, err := s.db.Exec(ctx, `
		UPDATE identity.oidc_refresh_tokens SET last_used_at = $2
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < $2::timestamptz - interval '1 minute')`, *device, now); err != nil {
		return User{}, uuid.Nil, fmt.Errorf("record device use: %w", err)
	}
	return u, *device, nil
}

// Device is a linked desktop app as its owner sees it.
type Device struct {
	ID         uuid.UUID  `json:"id"`
	Name       *string    `json:"name"`     // reported by the app; nil until it has
	Platform   *string    `json:"platform"` // linux, darwin or windows
	LinkedAt   time.Time  `json:"linked_at"`
	LastSeenAt *time.Time `json:"last_seen_at"`
}

const maxDeviceName = 64

var devicePlatforms = map[string]bool{"linux": true, "darwin": true, "windows": true}

// ReportDevice stores the name and platform a linked desktop app reports
// about itself. Both are shown to the user only, never trusted for anything.
func (s *Service) ReportDevice(ctx context.Context, userID, deviceID uuid.UUID, name, platform string) error {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n == 0 || n > maxDeviceName || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return &InputError{Reason: "Geef het apparaat een naam van 1 tot 64 tekens."}
	}
	if !devicePlatforms[platform] {
		return &InputError{Reason: "Onbekend besturingssysteem."}
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE identity.oidc_refresh_tokens SET device_name = $3, device_platform = $4
		WHERE id = $1 AND user_id = $2 AND client_id = $5`, deviceID, userID, name, platform, DesktopClientID)
	if err != nil {
		return fmt.Errorf("report device: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

// Devices lists the user's linked desktop apps, most recently used first.
func (s *Service) Devices(ctx context.Context, userID uuid.UUID) ([]Device, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, device_name, device_platform, created_at, last_used_at
		FROM identity.oidc_refresh_tokens
		WHERE user_id = $1 AND client_id = $2 AND expires_at > $3
		ORDER BY COALESCE(last_used_at, created_at) DESC, id DESC`, userID, DesktopClientID, s.now())
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	devices, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Device, error) {
		var d Device
		err := row.Scan(&d.ID, &d.Name, &d.Platform, &d.LinkedAt, &d.LastSeenAt)
		d.LinkedAt = d.LinkedAt.UTC()
		if d.LastSeenAt != nil {
			t := d.LastSeenAt.UTC()
			d.LastSeenAt = &t
		}
		return d, err
	})
	if err != nil {
		return nil, fmt.Errorf("read devices: %w", err)
	}
	return devices, nil
}

// UnlinkDevice ends one desktop app's access at once: its refresh token and
// every access token issued with it are deleted. The app notices at its
// next request and asks to sign in again.
func (s *Service) UnlinkDevice(ctx context.Context, c Client, userID, deviceID uuid.UUID) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var name *string
		err := tx.QueryRow(ctx, `
			DELETE FROM identity.oidc_refresh_tokens
			WHERE id = $1 AND user_id = $2 AND client_id = $3
			RETURNING device_name`, deviceID, userID, DesktopClientID).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("unlink device: %w", err)
		}
		details := map[string]any{"device": deviceID.String()}
		if name != nil {
			details["name"] = *name
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventDeviceUnlinked, actor: &userID, details: details})
	})
}

func (s *Service) handleReportDevice(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	if !p.Device || p.DeviceID == uuid.Nil {
		httpserver.WriteError(w, http.StatusForbidden, "forbidden", "Alleen de desktop-app kan zichzelf aanmelden.")
		return
	}
	var in struct {
		Name     string `json:"name"`
		Platform string `json:"platform"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	if err := s.ReportDevice(r.Context(), p.User.ID, p.DeviceID, in.Name, in.Platform); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleDevices(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	devices, err := s.Devices(r.Context(), p.User.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Devices []Device `json:"devices"`
	}{devices})
}

func (s *Service) handleUnlinkDevice(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	id, err := pathID(r)
	if err == nil {
		err = s.UnlinkDevice(r.Context(), clientFrom(r), p.User.ID, id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
