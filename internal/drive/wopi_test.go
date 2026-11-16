package drive

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testOffice() *office {
	pub, _ := url.Parse("http://cloud.example")
	col, _ := url.Parse("http://office.example:9980")
	return &office{cfg: Office{URL: col, PublicURL: pub, Key: [32]byte{1, 2, 3}}}
}

func TestWOPIToken(t *testing.T) {
	o := testOffice()
	now := time.Now()
	tok := o.sign(wopiToken{User: uuid.New(), File: uuid.New(), Write: true, Exp: now.Add(time.Hour).UnixMilli()})
	if got, ok := o.verify(tok, now); !ok || !got.Write {
		t.Fatalf("valid token rejected: %+v", got)
	}
	if _, ok := o.verify(tok, now.Add(2*time.Hour)); ok {
		t.Error("expired token accepted")
	}
	// Flipping the write flag breaks the signature.
	p, m, _ := strings.Cut(tok, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(p)
	forged := base64.RawURLEncoding.EncodeToString([]byte(strings.Replace(string(raw), `"w":true`, `"w":false`, 1))) + "." + m
	if _, ok := o.verify(forged, now); ok {
		t.Error("tampered token accepted")
	}
	other := testOffice()
	other.cfg.Key[0] = 9
	if _, ok := other.verify(tok, now); ok {
		t.Error("token accepted with another key")
	}
}

func TestWOPIProof(t *testing.T) {
	o := testOffice()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	o.proof = &key.PublicKey
	token := "abc"
	sign := func(path string, when time.Time) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		ts := when.UnixNano()/100 + 621355968000000000
		full := strings.ToUpper(o.cfg.PublicURL.String() + path)
		var b []byte
		b = binary.BigEndian.AppendUint32(b, uint32(len(token)))
		b = append(b, token...)
		b = binary.BigEndian.AppendUint32(b, uint32(len(full)))
		b = append(b, full...)
		b = binary.BigEndian.AppendUint32(b, 8)
		b = binary.BigEndian.AppendUint64(b, uint64(ts))
		d := sha256.Sum256(b)
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, d[:])
		r.Header.Set("X-WOPI-TimeStamp", strconv.FormatInt(ts, 10))
		r.Header.Set("X-WOPI-Proof", base64.StdEncoding.EncodeToString(sig))
		w := httptest.NewRecorder()
		if !o.verifyProof(r, token) {
			w.Code = 401
		}
		return w
	}
	if w := sign("/wopi/files/x?access_token=abc", time.Now()); w.Code != 200 {
		t.Error("valid proof rejected")
	}
	if w := sign("/wopi/files/x?access_token=abc", time.Now().Add(-time.Hour)); w.Code == 200 {
		t.Error("old proof accepted")
	}
	r := httptest.NewRequest("GET", "/wopi/files/x", nil)
	if o.verifyProof(r, token) {
		t.Error("missing proof accepted while a key is published")
	}
}
