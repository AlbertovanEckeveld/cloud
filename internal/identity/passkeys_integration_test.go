//go:build integration

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRecoveryCodes(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	_, full := e.activate(t, u)
	ctx := context.Background()

	codes, err := e.svc.GenerateRecoveryCodes(ctx, e.client, e.session(t, full))
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != recoveryCodeCount || len(codes[0]) != 19 {
		t.Fatalf("codes = %v", codes)
	}
	if n, _ := e.svc.RemainingRecoveryCodes(ctx, u.ID); n != recoveryCodeCount {
		t.Fatalf("remaining = %d", n)
	}

	login := func() Session {
		partial, err := e.svc.Login(ctx, e.client, u.Username, testPassword)
		if err != nil {
			t.Fatal(err)
		}
		return e.session(t, partial)
	}

	// Codes are accepted however the user types them.
	typed := strings.ToUpper(strings.ReplaceAll(codes[0], "-", " "))
	token, err := e.svc.VerifyRecoveryCode(ctx, e.client, login(), typed)
	if err != nil {
		t.Fatalf("recovery login: %v", err)
	}
	if e.session(t, token).State != StateAuthenticated {
		t.Fatal("not authenticated after recovery code")
	}
	if _, err := e.svc.VerifyRecoveryCode(ctx, e.client, login(), codes[0]); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("reused code: err = %v", err)
	}
	if n, _ := e.svc.RemainingRecoveryCodes(ctx, u.ID); n != recoveryCodeCount-1 {
		t.Fatalf("remaining after use = %d", n)
	}

	// Regenerating invalidates every old code.
	if _, err := e.svc.GenerateRecoveryCodes(ctx, e.client, e.session(t, token)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.VerifyRecoveryCode(ctx, e.client, login(), codes[1]); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("old code after regeneration: err = %v", err)
	}
}

func TestSensitiveChangesNeedRecentAuth(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	_, full := e.activate(t, u)
	ctx := context.Background()

	e.advance(recentAuthWindow + time.Minute)
	sess := e.session(t, full) // still a valid session, but no longer "recent"
	if _, err := e.svc.GenerateRecoveryCodes(ctx, e.client, sess); !errors.Is(err, ErrReauthRequired) {
		t.Errorf("recovery codes: err = %v", err)
	}
	if _, err := e.svc.BeginPasskeyRegistration(ctx, sess); !errors.Is(err, ErrReauthRequired) {
		t.Errorf("passkey registration: err = %v", err)
	}
	if err := e.svc.DeletePasskey(ctx, e.client, sess, uuid.New()); !errors.Is(err, ErrReauthRequired) {
		t.Errorf("passkey deletion: err = %v", err)
	}
}

func TestPasskeyRegistrationChallenge(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	secret, full := e.activate(t, u)
	ctx := context.Background()
	sess := e.session(t, full)

	ceremony, err := e.svc.BeginPasskeyRegistration(ctx, sess)
	if err != nil {
		t.Fatal(err)
	}
	options, _ := json.Marshal(ceremony.Options)
	for _, want := range []string{`"residentKey":"required"`, `"userVerification":"required"`, `"attestation":"none"`, `"id":"localhost"`} {
		if !strings.Contains(string(options), want) {
			t.Errorf("registration options lack %s: %s", want, options)
		}
	}

	// The challenge is bound to the session that started it.
	other := e.session(t, e.login(t, u, testPassword, secret))
	if _, err := e.svc.FinishPasskeyRegistration(ctx, e.client, other, ceremony.ChallengeID, "x", json.RawMessage(`{}`)); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("challenge from another session: err = %v", err)
	}
	// A garbage credential is rejected, and the challenge is used up regardless.
	if _, err := e.svc.FinishPasskeyRegistration(ctx, e.client, sess, ceremony.ChallengeID, "x", json.RawMessage(`{"id":"x"}`)); !errors.Is(err, ErrPasskeyRejected) {
		t.Fatalf("garbage credential: err = %v", err)
	}
	if _, err := e.svc.FinishPasskeyRegistration(ctx, e.client, sess, ceremony.ChallengeID, "x", json.RawMessage(`{}`)); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("reused challenge: err = %v", err)
	}

	// Challenges expire.
	ceremony, _ = e.svc.BeginPasskeyRegistration(ctx, sess)
	e.advance(challengeTTL + time.Second)
	if _, err := e.svc.FinishPasskeyRegistration(ctx, e.client, e.session(t, full), ceremony.ChallengeID, "x", json.RawMessage(`{}`)); err == nil {
		t.Fatal("expired challenge accepted")
	}
}

func TestPasskeyLoginRejectsGarbage(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()

	ceremony, err := e.svc.BeginPasskeyLogin(ctx, e.client)
	if err != nil {
		t.Fatal(err)
	}
	options, _ := json.Marshal(ceremony.Options)
	if !strings.Contains(string(options), `"userVerification":"required"`) || strings.Contains(string(options), "allowCredentials") {
		t.Errorf("login must be discoverable with user verification: %s", options)
	}
	if _, err := e.svc.FinishPasskeyLogin(ctx, e.client, ceremony.ChallengeID, json.RawMessage(`{"id":"x"}`)); !errors.Is(err, ErrPasskeyRejected) {
		t.Fatalf("err = %v", err)
	}
	if _, err := e.svc.FinishPasskeyLogin(ctx, e.client, ceremony.ChallengeID, json.RawMessage(`{}`)); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("reused login challenge: err = %v", err)
	}
	// The failed attempt is in the audit log.
	var failures int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM identity.audit_events WHERE event = $1 AND ip = $2`,
		EventPasskeyFailed, e.client.IP).Scan(&failures); err != nil || failures != 1 {
		t.Fatalf("audit failures = %d, %v", failures, err)
	}
	t.Cleanup(func() {
		if _, err := e.pool.Exec(context.Background(), `DELETE FROM identity.audit_events WHERE ip = $1`, e.client.IP); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
}

func TestSessionManagement(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	secret, current := e.activate(t, u)
	second := e.login(t, u, testPassword, secret)
	third := e.login(t, u, testPassword, secret)
	ctx := context.Background()
	sess := e.session(t, current)

	list, err := e.svc.ListSessions(ctx, sess)
	if err != nil || len(list) != 3 {
		t.Fatalf("list = %v, %v", list, err)
	}
	currentCount := 0
	for _, s := range list {
		if s.Current {
			currentCount++
		}
	}
	if currentCount != 1 {
		t.Errorf("%d sessions marked current", currentCount)
	}

	// Revoke one specific session.
	if err := e.svc.RevokeSession(ctx, e.client, sess, e.session(t, second).ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.Authenticate(ctx, second); !errors.Is(err, ErrNoSession) {
		t.Error("revoked session still valid")
	}

	// Another user's session cannot be revoked.
	other := e.newUser(t)
	_, otherToken := e.activate(t, other)
	if err := e.svc.RevokeSession(ctx, e.client, sess, e.session(t, otherToken).ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking another user's session: err = %v", err)
	}

	n, err := e.svc.RevokeOtherSessions(ctx, e.client, sess)
	if err != nil || n != 1 {
		t.Fatalf("revoke others = %d, %v", n, err)
	}
	if _, _, err := e.svc.Authenticate(ctx, third); !errors.Is(err, ErrNoSession) {
		t.Error("other session survived")
	}
	if _, _, err := e.svc.Authenticate(ctx, current); err != nil {
		t.Error("current session must survive")
	}
}

func TestResetWipesPasskeysAndRecoveryCodes(t *testing.T) {
	e := newTestEnv(t)
	u := e.newUser(t)
	_, full := e.activate(t, u)
	ctx := context.Background()
	if _, err := e.svc.GenerateRecoveryCodes(ctx, e.client, e.session(t, full)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO identity.passkeys (id, user_id, credential_id, name, credential, created_at)
		VALUES ($1, $2, $3, 'test', '{}', now())`, newID(), u.ID, []byte("cred-"+u.Username)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.IssueActivation(ctx, e.client, u.Username); err != nil {
		t.Fatal(err)
	}
	var passkeys, codes int
	if err := e.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM identity.passkeys WHERE user_id = $1),
		(SELECT count(*) FROM identity.recovery_codes WHERE user_id = $1)`, u.ID).Scan(&passkeys, &codes); err != nil {
		t.Fatal(err)
	}
	if passkeys != 0 || codes != 0 {
		t.Fatalf("after reset: %d passkeys, %d recovery codes", passkeys, codes)
	}
}
