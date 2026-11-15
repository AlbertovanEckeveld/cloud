package identity

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Password policy follows NIST SP 800-63B: a minimum length, no composition
// rules. The maximum keeps hashing cost bounded.
const (
	minPasswordLength = 12  // characters
	maxPasswordBytes  = 512 // bytes
)

// argonParams are Argon2id cost parameters. The defaults are the second
// recommended option of RFC 9106 §4 (64 MiB memory, 3 passes, 4 lanes).
type argonParams struct {
	memory  uint32 // KiB
	time    uint32
	threads uint8
}

var defaultArgonParams = argonParams{memory: 64 * 1024, time: 3, threads: 4}

const (
	argonSaltLen = 16
	argonKeyLen  = 32
)

// PolicyError explains why a new password was rejected; its message is safe to show to users.
type PolicyError struct{ Reason string }

func (e *PolicyError) Error() string { return e.Reason }

func checkPasswordPolicy(password, username string) error {
	switch {
	case len(password) > maxPasswordBytes:
		return &PolicyError{"Het wachtwoord is te lang."}
	case !utf8.ValidString(password):
		return &PolicyError{"Het wachtwoord bevat ongeldige tekens."}
	case utf8.RuneCountInString(password) < minPasswordLength:
		return &PolicyError{fmt.Sprintf("Het wachtwoord moet minstens %d tekens lang zijn.", minPasswordLength)}
	case strings.EqualFold(password, username):
		return &PolicyError{"Het wachtwoord mag niet gelijk zijn aan de gebruikersnaam."}
	}
	return nil
}

// passwordHasher hashes and verifies passwords. Argon2id is memory-hard on
// purpose, so the number of concurrent hashes is capped: otherwise a burst of
// login attempts could exhaust the server's memory.
type passwordHasher struct {
	params argonParams
	slots  chan struct{}
	// dummy is verified when a user does not exist, so that response time
	// does not reveal whether a username is valid.
	dummy string
}

func newPasswordHasher(params argonParams, concurrency int) *passwordHasher {
	h := &passwordHasher{params: params, slots: make(chan struct{}, concurrency)}
	dummy, err := h.hash(context.Background(), "dummy password for timing equalisation")
	if err != nil {
		panic(err) // only fails if the context is cancelled, which it is not
	}
	h.dummy = dummy
	return h
}

func (h *passwordHasher) acquire(ctx context.Context) error {
	select {
	case h.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *passwordHasher) release() { <-h.slots }

// hash returns an encoded hash in the PHC string format:
// $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
func (h *passwordHasher) hash(ctx context.Context, password string) (string, error) {
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()

	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	p := h.params
	key := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memory, p.time, p.threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

var errMalformedHash = errors.New("malformed password hash")

// verify reports whether password matches encoded, and whether the hash
// should be upgraded because it was made with other parameters.
func (h *passwordHasher) verify(ctx context.Context, password, encoded string) (match, rehash bool, err error) {
	p, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, false, err
	}
	defer h.release()

	got := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, uint32(len(want)))
	match = subtle.ConstantTimeCompare(got, want) == 1
	return match, match && p != h.params, nil
}

// verifyDummy spends the same effort as a real verification and always fails.
func (h *passwordHasher) verifyDummy(ctx context.Context, password string) {
	_, _, _ = h.verify(ctx, password, h.dummy)
}

func decodeHash(encoded string) (argonParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return argonParams{}, nil, nil, errMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return argonParams{}, nil, nil, errMalformedHash
	}
	var p argonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return argonParams{}, nil, nil, errMalformedHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return argonParams{}, nil, nil, errMalformedHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < 16 {
		return argonParams{}, nil, nil, errMalformedHash
	}
	return p, salt, key, nil
}
