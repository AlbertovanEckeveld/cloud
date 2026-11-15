//go:build integration

package identity

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

func TestHTTPAvatarUpload(t *testing.T) {
	o := newOIDCEnv(t)
	u := o.newUser(t)
	token := o.signIn(t, u)
	t.Cleanup(func() { o.removeAvatar(t, token) })

	put := func(contentType string, body []byte) (int, string) {
		req, _ := http.NewRequest(http.MethodPut, o.base+"/api/v1/account/avatar", bytes.NewReader(body))
		req.Header.Set("Content-Type", contentType)
		resp, err := o.browser.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if status, body := put("image/png", encodePNG(t, 120, 80)); status != http.StatusOK {
		t.Fatalf("upload: %d %s", status, body)
	}
	if status, _ := put("image/svg+xml", []byte("<svg/>")); status != http.StatusUnsupportedMediaType {
		t.Errorf("svg upload: %d", status)
	}
	if status, _ := put("image/png", []byte("not a png")); status != http.StatusUnprocessableEntity {
		t.Errorf("garbage upload: %d", status)
	}
}
