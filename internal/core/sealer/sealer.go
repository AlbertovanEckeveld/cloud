// Package sealer encrypts small secrets for storage at rest (for example TOTP
// secrets in the database) with AES-256-GCM from the standard library.
package sealer

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// version is the first byte of every sealed value. It leaves room for key
// rotation or a different algorithm later without ambiguity.
const version byte = 1

// ErrOpen is returned for any ciphertext that cannot be decrypted: wrong key,
// wrong associated data, or tampering. The causes are deliberately not
// distinguished.
var ErrOpen = errors.New("sealer: cannot open sealed value")

// Sealer encrypts and decrypts with one key.
type Sealer struct {
	aead cipher.AEAD
}

// New returns a sealer for a 32-byte key.
func New(key []byte) (*Sealer, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("sealer: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("sealer: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("sealer: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

// Seal encrypts plaintext. associatedData is authenticated but not stored: it
// binds the ciphertext to its context (such as the owning user ID and the
// purpose), so a sealed value copied to another row cannot be opened there.
func (s *Sealer) Seal(plaintext, associatedData []byte) []byte {
	// A random 96-bit nonce is safe for far more values than this platform
	// will ever seal with one key (NIST SP 800-38D §8.3 limit: 2^32).
	out := make([]byte, 1+s.aead.NonceSize(), 1+s.aead.NonceSize()+len(plaintext)+s.aead.Overhead())
	out[0] = version
	nonce := out[1:]
	if _, err := rand.Read(nonce); err != nil {
		panic("sealer: crypto/rand failed: " + err.Error()) // never happens on supported platforms
	}
	return s.aead.Seal(out, nonce, plaintext, associatedData)
}

// Open decrypts a value produced by Seal with the same associatedData.
func (s *Sealer) Open(sealed, associatedData []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < 1+n+s.aead.Overhead() || sealed[0] != version {
		return nil, ErrOpen
	}
	plaintext, err := s.aead.Open(nil, sealed[1:1+n], sealed[1+n:], associatedData)
	if err != nil {
		return nil, ErrOpen
	}
	return plaintext, nil
}
