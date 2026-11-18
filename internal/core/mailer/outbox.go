package mailer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlbertovanEckeveld/cloud/internal/core/config"
	"github.com/AlbertovanEckeveld/cloud/internal/core/sealer"
)

const (
	pollInterval = 5 * time.Second
	batchSize    = 10
	maxAttempts  = 8 // with exponential backoff: gives up after about four hours
)

// ErrDisabled is returned by Enqueue when no SMTP server is configured.
var ErrDisabled = errors.New("mail is not configured")

// Execer is implemented by both a pool and a transaction, so a message can
// be queued atomically with the change that causes it.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type sendFunc func(ctx context.Context, from, to string, raw []byte) error

// Mailer queues and delivers mail.
type Mailer struct {
	db       *pgxpool.Pool
	sealer   *sealer.Sealer
	cfg      *config.Mail
	from     *mail.Address
	heloName string
	log      *slog.Logger
	now      func() time.Time
	send     sendFunc
}

// New returns a mailer. With cfg nil, mail is disabled: Enabled reports
// false and Enqueue returns ErrDisabled.
func New(db *pgxpool.Pool, s *sealer.Sealer, cfg *config.Mail, heloName string, logger *slog.Logger) (*Mailer, error) {
	m := &Mailer{db: db, sealer: s, cfg: cfg, heloName: heloName, log: logger, now: time.Now}
	if cfg == nil {
		return m, nil
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("mail sender: %w", err)
	}
	m.from = from
	if cfg.DevDir != "" {
		if err := prepareDir(cfg.DevDir); err != nil {
			return nil, err
		}
		m.send = m.writeToDir(cfg.DevDir)
		return m, nil
	}
	m.send = func(ctx context.Context, from, to string, raw []byte) error {
		return send(ctx, cfg, heloName, from, to, raw)
	}
	return m, nil
}

// Enabled reports whether mail can be sent.
func (m *Mailer) Enabled() bool { return m.cfg != nil }

// Verify connects to the SMTP server and authenticates, without sending.
func (m *Mailer) Verify(ctx context.Context) error {
	if !m.Enabled() {
		return ErrDisabled
	}
	if m.cfg.DevDir != "" {
		return prepareDir(m.cfg.DevDir)
	}
	return verify(ctx, m.cfg, m.heloName)
}

// bodyAD binds a sealed body to its outbox row.
func bodyAD(id uuid.UUID) []byte { return []byte("mail:" + id.String()) }

// Enqueue validates msg and stores it for delivery. Pass the transaction of
// the action that causes the mail, so both commit or neither does. The body
// may contain secrets such as activation links, so it is stored encrypted.
func (m *Mailer) Enqueue(ctx context.Context, q Execer, msg Message) error {
	if !m.Enabled() {
		return ErrDisabled
	}
	if err := msg.validate(); err != nil {
		return err
	}
	id := uuid.Must(uuid.NewV7())
	now := m.now()
	_, err := q.Exec(ctx, `
		INSERT INTO core.mail_outbox (id, recipient, subject, body_sealed, created_at, next_attempt_at)
		VALUES ($1, $2, $3, $4, $5, $5)`,
		id, msg.To, msg.Subject, m.sealer.Seal([]byte(msg.Body), bodyAD(id)), now)
	if err != nil {
		return fmt.Errorf("queue mail: %w", err)
	}
	return nil
}

// Run delivers queued mail until ctx is cancelled.
func (m *Mailer) Run(ctx context.Context) {
	if !m.Enabled() {
		return
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if err := m.dispatch(ctx); err != nil && ctx.Err() == nil {
			m.log.Warn("mail dispatch failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// dispatch sends everything that is due.
func (m *Mailer) dispatch(ctx context.Context) error {
	for {
		n, err := m.sendBatch(ctx)
		if err != nil || n < batchSize {
			return err
		}
	}
}

type queued struct {
	id        uuid.UUID
	recipient string
	subject   string
	sealed    []byte
	attempts  int
}

// sendBatch claims due messages with FOR UPDATE SKIP LOCKED: several
// instances can dispatch at once without sending a message twice.
func (m *Mailer) sendBatch(ctx context.Context) (int, error) {
	var count int
	err := pgx.BeginFunc(ctx, m.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, recipient, subject, body_sealed, attempts FROM core.mail_outbox
			WHERE next_attempt_at <= $1 ORDER BY created_at LIMIT $2
			FOR UPDATE SKIP LOCKED`, m.now(), batchSize)
		if err != nil {
			return fmt.Errorf("claim mail: %w", err)
		}
		batch, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (queued, error) {
			var q queued
			return q, row.Scan(&q.id, &q.recipient, &q.subject, &q.sealed, &q.attempts)
		})
		if err != nil {
			return fmt.Errorf("claim mail: %w", err)
		}
		count = len(batch)
		for _, q := range batch {
			if err := m.deliver(ctx, tx, q); err != nil {
				return err
			}
		}
		return nil
	})
	return count, err
}

// deliver sends one message and records the outcome. A send failure is not
// an error of the batch: the message is rescheduled with backoff.
func (m *Mailer) deliver(ctx context.Context, tx pgx.Tx, q queued) error {
	sendErr := func() error {
		body, err := m.sealer.Open(q.sealed, bodyAD(q.id))
		if err != nil {
			return err
		}
		domain := m.from.Address[strings.LastIndex(m.from.Address, "@")+1:]
		raw, err := build(m.from, Message{To: q.recipient, Subject: q.subject, Body: string(body)}, m.now(), q.id.String()+"@"+domain)
		if err != nil {
			return err
		}
		return m.send(ctx, m.from.Address, q.recipient, raw)
	}()

	if sendErr == nil {
		m.log.Info("mail sent", "id", q.id, "subject", q.subject)
		_, err := tx.Exec(ctx, `DELETE FROM core.mail_outbox WHERE id = $1`, q.id)
		return err
	}
	attempts := q.attempts + 1
	if attempts >= maxAttempts {
		// Give up rather than keep a (possibly secret) body around forever.
		m.log.Error("mail dropped after repeated failures", "id", q.id, "subject", q.subject, "error", sendErr)
		_, err := tx.Exec(ctx, `DELETE FROM core.mail_outbox WHERE id = $1`, q.id)
		return err
	}
	backoff := time.Minute << (attempts - 1) // 1, 2, 4, … minutes
	m.log.Warn("mail delivery failed, will retry", "id", q.id, "attempt", attempts, "retry_in", backoff, "error", sendErr)
	errText := sendErr.Error()
	if len(errText) > 500 {
		errText = errText[:500]
	}
	_, err := tx.Exec(ctx, `
		UPDATE core.mail_outbox SET attempts = $2, next_attempt_at = $3, last_error = $4 WHERE id = $1`,
		q.id, attempts, m.now().Add(backoff), errText)
	return err
}
