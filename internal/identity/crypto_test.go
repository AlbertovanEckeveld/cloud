package identity

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image/png"
	"strings"
	"testing"
	"time"
)

// fastArgon keeps tests quick; production parameters are tested separately.
var fastArgon = argonParams{memory: 64, time: 1, threads: 1}

func TestPasswordHashAndVerify(t *testing.T) {
	h := newPasswordHasher(fastArgon, 2)
	ctx := context.Background()

	encoded, err := h.hash(ctx, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("unexpected encoding: %s", encoded)
	}
	if other, _ := h.hash(ctx, "correct horse battery staple"); other == encoded {
		t.Error("hashes of the same password must differ (random salt)")
	}

	if ok, rehash, err := h.verify(ctx, "correct horse battery staple", encoded); !ok || rehash || err != nil {
		t.Errorf("verify correct = %v, %v, %v", ok, rehash, err)
	}
	if ok, _, _ := h.verify(ctx, "wrong password", encoded); ok {
		t.Error("wrong password accepted")
	}
}

func TestPasswordRehashOnParamChange(t *testing.T) {
	ctx := context.Background()
	old, _ := newPasswordHasher(fastArgon, 1).hash(ctx, "a long enough password")
	stronger := newPasswordHasher(argonParams{memory: 128, time: 2, threads: 1}, 1)
	ok, rehash, err := stronger.verify(ctx, "a long enough password", old)
	if !ok || !rehash || err != nil {
		t.Fatalf("verify = %v, rehash = %v, err = %v", ok, rehash, err)
	}
}

func TestPasswordMalformedHash(t *testing.T) {
	h := newPasswordHasher(fastArgon, 1)
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=64,t=1,p=1$c2FsdA$a2V5", "$argon2id$v=18$m=64,t=1,p=1$c2FsdA$a2V5a2V5a2V5a2V5a2V5a2V5"} {
		if _, _, err := h.verify(context.Background(), "x", bad); !errors.Is(err, errMalformedHash) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

func TestPasswordHasherRespectsContext(t *testing.T) {
	h := newPasswordHasher(fastArgon, 1)
	h.slots <- struct{}{} // all slots busy
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := h.hash(ctx, "x"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded while waiting for a slot", err)
	}
}

func TestDefaultArgonParamsFollowRFC9106(t *testing.T) {
	if defaultArgonParams != (argonParams{memory: 65536, time: 3, threads: 4}) {
		t.Fatalf("defaults changed: %+v", defaultArgonParams)
	}
}

func TestPasswordPolicy(t *testing.T) {
	cases := map[string]bool{
		"short":                       false,
		"twelve chars":                true,
		"ééééééééééé":                 false, // 11 characters, 22 bytes
		"éééééééééééé":                true,
		strings.Repeat("a", 513):      false,
		"alberto.van.e":               false, // equals username (case-insensitive)
		"a perfectly fine passphrase": true,
	}
	for pw, want := range cases {
		err := checkPasswordPolicy(pw, "Alberto.van.E")
		if (err == nil) != want {
			t.Errorf("%q: err = %v, want ok=%v", pw, err, want)
		}
	}
}

// RFC 6238 Appendix B test vectors for SHA-1 (8 digits).
func TestHOTPMatchesRFC6238Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	vectors := map[int64]string{
		59:          "94287082",
		1111111109:  "07081804",
		1111111111:  "14050471",
		1234567890:  "89005924",
		2000000000:  "69279037",
		20000000000: "65353130",
	}
	for unix, want := range vectors {
		if got := hotp(secret, totpStep(time.Unix(unix, 0)), 8); got != want {
			t.Errorf("T=%d: got %s, want %s", unix, got, want)
		}
	}
}

func TestVerifyTOTP(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1111111111, 0)
	step := totpStep(now)
	code := hotp(secret, step, totpDigits)

	if got, ok := verifyTOTP(secret, code, now, 0); !ok || got != step {
		t.Fatalf("current code rejected")
	}
	// Clock drift of one step either way is tolerated.
	if _, ok := verifyTOTP(secret, code, now.Add(totpPeriod*time.Second), 0); !ok {
		t.Error("code from previous step rejected")
	}
	if _, ok := verifyTOTP(secret, code, now.Add(-totpPeriod*time.Second), 0); !ok {
		t.Error("code from next step rejected")
	}
	if _, ok := verifyTOTP(secret, code, now.Add(2*totpPeriod*time.Second), 0); ok {
		t.Error("code two steps old accepted")
	}
	// Replay protection: a code whose step was already used is rejected.
	if _, ok := verifyTOTP(secret, code, now, step); ok {
		t.Error("replayed code accepted")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef"} {
		if _, ok := verifyTOTP(secret, bad, now, 0); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestTOTPURI(t *testing.T) {
	uri := totpURI("Cloud", "alberto", []byte("12345678901234567890"))
	want := "otpauth://totp/Cloud:alberto?algorithm=SHA1&digits=6&issuer=Cloud&period=30&secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	if uri != want {
		t.Fatalf("got  %s\nwant %s", uri, want)
	}
}

func TestTOTPQR(t *testing.T) {
	uri := totpURI("Cloud", "alberto", []byte("12345678901234567890"))
	dataURI, err := totpQR(uri)
	if err != nil {
		t.Fatal(err)
	}
	b64, ok := strings.CutPrefix(dataURI, "data:image/png;base64,")
	if !ok {
		t.Fatalf("not a PNG data URI: %.40s", dataURI)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("invalid PNG: %v", err)
	}
	if b := img.Bounds(); b.Dx() < 100 || b.Dx() != b.Dy() {
		t.Errorf("unexpected QR size %v", b)
	}
}
