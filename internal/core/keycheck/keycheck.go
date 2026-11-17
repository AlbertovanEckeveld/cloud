// Package keycheck makes sure the platform runs with the ENCRYPTION_KEY its
// database was set up with. Without this check a replaced key goes unnoticed
// until something sealed with the old key is needed (a TOTP secret at the
// next sign-in), while new values are already sealed with the new key.
package keycheck

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/AlbertovanEckeveld/cloud/internal/core/sealer"
)

// ErrMismatch means ENCRYPTION_KEY is not the key this database uses.
var ErrMismatch = errors.New("ENCRYPTION_KEY does not match the key this database was set up with; " +
	"restore the original key (data sealed with it cannot be read otherwise)")

// canary is the fixed plaintext; associatedData binds it to this purpose.
var (
	canary         = []byte("cloud key canary v1")
	associatedData = []byte("core:key-canary")
)

// DB is what Verify needs: a pool, a connection or a transaction.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Verify stores the canary on first use and otherwise checks that the
// sealer opens it. It returns ErrMismatch for a different key.
func Verify(ctx context.Context, db DB, s *sealer.Sealer) error {
	// Concurrent first starts both try to insert; exactly one row survives,
	// and the loser then verifies the winner's value like any later start.
	if _, err := db.Exec(ctx, `INSERT INTO core.key_canary (sealed) VALUES ($1) ON CONFLICT DO NOTHING`,
		s.Seal(canary, associatedData)); err != nil {
		return fmt.Errorf("store key canary: %w", err)
	}
	var sealed []byte
	if err := db.QueryRow(ctx, `SELECT sealed FROM core.key_canary`).Scan(&sealed); err != nil {
		return fmt.Errorf("load key canary: %w", err)
	}
	plain, err := s.Open(sealed, associatedData)
	if err != nil || !bytes.Equal(plain, canary) {
		return ErrMismatch
	}
	return nil
}
