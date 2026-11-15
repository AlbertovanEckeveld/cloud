// Package mailer sends plain-text mail through an outbox table: messages are
// queued in the same database transaction as the action that causes them,
// and a background dispatcher delivers them over SMTP with retries.
package mailer

import (
	"bytes"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"
)

// Message is one e-mail to one recipient.
type Message struct {
	// To is a bare address such as "alice@example.com".
	To      string
	Subject string
	// Body is plain text; lines end with "\n".
	Body string
}

// ErrInvalidMessage is returned for messages that could alter the mail
// headers or are otherwise unsafe to send.
var ErrInvalidMessage = errors.New("invalid mail message")

// ValidateAddress checks that s is a single bare e-mail address.
func ValidateAddress(s string) error {
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || a.Name != "" || len(s) > 254 {
		return fmt.Errorf("%w: invalid recipient", ErrInvalidMessage)
	}
	return nil
}

func (m Message) validate() error {
	if err := ValidateAddress(m.To); err != nil {
		return err
	}
	// A CR or LF in a header would let the value add headers of its own
	// (header injection), e.g. an extra Bcc recipient.
	if strings.ContainsAny(m.Subject, "\r\n") || m.Subject == "" || len(m.Subject) > 200 || !utf8.ValidString(m.Subject) {
		return fmt.Errorf("%w: invalid subject", ErrInvalidMessage)
	}
	if !utf8.ValidString(m.Body) {
		return fmt.Errorf("%w: body is not UTF-8", ErrInvalidMessage)
	}
	return nil
}

// build renders the message in RFC 5322 format. The body is quoted-printable
// so that any text survives every mail server unchanged.
func build(from *mail.Address, m Message, date time.Time, messageID string) ([]byte, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	header := func(name, value string) { fmt.Fprintf(&b, "%s: %s\r\n", name, value) }
	header("From", from.String()) // encodes a non-ASCII display name (RFC 2047)
	header("To", "<"+m.To+">")
	header("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	header("Date", date.Format(time.RFC1123Z))
	header("Message-ID", "<"+messageID+">")
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "quoted-printable")
	// RFC 3834: automatic mail; keeps out-of-office replies from answering it.
	header("Auto-Submitted", "auto-generated")
	b.WriteString("\r\n")

	qp := quotedprintable.NewWriter(&b) // converts "\n" to CRLF
	if _, err := qp.Write([]byte(m.Body)); err != nil {
		return nil, fmt.Errorf("encode body: %w", err)
	}
	if err := qp.Close(); err != nil {
		return nil, fmt.Errorf("encode body: %w", err)
	}
	return b.Bytes(), nil
}
