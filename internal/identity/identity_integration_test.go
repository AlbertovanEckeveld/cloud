//go:build integration

package identity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlbertovanEckeveld/cloud/internal/core/config"
	"github.com/AlbertovanEckeveld/cloud/internal/core/database"
	"github.com/AlbertovanEckeveld/cloud/internal/core/mailer"
	"github.com/AlbertovanEckeveld/cloud/internal/core/sealer"
	"github.com/AlbertovanEckeveld/cloud/migrations"
)

const testPassword = "a sufficiently long passphrase"

var testSeq atomic.Int64

// testEnv is a service with a controllable clock and a unique client per test.
type testEnv struct {
	svc    *Service
	pool   *pgxpool.Pool
	sealer *sealer.Sealer
	clock  time.Time
	client Client
}

func (e *testEnv) advance(d time.Duration) { e.clock = e.clock.Add(d) }

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvURL(t, &url.URL{Scheme: "http", Host: "localhost:8080"})
}

func newTestEnvURL(t *testing.T, publicURL *url.URL) *testEnv {
	t.Helper()
	ctx := context.Background()
	pool, err := database.Open(ctx, config.Database{URL: config.Secret(os.Getenv("DATABASE_URL"))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var modules []database.Module
	for _, name := range []string{"core", "identity"} {
		fsys, err := migrations.For(name)
		if err != nil {
			t.Fatal(err)
		}
		modules = append(modules, database.Module{Name: name, FS: fsys})
	}
	if err := database.Migrate(ctx, pool, logger, modules...); err != nil {
		t.Fatal(err)
	}
	sl, err := sealer.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}

	// An enabled mailer whose dispatcher never runs: mail stays in the outbox,
	// where tests inspect it.
	mail, err := mailer.New(pool, sl, &config.Mail{From: "Cloud <cloud@example.test>"}, "localhost", logger)
	if err != nil {
		t.Fatal(err)
	}
	env := &testEnv{
		pool:   pool,
		sealer: sl,
		clock:  time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC),
		client: Client{IP: fmt.Sprintf("it-%d-%d", time.Now().UnixNano(), testSeq.Add(1)), UserAgent: "integration-test"},
	}
	env.svc, err = New(pool, sl, Options{SiteID: "test", PublicURL: publicURL, Mailer: mail, Logger: logger,
		OIDCTokenKey: [32]byte{1, 2, 3}})
	if err != nil {
		t.Fatal(err)
	}
	env.svc.hasher = newPasswordHasher(fastArgon, 2)
	t.Cleanup(func() {
		// Reads env.client at cleanup time: tests may change the IP.
		if _, err := pool.Exec(context.Background(), `DELETE FROM identity.throttle WHERE key = $1`, throttleIP.key(env.client.IP)); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	env.svc.now = func() time.Time { return env.clock }
	return env
}

// newUser creates a user with a unique name and removes it (and its audit
// trail and throttle counters) when the test ends.
func (e *testEnv) newUser(t *testing.T) User { return e.newUserWith(t, false) }

// newAdmin creates an administrator, activated, and returns a fresh full session for it.
func (e *testEnv) newAdmin(t *testing.T) (User, Session) {
	t.Helper()
	u := e.newUserWith(t, true)
	_, token := e.activate(t, u)
	return u, e.session(t, token)
}

func (e *testEnv) newUserWith(t *testing.T, admin bool) User {
	t.Helper()
	name := fmt.Sprintf("it-%d-%d", time.Now().UnixNano()%1e9, testSeq.Add(1))
	u, err := e.svc.CreateUser(context.Background(), e.client, NewUser{Username: name, DisplayName: "Test User", IsAdmin: admin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM identity.audit_events WHERE subject_id = $1 OR actor_id = $1 OR details->>'username' IN ($2, $3)`,
				[]any{u.ID, u.Username, unknownUsername(u)}},
			{`DELETE FROM identity.throttle WHERE key = ANY($1)`, []any{[]string{
				throttleUser.key(u.Username), throttleUser.key(u.ID.String()), throttleUser.key(unknownUsername(u)),
				throttleTOTP.key(u.ID.String()),
			}}},
			{`DELETE FROM identity.users WHERE id = $1`, []any{u.ID}},
		} {
			if _, err := e.pool.Exec(ctx, q.sql, q.args...); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
	})
	return u
}

// unknownUsername is a per-test name that does not exist, for enumeration tests.
func unknownUsername(u User) string { return "nobody-" + u.Username }

func tokenFromLink(t *testing.T, link string) string {
	t.Helper()
	_, token, ok := strings.Cut(link, "/activate#")
	if !ok || token == "" {
		t.Fatalf("unexpected activation link %q", link)
	}
	return token
}

func (e *testEnv) session(t *testing.T, token string) Session {
	t.Helper()
	sess, _, err := e.svc.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	return sess
}

func (e *testEnv) code(secret []byte) string { return hotp(secret, totpStep(e.clock), totpDigits) }

// activate runs the full activation flow and returns the TOTP secret and a full session token.
func (e *testEnv) activate(t *testing.T, u User) ([]byte, string) {
	t.Helper()
	ctx := context.Background()
	link, err := e.svc.IssueActivation(ctx, e.client, u.Username)
	if err != nil {
		t.Fatal(err)
	}
	partial, err := e.svc.Activate(ctx, e.client, tokenFromLink(t, link.Link), testPassword)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	enrollment, err := e.svc.BeginTOTPEnrollment(ctx, e.session(t, partial), u.Username)
	if err != nil {
		t.Fatalf("BeginTOTPEnrollment: %v", err)
	}
	secret, err := base32NoPad.DecodeString(enrollment.Secret)
	if err != nil {
		t.Fatal(err)
	}
	full, err := e.svc.ConfirmTOTPEnrollment(ctx, e.client, e.session(t, partial), e.code(secret))
	if err != nil {
		t.Fatalf("ConfirmTOTPEnrollment: %v", err)
	}
	e.advance(totpPeriod * time.Second) // the next code must come from a new step
	return secret, full
}

// login performs password + TOTP and returns the full session token.
func (e *testEnv) login(t *testing.T, u User, password string, secret []byte) string {
	t.Helper()
	ctx := context.Background()
	partial, err := e.svc.Login(ctx, e.client, u.Username, password)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	full, err := e.svc.VerifyTOTP(ctx, e.client, e.session(t, partial), e.code(secret))
	if err != nil {
		t.Fatalf("VerifyTOTP: %v", err)
	}
	e.advance(totpPeriod * time.Second)
	return full
}

func TestActivationFlow(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	ctx := context.Background()

	link, err := e.svc.IssueActivation(ctx, e.client, u.Username)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link.Link, "http://localhost:8080/activate#") || link.Emailed {
		t.Fatalf("activation = %+v", link)
	}
	token := tokenFromLink(t, link.Link)

	if _, err := e.svc.Activate(ctx, e.client, token, "short"); err == nil {
		t.Fatal("weak password accepted")
	} else if _, ok := errors.AsType[*PolicyError](err); !ok {
		t.Fatalf("err = %v, want PolicyError", err)
	}

	partial, err := e.svc.Activate(ctx, e.client, token, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	sess := e.session(t, partial)
	if sess.State != StateMFAEnrollmentRequired {
		t.Fatalf("state = %s", sess.State)
	}

	// Calling Begin twice replaces the pending secret; only the latest counts.
	first, _ := e.svc.BeginTOTPEnrollment(ctx, sess, u.Username)
	second, err := e.svc.BeginTOTPEnrollment(ctx, sess, u.Username)
	if err != nil || first.Secret == second.Secret {
		t.Fatalf("second enrollment: %v", err)
	}
	if !strings.HasPrefix(second.URI, "otpauth://totp/Cloud:"+u.Username+"?") {
		t.Errorf("uri = %s", second.URI)
	}
	oldSecret, _ := base32NoPad.DecodeString(first.Secret)
	if _, err := e.svc.ConfirmTOTPEnrollment(ctx, e.client, sess, e.code(oldSecret)); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("code from replaced secret: err = %v", err)
	}
	secret, _ := base32NoPad.DecodeString(second.Secret)
	full, err := e.svc.ConfirmTOTPEnrollment(ctx, e.client, sess, e.code(secret))
	if err != nil {
		t.Fatal(err)
	}

	if got := e.session(t, full); got.State != StateAuthenticated {
		t.Errorf("state after enrollment = %s", got.State)
	}
	if _, _, err := e.svc.Authenticate(ctx, partial); !errors.Is(err, ErrNoSession) {
		t.Error("partial token must be invalid after rotation (session fixation)")
	}
	if _, err := e.svc.Activate(ctx, e.client, token, testPassword); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("activation link reused: err = %v", err)
	}
}

func TestActivationLinkExpires(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	link, err := e.svc.IssueActivation(context.Background(), e.client, u.Username)
	if err != nil {
		t.Fatal(err)
	}
	e.advance(activationTTL + time.Minute)
	if _, err := e.svc.Activate(context.Background(), e.client, tokenFromLink(t, link.Link), testPassword); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestLoginRequiresBothFactors(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	secret, _ := e.activate(t, u)
	ctx := context.Background()

	for name, pw := range map[string]string{"wrong password": "not the password!!", "too long": strings.Repeat("x", 600)} {
		if _, err := e.svc.Login(ctx, e.client, u.Username, pw); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := e.svc.Login(ctx, e.client, unknownUsername(u), testPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("unknown user: err = %v (must match wrong password)", err)
	}

	partial, err := e.svc.Login(ctx, e.client, strings.ToUpper(u.Username), testPassword)
	if err != nil {
		t.Fatalf("login with uppercase username: %v", err)
	}
	sess := e.session(t, partial)
	if sess.State != StateMFARequired {
		t.Fatalf("state = %s", sess.State)
	}
	if _, err := e.svc.VerifyTOTP(ctx, e.client, sess, "000000"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("wrong code: err = %v", err)
	}
	code := e.code(secret)
	full, err := e.svc.VerifyTOTP(ctx, e.client, sess, code)
	if err != nil {
		t.Fatal(err)
	}
	if e.session(t, full).State != StateAuthenticated {
		t.Error("not authenticated after second factor")
	}

	// The same code cannot be used again, even in a fresh login.
	partial2, err := e.svc.Login(ctx, e.client, u.Username, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.VerifyTOTP(ctx, e.client, e.session(t, partial2), code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("replayed code: err = %v", err)
	}
}

func TestConcurrentUseOfOneCode(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	secret, _ := e.activate(t, u)
	ctx := context.Background()

	sessions := make([]Session, 5)
	for i := range sessions {
		partial, err := e.svc.Login(ctx, e.client, u.Username, testPassword)
		if err != nil {
			t.Fatal(err)
		}
		sessions[i] = e.session(t, partial)
	}
	code := e.code(secret)
	var wg sync.WaitGroup
	var successes atomic.Int32
	for _, sess := range sessions {
		wg.Go(func() {
			if _, err := e.svc.VerifyTOTP(ctx, e.client, sess, code); err == nil {
				successes.Add(1)
			}
		})
	}
	wg.Wait()
	if got := successes.Load(); got != 1 {
		t.Fatalf("%d logins succeeded with one code, want exactly 1", got)
	}
}

func TestPasswordThrottling(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	e.activate(t, u)
	ctx := context.Background()

	for range throttleUser.threshold {
		if _, err := e.svc.Login(ctx, e.client, u.Username, "wrong password!!"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("err = %v", err)
		}
	}
	_, err := e.svc.Login(ctx, e.client, u.Username, testPassword)
	throttled, ok := errors.AsType[*ThrottledError](err)
	if !ok || throttled.RetryAfter <= 0 {
		t.Fatalf("correct password during lockout: err = %v, want ThrottledError", err)
	}

	e.advance(time.Minute + time.Second)
	if _, err := e.svc.Login(ctx, e.client, u.Username, testPassword); err != nil {
		t.Fatalf("login after lock expired: %v", err)
	}
}

func TestTOTPLockoutRevokesPartialSession(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	e.activate(t, u)
	ctx := context.Background()

	partial, err := e.svc.Login(ctx, e.client, u.Username, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	sess := e.session(t, partial)
	for range throttleTOTP.threshold {
		if _, err := e.svc.VerifyTOTP(ctx, e.client, sess, "000000"); !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("err = %v", err)
		}
	}
	if _, _, err := e.svc.Authenticate(ctx, partial); !errors.Is(err, ErrNoSession) {
		t.Fatal("partial session survived TOTP lockout")
	}
}

func TestSessionExpiry(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	secret, full := e.activate(t, u)
	ctx := context.Background()

	partial, err := e.svc.Login(ctx, e.client, u.Username, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	e.advance(partialSessionTTL + time.Second)
	if _, _, err := e.svc.Authenticate(ctx, partial); !errors.Is(err, ErrNoSession) {
		t.Error("partial session outlived its TTL")
	}

	// Activity keeps a full session alive; idleness ends it.
	e.advance(sessionIdleTTL - time.Hour)
	if _, _, err := e.svc.Authenticate(ctx, full); err != nil {
		t.Fatalf("active session expired early: %v", err)
	}
	e.advance(sessionIdleTTL)
	if _, _, err := e.svc.Authenticate(ctx, full); !errors.Is(err, ErrNoSession) {
		t.Error("idle session still valid")
	}

	// Even an active session ends after the maximum lifetime.
	full = e.login(t, u, testPassword, secret)
	for range 5 {
		e.advance(sessionIdleTTL - time.Hour)
		_, _, _ = e.svc.Authenticate(ctx, full)
	}
	if _, _, err := e.svc.Authenticate(ctx, full); !errors.Is(err, ErrNoSession) {
		t.Error("session outlived its maximum lifetime")
	}
}

func TestReissuingActivationResetsAccount(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	_, full := e.activate(t, u)
	ctx := context.Background()

	if _, err := e.svc.IssueActivation(ctx, e.client, u.Username); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.Authenticate(ctx, full); !errors.Is(err, ErrNoSession) {
		t.Error("session survived account reset")
	}
	if _, err := e.svc.Login(ctx, e.client, u.Username, testPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("old password still works: err = %v", err)
	}
}

func TestOldLinkVoidedByNewLinkDuringEnrollment(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	ctx := context.Background()
	link, _ := e.svc.IssueActivation(ctx, e.client, u.Username)
	partial, err := e.svc.Activate(ctx, e.client, tokenFromLink(t, link.Link), testPassword)
	if err != nil {
		t.Fatal(err)
	}
	sess := e.session(t, partial)
	enrollment, err := e.svc.BeginTOTPEnrollment(ctx, sess, u.Username)
	if err != nil {
		t.Fatal(err)
	}
	// An admin issues a new link (e.g. the first one was intercepted).
	if _, err := e.svc.IssueActivation(ctx, e.client, u.Username); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.Authenticate(ctx, partial); !errors.Is(err, ErrNoSession) {
		t.Fatal("enrollment session survived reissue")
	}
	// Even with a session object still in hand, the old enrollment cannot complete.
	secret, _ := base32NoPad.DecodeString(enrollment.Secret)
	if _, err := e.svc.ConfirmTOTPEnrollment(ctx, e.client, sess, e.code(secret)); err == nil {
		t.Fatal("enrollment from a voided link completed")
	}
}

func TestAbandonedEnrollmentCannotLogIn(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	ctx := context.Background()
	link, _ := e.svc.IssueActivation(ctx, e.client, u.Username)
	if _, err := e.svc.Activate(ctx, e.client, tokenFromLink(t, link.Link), testPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Login(ctx, e.client, u.Username, testPassword); !errors.Is(err, ErrNotActivated) {
		t.Fatalf("err = %v, want ErrNotActivated", err)
	}
}

func TestChangePassword(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	secret, current := e.activate(t, u)
	other := e.login(t, u, testPassword, secret)
	ctx := context.Background()
	sess := e.session(t, current)

	if err := e.svc.ChangePassword(ctx, e.client, sess, "wrong current pw", "a brand new passphrase"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong current: err = %v", err)
	}
	if err := e.svc.ChangePassword(ctx, e.client, sess, testPassword, "short"); err == nil {
		t.Fatal("weak new password accepted")
	}
	if err := e.svc.ChangePassword(ctx, e.client, sess, testPassword, "a brand new passphrase"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.Authenticate(ctx, current); err != nil {
		t.Error("current session should survive a password change")
	}
	if _, _, err := e.svc.Authenticate(ctx, other); !errors.Is(err, ErrNoSession) {
		t.Error("other sessions must be signed out")
	}
	e.login(t, u, "a brand new passphrase", secret)
}

func TestLogoutAndActivity(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	_, full := e.activate(t, u)
	ctx := context.Background()

	if _, err := e.svc.Login(ctx, e.client, u.Username, "wrong password!!"); err == nil {
		t.Fatal("expected failure")
	}
	if err := e.svc.Logout(ctx, e.client, e.session(t, full)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.Authenticate(ctx, full); !errors.Is(err, ErrNoSession) {
		t.Error("session valid after logout")
	}

	events, err := e.svc.Activity(ctx, u.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ev := range events {
		got = append(got, ev.Event)
	}
	want := []string{EventLogout, EventLoginFailed, EventLoginSucceeded, EventTOTPEnrolled, EventActivationStarted, EventActivationIssued, EventUserCreated}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("activity:\n got  %v\n want %v", got, want)
	}
}

func TestCreateUserValidation(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	ctx := context.Background()
	if _, err := e.svc.CreateUser(ctx, e.client, NewUser{Username: u.Username, DisplayName: "Dup"}); !errors.Is(err, ErrUserExists) {
		t.Errorf("duplicate: err = %v", err)
	}
	for _, in := range []NewUser{
		{Username: "a", DisplayName: "x"},
		{Username: "has space", DisplayName: "x"},
		{Username: "valid-name", DisplayName: ""},
		{Username: "valid-name", DisplayName: "bell\a"},
	} {
		if _, err := e.svc.CreateUser(ctx, e.client, in); err == nil {
			t.Errorf("%+v accepted", in)
		} else if _, ok := errors.AsType[*InputError](err); !ok {
			t.Errorf("%+v: err = %v, want InputError", in, err)
		}
	}
	if _, err := e.svc.IssueActivation(ctx, e.client, "no-such-user-xyz"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("unknown user: err = %v", err)
	}
}

func TestTOTPSecretIsEncryptedAtRest(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	secret, _ := e.activate(t, u)
	var stored []byte
	if err := e.pool.QueryRow(context.Background(), `SELECT secret_sealed FROM identity.totp_credentials WHERE user_id = $1`, u.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, secret) || bytes.Contains(stored, []byte(base32NoPad.EncodeToString(secret))) {
		t.Fatal("TOTP secret stored in plaintext")
	}
}
