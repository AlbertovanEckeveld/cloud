package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func serve(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestRequestID(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = RequestIDFrom(r.Context()) }))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "injected\nfake log line")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	id, err := uuid.Parse(seen)
	if err != nil || id.Version() != 7 {
		t.Fatalf("request id %q is not a UUIDv7", seen)
	}
	if rec.Header().Get("X-Request-ID") != seen {
		t.Error("response header does not carry the request id")
	}
	first := seen
	if serve(h, http.MethodGet, "/").Header().Get("X-Request-ID") == first {
		t.Error("request ids must be unique")
	}
}

func TestAccessLogOmitsQueryString(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := RequestID(AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})))
	serve(h, http.MethodGet, "/share?token=supersecret")

	if strings.Contains(buf.String(), "supersecret") {
		t.Fatalf("access log contains query string: %s", buf.String())
	}
	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["path"] != "/share" || entry["status"] != float64(http.StatusTeapot) || entry["request_id"] == "" {
		t.Errorf("unexpected log entry: %v", entry)
	}
}

func TestRecoverHidesPanicDetails(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	h := Recover(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("db password is hunter2")
	}))
	rec := serve(h, http.MethodGet, "/")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatal("panic message leaked to client")
	}
	var body ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != "internal" {
		t.Errorf("body = %s", rec.Body.String())
	}
	if !strings.Contains(buf.String(), "panic in handler") {
		t.Error("panic not logged")
	}
}

func TestRecoverRepanicsOnAbort(t *testing.T) {
	h := Recover(discard())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
	defer func() {
		if recover() != http.ErrAbortHandler {
			t.Error("ErrAbortHandler must propagate to net/http")
		}
	}()
	serve(h, http.MethodGet, "/")
}

func TestSecurityHeaders(t *testing.T) {
	ok := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	rec := serve(SecurityHeaders(false)(ok), http.MethodGet, "/")
	for header, want := range map[string]string{
		"Content-Security-Policy": apiCSP,
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "no-referrer",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS must not be sent when disabled")
	}
	if serve(SecurityHeaders(true)(ok), http.MethodGet, "/").Header().Get("Strict-Transport-Security") == "" {
		t.Error("HSTS missing when enabled")
	}
}

const testIndex = `<!doctype html><html><head>` +
	`<link rel="modulepreload" href="/_app/immutable/entry/start.js">` +
	`<script src="/_app/immutable/x.js"></script>` +
	`</head><body><script>
	boot();
</script></body></html>`

func testSPA(t *testing.T) *SPA {
	t.Helper()
	spa, err := NewSPA(fstest.MapFS{
		"index.html":                  {Data: []byte(testIndex)},
		"robots.txt":                  {Data: []byte("User-agent: *\nDisallow: /\n")},
		"_app/immutable/entry/app.js": {Data: []byte("console.log(1)")},
		"_app/immutable":              {Mode: 0o755 | 1<<31}, // directory
	})
	if err != nil {
		t.Fatal(err)
	}
	return spa
}

func TestSPACSPHashesInlineScriptOnly(t *testing.T) {
	sum := sha256.Sum256([]byte("\n\tboot();\n"))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"

	csp := serve(testSPA(t), http.MethodGet, "/").Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self' "+want+";") {
		t.Fatalf("CSP lacks exact hash of the inline script:\n%s", csp)
	}
	if strings.Count(csp, "sha256-") != 1 {
		t.Errorf("external scripts must not be hashed: %s", csp)
	}
	for _, forbidden := range []string{"unsafe-inline", "unsafe-eval", "*"} {
		if strings.Contains(csp, forbidden) {
			t.Errorf("CSP contains %q", forbidden)
		}
	}
}

func TestSPARouting(t *testing.T) {
	spa := testSPA(t)
	tests := []struct {
		target, wantBody, wantCache string
	}{
		{"/", "boot()", "no-cache"},
		{"/settings/profile", "boot()", "no-cache"},
		{"/index.html", "boot()", "no-cache"},
		{"/robots.txt", "Disallow", "no-cache"},
		{"/_app/immutable/entry/app.js", "console.log", "public, max-age=31536000, immutable"},
		{"/_app/immutable", "boot()", "no-cache"},
		{"/../../etc/passwd", "boot()", "no-cache"},
		{"/%2e%2e/%2e%2e/etc/passwd", "boot()", "no-cache"},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			rec := serve(spa, http.MethodGet, tt.target)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q", rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != tt.wantCache {
				t.Errorf("Cache-Control = %q, want %q", got, tt.wantCache)
			}
		})
	}
}

func TestSPAMethods(t *testing.T) {
	spa := testSPA(t)
	if rec := serve(spa, http.MethodPost, "/"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d", rec.Code)
	}
	rec := serve(spa, http.MethodHead, "/")
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD status=%d body=%d bytes", rec.Code, rec.Body.Len())
	}
}

func TestNewSPARequiresIndex(t *testing.T) {
	if _, err := NewSPA(fstest.MapFS{}); err == nil {
		t.Fatal("expected error without index.html")
	}
}

func TestRunGracefulShutdown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	shutdownCalled := make(chan struct{})
	done := make(chan error, 1)

	release := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // a request that is still in flight during shutdown
		_, _ = w.Write([]byte("finished"))
	})
	go func() { done <- Run(ctx, discard(), ln, h, func() { close(shutdownCalled) }) }()

	respCh := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String())
		if err != nil {
			respCh <- "error: " + err.Error()
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		respCh <- string(b)
	}()

	time.Sleep(100 * time.Millisecond) // let the request reach the handler
	cancel()
	select {
	case <-shutdownCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("onShutdown not called")
	}
	close(release)

	if got := <-respCh; got != "finished" {
		t.Errorf("in-flight request was not completed: %q", got)
	}
	if err := <-done; err != nil {
		t.Errorf("Run returned %v", err)
	}
}

func TestSPACSPAllowsFrameworkStyleAttributesByHash(t *testing.T) {
	const style = "position: absolute; width: 1px"
	spa, err := NewSPA(fstest.MapFS{
		"index.html":           {Data: []byte("<html></html>")},
		"_app/immutable/a.js":  {Data: []byte("var t=`<div style=\"" + style + "\"></div>`;var u=`<p style=\"" + style + "\">`")},
		"_app/immutable/b.css": {Data: []byte(`div{style="ignored: css"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(style))
	want := "style-src 'self' 'unsafe-hashes' 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "';"
	if !strings.Contains(spa.csp, want) {
		t.Fatalf("CSP = %s\nwant %s", spa.csp, want)
	}
	if strings.Count(spa.csp, "sha256-") != 1 {
		t.Errorf("duplicate or non-JS styles hashed: %s", spa.csp)
	}
}

func TestSPACSPWithoutStyleAttributes(t *testing.T) {
	csp := testSPA(t).csp
	if !strings.Contains(csp, "style-src 'self';") || strings.Contains(csp, "unsafe-hashes") {
		t.Fatalf("CSP = %s", csp)
	}
}
