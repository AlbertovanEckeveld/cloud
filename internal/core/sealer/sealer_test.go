package sealer

import (
	"bytes"
	"errors"
	"testing"
)

func newSealer(t *testing.T, fill byte) *Sealer {
	t.Helper()
	s, err := New(bytes.Repeat([]byte{fill}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRoundTrip(t *testing.T) {
	s := newSealer(t, 1)
	sealed := s.Seal([]byte("totp secret"), []byte("user-1"))
	got, err := s.Open(sealed, []byte("user-1"))
	if err != nil || string(got) != "totp secret" {
		t.Fatalf("Open = %q, %v", got, err)
	}
}

func TestSealIsRandomized(t *testing.T) {
	s := newSealer(t, 1)
	if bytes.Equal(s.Seal([]byte("x"), nil), s.Seal([]byte("x"), nil)) {
		t.Fatal("two seals of the same value must differ (random nonce)")
	}
}

func TestOpenRejects(t *testing.T) {
	s := newSealer(t, 1)
	sealed := s.Seal([]byte("secret"), []byte("user-1"))

	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 1
	wrongVersion := bytes.Clone(sealed)
	wrongVersion[0] = 99

	cases := map[string]struct {
		sealer *Sealer
		value  []byte
		ad     string
	}{
		"other associated data": {s, sealed, "user-2"},
		"other key":             {newSealer(t, 2), sealed, "user-1"},
		"tampered":              {s, tampered, "user-1"},
		"unknown version":       {s, wrongVersion, "user-1"},
		"truncated":             {s, sealed[:10], "user-1"},
		"empty":                 {s, nil, "user-1"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := c.sealer.Open(c.value, []byte(c.ad)); !errors.Is(err, ErrOpen) {
				t.Fatalf("err = %v, want ErrOpen", err)
			}
		})
	}
}

func TestNewRequires32ByteKey(t *testing.T) {
	if _, err := New(make([]byte, 16)); err == nil {
		t.Fatal("expected error for 16-byte key")
	}
}
