//go:build integration

package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type outboxMail struct{ subject, body string }

// mails returns the queued mail for an address, decrypting the bodies.
func (e *testEnv) mails(t *testing.T, to string) []outboxMail {
	t.Helper()
	rows, err := e.pool.Query(context.Background(),
		`SELECT id, subject, body_sealed FROM core.mail_outbox WHERE recipient = $1 ORDER BY created_at, id`, to)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []outboxMail
	for rows.Next() {
		var id uuid.UUID
		var m outboxMail
		var sealed []byte
		if err := rows.Scan(&id, &m.subject, &sealed); err != nil {
			t.Fatal(err)
		}
		body, err := e.sealer.Open(sealed, []byte("mail:"+id.String()))
		if err != nil {
			t.Fatalf("open queued mail: %v", err)
		}
		if strings.Contains(string(sealed), "activate#") {
			t.Fatal("mail body stored in plaintext")
		}
		m.body = string(body)
		out = append(out, m)
	}
	return out
}

// newMailUser creates a user with an e-mail address; its mail is cleaned up.
func (e *testEnv) newMailUser(t *testing.T) (User, string) {
	t.Helper()
	u := e.newUser(t)
	email := fmt.Sprintf("%s@example.test", u.Username)
	_, actor := e.newAdmin(t)
	if err := e.svc.UpdateUser(context.Background(), e.client, e.as(t, actor), u.ID, UserUpdate{Email: &email}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := e.pool.Exec(context.Background(), `DELETE FROM core.mail_outbox WHERE recipient = $1`, email); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	return u, email
}

func TestInviteAndResetAreMailed(t *testing.T) {
	e := newTestEnv(t)
	_, actor := e.newAdmin(t)
	ctx := context.Background()
	email := fmt.Sprintf("it-inv-%d@example.test", testSeq.Add(1))
	u, a, err := e.svc.InviteUser(ctx, e.client, actor, NewUser{Username: strings.TrimSuffix(email, "@example.test"), DisplayName: "Mail Test", Email: email})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, q := range []struct {
			sql string
			arg any
		}{
			{`DELETE FROM core.mail_outbox WHERE recipient = $1`, email},
			{`DELETE FROM identity.audit_events WHERE subject_id = $1 OR actor_id = $1`, u.ID},
			{`DELETE FROM identity.users WHERE id = $1`, u.ID},
		} {
			if _, err := e.pool.Exec(context.Background(), q.sql, q.arg); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
	})
	if !a.Emailed || u.Email != email {
		t.Fatalf("invite: emailed=%v email=%q", a.Emailed, u.Email)
	}
	mails := e.mails(t, email)
	if len(mails) != 1 || mails[0].subject != "Je account voor Cloud" || !strings.Contains(mails[0].body, a.Link) ||
		!strings.Contains(mails[0].body, "Hallo Mail Test,") {
		t.Fatalf("invite mail = %+v", mails)
	}

	// After activation a reset mail says so.
	tokenSession, err := e.svc.Activate(ctx, e.client, tokenFromLink(t, a.Link), testPassword)
	if err != nil || tokenSession == "" {
		t.Fatal(err)
	}
	reset, err := e.svc.ResetUser(ctx, e.client, e.as(t, actor), u.ID)
	if err != nil || !reset.Emailed {
		t.Fatalf("reset: %+v, %v", reset, err)
	}
	mails = e.mails(t, email)
	if len(mails) != 2 || mails[1].subject != "Je account is opnieuw ingesteld" || !strings.Contains(mails[1].body, reset.Link) {
		t.Fatalf("reset mail = %+v", mails)
	}
}

func TestSecurityNotifications(t *testing.T) {
	e := newTestEnv(t)
	u, email := e.newMailUser(t)
	_, full := e.activate(t, u)
	ctx := context.Background()
	before := len(e.mails(t, email))

	// A failed change sends nothing: the mail is part of the transaction.
	if err := e.svc.ChangePassword(ctx, e.client, e.session(t, full), "wrong password!!", "another long password"); err == nil {
		t.Fatal("expected failure")
	}
	if got := len(e.mails(t, email)); got != before {
		t.Fatalf("mail queued for a failed change")
	}

	if err := e.svc.ChangePassword(ctx, e.client, e.session(t, full), testPassword, "another long password"); err != nil {
		t.Fatal(err)
	}
	codes, err := e.svc.GenerateRecoveryCodes(ctx, e.client, e.session(t, full))
	if err != nil {
		t.Fatal(err)
	}
	partial, err := e.svc.Login(ctx, e.client, u.Username, "another long password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.VerifyRecoveryCode(ctx, e.client, e.session(t, partial), codes[0]); err != nil {
		t.Fatal(err)
	}
	var subjects []string
	for _, m := range e.mails(t, email)[before:] {
		subjects = append(subjects, m.subject)
		if !strings.Contains(m.body, "IP-adres: "+e.client.IP) || !strings.Contains(m.body, "Was jij dit niet?") {
			t.Errorf("notification body lacks context: %q", m.body)
		}
	}
	want := []string{"Je wachtwoord is gewijzigd", "Nieuwe herstelcodes aangemaakt", "Er is ingelogd met een herstelcode"}
	if strings.Join(subjects, "|") != strings.Join(want, "|") {
		t.Fatalf("subjects = %v, want %v", subjects, want)
	}
}

func TestEmailValidation(t *testing.T) {
	e := newTestEnv(t)
	_, actor := e.newAdmin(t)
	u, email := e.newMailUser(t)
	other := e.newUser(t)
	ctx := context.Background()

	upper := strings.ToUpper(email)
	if err := e.svc.UpdateUser(ctx, e.client, e.as(t, actor), other.ID, UserUpdate{Email: &upper}); !errors.Is(err, ErrEmailExists) {
		t.Errorf("duplicate (other case): err = %v", err)
	}
	for _, bad := range []string{"no-at-sign", "Name <a@example.test>", "a@example.test\r\nBcc: x@y", "a@b@c"} {
		if err := e.svc.UpdateUser(ctx, e.client, e.as(t, actor), other.ID, UserUpdate{Email: &bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	empty := ""
	if err := e.svc.UpdateUser(ctx, e.client, e.as(t, actor), u.ID, UserUpdate{Email: &empty}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.GetUser(ctx, u.ID); got.Email != "" {
		t.Errorf("email not removed: %q", got.Email)
	}
	// Without an address there is no mail, and that is not an error.
	if a, err := e.svc.ResetUser(ctx, e.client, e.as(t, actor), u.ID); err != nil || a.Emailed {
		t.Errorf("reset without email: %+v, %v", a, err)
	}
}
