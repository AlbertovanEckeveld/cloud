package mailer

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"time"

	"github.com/AlbertovanEckeveld/cloud/internal/core/config"
)

const smtpTimeout = 30 * time.Second

// client connects to the configured SMTP server and authenticates.
// net/smtp's PlainAuth refuses to send credentials over an unencrypted
// connection (except to localhost), which is a second safety net.
func dial(ctx context.Context, cfg *config.Mail, heloName string) (*smtp.Client, error) {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	tlsConfig := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{Timeout: 10 * time.Second}

	var conn net.Conn
	var err error
	if cfg.Security == config.MailTLS {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", addr, err)
	}
	// ctx bounds connecting; this deadline bounds the whole SMTP exchange.
	if err := conn.SetDeadline(time.Now().Add(smtpTimeout)); err != nil {
		conn.Close()
		return nil, err
	}
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("smtp greeting: %w", err)
	}
	if err := c.Hello(heloName); err != nil {
		c.Close()
		return nil, fmt.Errorf("smtp hello: %w", err)
	}
	if cfg.Security == config.MailStartTLS {
		if err := c.StartTLS(tlsConfig); err != nil {
			c.Close()
			return nil, fmt.Errorf("smtp starttls: %w", err)
		}
	}
	if cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password.Reveal(), cfg.Host)); err != nil {
			c.Close()
			return nil, fmt.Errorf("smtp auth: %w", err)
		}
	}
	return c, nil
}

// send delivers one rendered message.
func send(ctx context.Context, cfg *config.Mail, heloName, from, to string, raw []byte) error {
	c, err := dial(ctx, cfg, heloName)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt to: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	return c.Quit()
}

// verify connects and authenticates without sending anything.
func verify(ctx context.Context, cfg *config.Mail, heloName string) error {
	c, err := dial(ctx, cfg, heloName)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Quit()
}
