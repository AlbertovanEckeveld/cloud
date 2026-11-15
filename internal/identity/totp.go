package identity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/url"
	"time"

	"rsc.io/qr"
)

// TOTP as specified in RFC 6238, built on HOTP (RFC 4226). The parameters
// (SHA-1, 6 digits, 30 seconds) are the defaults every authenticator app
// supports. SHA-1 is not a weakness here: HOTP relies on HMAC, not on
// collision resistance.
const (
	totpDigits    = 6
	totpPeriod    = 30 // seconds
	totpSkew      = 1  // accept one step before and after, for clock drift
	totpSecretLen = 20 // bytes; RFC 4226 §4 recommends 160 bits
)

var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

func newTOTPSecret() []byte {
	secret := make([]byte, totpSecretLen)
	if _, err := rand.Read(secret); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return secret
}

func totpStep(t time.Time) int64 { return t.Unix() / totpPeriod }

// hotp computes an HOTP value (RFC 4226 §5.3, dynamic truncation).
func hotp(secret []byte, counter int64, digits int) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(counter))
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	code := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	mod := uint32(1)
	for range digits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, code%mod)
}

// verifyTOTP checks code against the steps around now. Steps at or before
// lastStep are rejected, so an observed code cannot be replayed (RFC 6238
// §5.2). It returns the matched step, to be stored as the new lastStep.
func verifyTOTP(secret []byte, code string, now time.Time, lastStep int64) (int64, bool) {
	if len(code) != totpDigits {
		return 0, false
	}
	current := totpStep(now)
	for step := current - totpSkew; step <= current+totpSkew; step++ {
		if step <= lastStep {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(hotp(secret, step, totpDigits)), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// totpURI builds the otpauth:// URI that authenticator apps import
// (Key Uri Format, as introduced by Google Authenticator).
func totpURI(issuer, account string, secret []byte) string {
	q := url.Values{}
	q.Set("secret", base32NoPad.EncodeToString(secret))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(totpDigits))
	q.Set("period", fmt.Sprint(totpPeriod))
	u := url.URL{Scheme: "otpauth", Host: "totp", Path: "/" + issuer + ":" + account, RawQuery: q.Encode()}
	return u.String()
}

// totpQR renders the otpauth URI as a PNG QR code in a data: URI, which the
// page can show without loading anything from elsewhere.
func totpQR(uri string) (string, error) {
	code, err := qr.Encode(uri, qr.M)
	if err != nil {
		return "", fmt.Errorf("encode qr: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()), nil
}
