package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	recoveryCodeCount = 10
	recoveryCodeBytes = 10 // 80 bits: brute force is infeasible even against the stored hash
)

// newRecoveryCode returns a code like "k4m2-x7qa-p9rt-b3wn".
func newRecoveryCode() string {
	b := make([]byte, recoveryCodeBytes)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	s := strings.ToLower(base32NoPad.EncodeToString(b)) // 16 characters
	return s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
}

// hashRecoveryCode normalises what a user typed (case, spaces, dashes) and hashes it.
func hashRecoveryCode(code string) []byte {
	normalized := strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, strings.ToLower(strings.TrimSpace(code)))
	sum := sha256.Sum256([]byte(normalized))
	return sum[:]
}

// GenerateRecoveryCodes replaces all recovery codes of the user with new
// ones. The codes are returned once and only their hashes are kept.
func (s *Service) GenerateRecoveryCodes(ctx context.Context, c Client, sess Session) ([]string, error) {
	if err := s.requireRecentAuth(sess); err != nil {
		return nil, err
	}
	codes := make([]string, recoveryCodeCount)
	for i := range codes {
		codes[i] = newRecoveryCode()
	}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		now := s.now()
		if _, err := tx.Exec(ctx, `DELETE FROM identity.recovery_codes WHERE user_id = $1`, sess.UserID); err != nil {
			return fmt.Errorf("delete old recovery codes: %w", err)
		}
		for _, code := range codes {
			if _, err := tx.Exec(ctx, `INSERT INTO identity.recovery_codes (user_id, code_hash, created_at) VALUES ($1, $2, $3)`,
				sess.UserID, hashRecoveryCode(code), now); err != nil {
				return fmt.Errorf("store recovery code: %w", err)
			}
		}
		if err := s.audit(ctx, tx, c, auditEntry{event: EventRecoveryCodesGenerated, actor: &sess.UserID}); err != nil {
			return err
		}
		return s.notify(ctx, tx, c, sess.UserID, noticeRecoveryNew)
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// RemainingRecoveryCodes counts the unused recovery codes of a user.
func (s *Service) RemainingRecoveryCodes(ctx context.Context, userID uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM identity.recovery_codes WHERE user_id = $1 AND used_at IS NULL`, userID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count recovery codes: %w", err)
	}
	return n, nil
}

// VerifyRecoveryCode completes a login with a recovery code instead of a
// TOTP code. The code is used up. Wrong codes count towards the same limit
// as wrong TOTP codes.
func (s *Service) VerifyRecoveryCode(ctx context.Context, c Client, sess Session, code string) (string, error) {
	if sess.State != StateMFARequired {
		return "", ErrWrongState
	}
	key := throttleTOTP.key(sess.UserID.String())
	if err := s.checkThrottle(ctx, key); err != nil {
		return "", err
	}
	var token string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE identity.recovery_codes SET used_at = $3
			WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL`, sess.UserID, hashRecoveryCode(code), s.now())
		if err != nil {
			return fmt.Errorf("use recovery code: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrInvalidCode
		}
		if token, err = s.rotateSession(ctx, tx, sess.ID, StateAuthenticated, sessionMaxTTL); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, c, auditEntry{event: EventRecoveryCodeUsed, actor: &sess.UserID}); err != nil {
			return err
		}
		if err := s.notify(ctx, tx, c, sess.UserID, noticeRecoveryUsed); err != nil {
			return err
		}
		return s.recordLogin(ctx, tx, c, sess.UserID, "password+recovery_code")
	})
	if errors.Is(err, ErrInvalidCode) {
		s.totpFailed(ctx, c, sess, key)
		return "", ErrInvalidCode
	}
	if err != nil {
		return "", err
	}
	if err := s.clearThrottle(ctx, key); err != nil {
		s.log.Warn("clear totp throttle", "error", err)
	}
	return token, nil
}
