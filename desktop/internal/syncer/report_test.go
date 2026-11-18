package syncer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReports(t *testing.T) {
	type call struct {
		method, path, auth, ctype string
		body                      map[string]any
	}
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		calls = append(calls, call{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), body})
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL, true, staticToken("tok"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ReportDevice(context.Background(), "werk-laptop", "linux"); err != nil {
		t.Fatal(err)
	}
	if err := c.ReportSync(context.Background(), []string{"mine", "0193"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls: %+v", calls)
	}
	d, s := calls[0], calls[1]
	if d.method != "PUT" || d.path != "/api/v1/device" || d.auth != "Bearer tok" || d.ctype != "application/json" ||
		d.body["name"] != "werk-laptop" || d.body["platform"] != "linux" {
		t.Errorf("device report: %+v", d)
	}
	roots, _ := s.body["roots"].([]any)
	if s.method != "PUT" || s.path != "/api/v1/drive/sync/device" || len(roots) != 2 || roots[0] != "mine" {
		t.Errorf("sync report: %+v", s)
	}
}
