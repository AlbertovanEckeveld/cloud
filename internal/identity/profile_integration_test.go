//go:build integration

package identity

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/AlbertovanEckeveld/cloud/internal/core/storage"
)

func (o *oidcEnv) get(t *testing.T, rawURL string) (*http.Response, []byte) {
	t.Helper()
	resp, err := o.plain.Get(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

// avatarKey turns a picture URL back into its S3 key.
func avatarKey(picture string) string {
	return avatarPrefix + picture[strings.LastIndex(picture, "/")+1:]
}

func TestProfileAndAvatar(t *testing.T) {
	o := newOIDCEnv(t)
	u := o.newUser(t)
	_, token := o.activate(t, u)
	ctx := context.Background()
	sess := o.session(t, token)
	t.Cleanup(func() { o.removeAvatar(t, token) })

	if err := o.svc.UpdateProfile(ctx, o.client, sess, Profile{DisplayName: "  Alice  ", GivenName: "Alice", FamilyName: "de Vries", Locale: "en"}); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]Profile{
		"empty display name": {DisplayName: "", Locale: "nl"},
		"bad locale":         {DisplayName: "x", Locale: "fr"},
		"control character":  {DisplayName: "x", GivenName: "a\x00b", Locale: "nl"},
		"too long":           {DisplayName: "x", FamilyName: strings.Repeat("a", 101), Locale: "nl"},
	} {
		if err := o.svc.UpdateProfile(ctx, o.client, sess, p); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := o.svc.SetAvatar(ctx, o.client, sess, []byte("<svg/>")); !errors.Is(err, ErrInvalidImage) {
		t.Errorf("svg avatar: err = %v", err)
	}

	if err := o.svc.SetAvatar(ctx, o.client, sess, encodePNG(t, 600, 300)); err != nil {
		t.Fatal(err)
	}
	p, err := o.svc.Profile(ctx, u.ID)
	if err != nil || p.DisplayName != "Alice" || p.Locale != "en" || !strings.HasPrefix(p.Picture, o.base+"/avatars/") {
		t.Fatalf("profile = %+v, %v", p, err)
	}

	resp, body := o.get(t, p.Picture)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/jpeg" ||
		resp.Header.Get("Cross-Origin-Resource-Policy") != "cross-origin" || !bytes.HasPrefix(body, []byte{0xFF, 0xD8}) {
		t.Fatalf("avatar response: %d %v", resp.StatusCode, resp.Header)
	}

	// Replacing the picture deletes the old object; the old URL stops working.
	if err := o.svc.SetAvatar(ctx, o.client, sess, encodePNG(t, 64, 64)); err != nil {
		t.Fatal(err)
	}
	if resp, _ := o.get(t, p.Picture); resp.StatusCode != http.StatusNotFound {
		t.Errorf("old avatar URL: %d", resp.StatusCode)
	}
	if _, err := o.svc.opts.Objects.Get(ctx, avatarKey(p.Picture)); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("old avatar object still in S3: %v", err)
	}

	// Malformed or guessed names get nothing.
	for _, name := range []string{"..%2Fsecrets", "abc.jpg", strings.Repeat("0", 32) + ".jpg"} {
		if resp, _ := o.get(t, o.base+"/avatars/"+name); resp.StatusCode != http.StatusNotFound {
			t.Errorf("/avatars/%s: %d", name, resp.StatusCode)
		}
	}

	// Removing the picture empties the claim.
	if err := o.svc.SetAvatar(ctx, o.client, sess, nil); err != nil {
		t.Fatal(err)
	}
	if p, _ := o.svc.Profile(ctx, u.ID); p.Picture != "" {
		t.Errorf("picture after removal = %q", p.Picture)
	}
}

func TestProfileClaims(t *testing.T) {
	o := newOIDCEnv(t)
	u := o.newUser(t)
	token := o.signIn(t, u)
	sess := o.session(t, token)
	ctx := context.Background()
	if err := o.svc.UpdateProfile(ctx, o.client, sess, Profile{DisplayName: "Bob B", GivenName: "Bob", FamilyName: "Bakker", Locale: "nl"}); err != nil {
		t.Fatal(err)
	}
	if err := o.svc.SetAvatar(ctx, o.client, sess, encodePNG(t, 32, 32)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.removeAvatar(t, token) })

	client, secret := o.newClient(t, true, nil)
	f := newFlow(client.ID)
	loc, _, _ := o.approve(t, requestID(t, o.authorize(t, f)))
	_, tk := o.token(t, client.ID, secret, codeExchange(loc.Query().Get("code"), f))
	claims := o.verifyIDToken(t, tk.IDToken)
	picture, _ := claims["picture"].(string)
	if claims["given_name"] != "Bob" || claims["family_name"] != "Bakker" || claims["name"] != "Bob B" ||
		claims["locale"] != "nl" || !strings.HasPrefix(picture, o.base+"/avatars/") {
		t.Fatalf("profile claims = %v", claims)
	}
	if resp, _ := o.get(t, picture); resp.StatusCode != http.StatusOK {
		t.Errorf("picture claim URL: %d", resp.StatusCode)
	}
}

// removeAvatar deletes a test user's picture from S3.
func (o *oidcEnv) removeAvatar(t *testing.T, token string) {
	sess, _, err := o.svc.Authenticate(context.Background(), token)
	if err == nil {
		err = o.svc.SetAvatar(context.Background(), o.client, sess, nil)
	}
	if err != nil {
		t.Errorf("cleanup avatar: %v", err)
	}
}
