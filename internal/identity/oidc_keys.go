package identity

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"fmt"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/jackc/pgx/v5"
)

const (
	// RS256 is the one algorithm every OpenID Connect client must support
	// (OIDC Core §15.1); 3072-bit RSA follows current key-size guidance.
	oidcKeyBits = 3072
	// A new key is made after keyRotation; older keys stay published for
	// keyRetention so tokens signed just before a rotation still verify.
	keyRotation  = 90 * 24 * time.Hour
	keyRetention = keyRotation + 24*time.Hour
	// keyLockID serialises key creation across instances (an advisory lock key).
	keyLockID int64 = 0x6f6964636b657973 // "oidckeys"
)

type signingKey struct {
	id  string
	key *rsa.PrivateKey
}

func (k signingKey) SignatureAlgorithm() jose.SignatureAlgorithm { return jose.RS256 }
func (k signingKey) Key() any                                    { return k.key }
func (k signingKey) ID() string                                  { return k.id }

// publicKey is a verification key as published in the JWKS.
type publicKey struct {
	id  string
	key *rsa.PublicKey
}

func (k publicKey) ID() string                         { return k.id }
func (k publicKey) Algorithm() jose.SignatureAlgorithm { return jose.RS256 }
func (k publicKey) Use() string                        { return "sig" }
func (k publicKey) Key() any                           { return k.key }

func keyAD(id string) []byte { return []byte("oidc-key:" + id) }

// signingKeys returns the usable keys created after since, newest first.
// A key that cannot be decrypted (for example after ENCRYPTION_KEY was
// replaced) is skipped and reported, rather than breaking every sign-in.
func (s *Service) signingKeys(ctx context.Context, q dbtx, since time.Time) ([]signingKey, error) {
	rows, err := q.Query(ctx, `
		SELECT id, private_key FROM identity.oidc_signing_keys
		WHERE created_at > $1 ORDER BY created_at DESC`, since)
	if err != nil {
		return nil, fmt.Errorf("load signing keys: %w", err)
	}
	type stored struct {
		id     string
		sealed []byte
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (stored, error) {
		var k stored
		return k, row.Scan(&k.id, &k.sealed)
	})
	if err != nil {
		return nil, fmt.Errorf("load signing keys: %w", err)
	}
	var keys []signingKey
	for _, k := range all {
		key, err := s.openSigningKey(k.id, k.sealed)
		if err != nil {
			s.log.Error("unusable OIDC signing key skipped", "kid", k.id, "error", err)
			continue
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func (s *Service) openSigningKey(id string, sealed []byte) (signingKey, error) {
	der, err := s.sealer.Open(sealed, keyAD(id))
	if err != nil {
		return signingKey{}, err
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return signingKey{}, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return signingKey{}, fmt.Errorf("not an RSA key")
	}
	return signingKey{id: id, key: key}, nil
}

// publishedKeys are the keys tokens may have been signed with.
func (s *Service) publishedKeys(ctx context.Context) ([]signingKey, error) {
	return s.signingKeys(ctx, s.db, s.now().Add(-keyRetention))
}

// ensureSigningKey creates a key when none is younger than keyRotation.
func (s *Service) ensureSigningKey(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, keyLockID); err != nil {
			return fmt.Errorf("lock signing keys: %w", err)
		}
		fresh, err := s.signingKeys(ctx, tx, s.now().Add(-keyRotation))
		if err != nil {
			return err
		}
		if len(fresh) > 0 {
			return nil
		}
		key, err := rsa.GenerateKey(rand.Reader, oidcKeyBits)
		if err != nil {
			return fmt.Errorf("generate signing key: %w", err)
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return fmt.Errorf("encode signing key: %w", err)
		}
		id := newID().String()
		if _, err := tx.Exec(ctx, `INSERT INTO identity.oidc_signing_keys (id, private_key, created_at) VALUES ($1, $2, $3)`,
			id, s.sealer.Seal(der, keyAD(id)), s.now()); err != nil {
			return fmt.Errorf("store signing key: %w", err)
		}
		s.log.Info("new OIDC signing key", "kid", id)
		return nil
	})
}
