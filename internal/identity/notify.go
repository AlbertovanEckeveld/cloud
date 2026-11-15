package identity

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/AlbertovanEckeveld/cloud/internal/core/mailer"
)

// notice is a security notification to the owner of an account.
type notice struct {
	subject string
	text    string
}

var (
	noticePasswordChanged = notice{"Je wachtwoord is gewijzigd", "Het wachtwoord van je account is zojuist gewijzigd. Je bent op je andere apparaten uitgelogd."}
	noticePasskeyAdded    = notice{"Er is een passkey toegevoegd", "Er is zojuist een passkey aan je account toegevoegd."}
	noticePasskeyRemoved  = notice{"Er is een passkey verwijderd", "Er is zojuist een passkey van je account verwijderd."}
	noticeRecoveryUsed    = notice{"Er is ingelogd met een herstelcode", "Er is zojuist op je account ingelogd met een herstelcode in plaats van je authenticator-app."}
	noticeRecoveryNew     = notice{"Nieuwe herstelcodes aangemaakt", "Er zijn zojuist nieuwe herstelcodes voor je account aangemaakt. De oude codes werken niet meer."}
)

// recipient returns the user's name and e-mail address; email is empty when
// the user has none.
func recipient(ctx context.Context, q dbtx, userID uuid.UUID) (name, email string, err error) {
	var addr *string
	if err := q.QueryRow(ctx, `SELECT display_name, email FROM identity.users WHERE id = $1`, userID).Scan(&name, &addr); err != nil {
		return "", "", fmt.Errorf("load recipient: %w", err)
	}
	if addr != nil {
		email = *addr
	}
	return name, email, nil
}

// notify queues a security notification in the caller's transaction, so it
// is only sent if the change itself is committed. Users without an e-mail
// address, or a platform without mail, simply get no notification.
func (s *Service) notify(ctx context.Context, tx dbtx, c Client, userID uuid.UUID, n notice) error {
	if !s.mail.Enabled() {
		return nil
	}
	name, email, err := recipient(ctx, tx, userID)
	if err != nil || email == "" {
		return err
	}
	body := fmt.Sprintf("Hallo %s,\n\n%s\n\nTijdstip: %s\nIP-adres: %s\n\nWas jij dit niet? Neem dan direct contact op met je beheerder.\n\n%s\n",
		name, n.text, s.now().UTC().Format("2006-01-02 15:04 UTC"), orUnknown(c.IP), s.signature())
	return s.mail.Enqueue(ctx, tx, mailer.Message{To: email, Subject: n.subject, Body: body})
}

// activationMail queues the activation link for a new or reset account.
// It reports whether a mail was queued.
func (s *Service) activationMail(ctx context.Context, tx dbtx, userID uuid.UUID, username, link string, reset bool) (bool, error) {
	if !s.mail.Enabled() {
		return false, nil
	}
	name, email, err := recipient(ctx, tx, userID)
	if err != nil || email == "" {
		return false, err
	}
	subject, intro := "Je account voor Cloud", "Er is een account voor je aangemaakt."
	if reset {
		subject, intro = "Je account is opnieuw ingesteld", "Je account is opnieuw ingesteld: je oude wachtwoord, authenticator-app en passkeys werken niet meer."
	}
	body := fmt.Sprintf("Hallo %s,\n\n%s Je gebruikersnaam is: %s\n\n"+
		"Kies met deze link een wachtwoord en koppel een authenticator-app:\n\n%s\n\n"+
		"De link is 72 uur geldig en werkt één keer. Deel hem met niemand. Had je dit niet verwacht? Dan kun je deze e-mail negeren.\n\n%s\n",
		name, intro, username, link, s.signature())
	if err := s.mail.Enqueue(ctx, tx, mailer.Message{To: email, Subject: subject, Body: body}); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) signature() string { return "Cloud · " + s.opts.PublicURL.String() }

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "onbekend"
	}
	return s
}

// normalizeEmail validates an optional address; "" means none.
func normalizeEmail(s string) (*string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if err := mailer.ValidateAddress(s); err != nil {
		return nil, &InputError{"Dit is geen geldig e-mailadres."}
	}
	return &s, nil
}
