package mailer

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlbertovanEckeveld/cloud/internal/core/config"
)

var testFrom = &mail.Address{Name: "Cloud Ø", Address: "cloud@example.test"}

func TestBuildMessage(t *testing.T) {
	raw, err := build(testFrom, Message{To: "alice@example.test", Subject: "Wachtwoord gewijzigd ✓", Body: "Hallo Älice,\nregel twee\n"},
		time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC), "id-1@example.test")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("not a valid RFC 5322 message: %v", err)
	}
	dec := new(mime.WordDecoder)
	subject, _ := dec.DecodeHeader(msg.Header.Get("Subject"))
	from, _ := msg.Header.AddressList("From")
	checks := map[string]bool{
		"subject":        subject == "Wachtwoord gewijzigd ✓",
		"from":           len(from) == 1 && from[0].Name == "Cloud Ø" && from[0].Address == "cloud@example.test",
		"to":             msg.Header.Get("To") == "<alice@example.test>",
		"message-id":     msg.Header.Get("Message-ID") == "<id-1@example.test>",
		"auto-submitted": msg.Header.Get("Auto-Submitted") == "auto-generated",
		"crlf":           strings.Contains(string(raw), "\r\n\r\n"),
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("%s wrong:\n%s", name, raw)
		}
	}
	body, _ := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if string(body) != "Hallo Älice,\r\nregel twee\r\n" {
		t.Errorf("body = %q", body)
	}
}

func TestBuildRejectsHeaderInjection(t *testing.T) {
	for name, m := range map[string]Message{
		"subject CRLF":    {To: "a@example.test", Subject: "Hi\r\nBcc: evil@example.test", Body: "x"},
		"subject LF":      {To: "a@example.test", Subject: "Hi\nBcc: evil@example.test", Body: "x"},
		"to with name":    {To: "Evil <a@example.test>", Subject: "Hi", Body: "x"},
		"two recipients":  {To: "a@example.test, b@example.test", Subject: "Hi", Body: "x"},
		"to with newline": {To: "a@example.test\r\nBcc: b@example.test", Subject: "Hi", Body: "x"},
		"empty subject":   {To: "a@example.test", Subject: "", Body: "x"},
		"invalid utf8":    {To: "a@example.test", Subject: "Hi", Body: "\xff"},
		"not an address":  {To: "nobody", Subject: "Hi", Body: "x"},
	} {
		if _, err := build(testFrom, m, time.Now(), "x@y"); !errors.Is(err, ErrInvalidMessage) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// fakeSMTP is a minimal SMTP server that records one session.
type fakeSMTP struct {
	ln       net.Listener
	mu       sync.Mutex
	auth     string
	mailFrom string
	rcptTo   string
	data     string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go f.serve()
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) serve() {
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	r := bufio.NewReader(conn)
	reply := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	reply("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		f.mu.Lock()
		switch cmd {
		case "EHLO":
			reply("250-fake")
			reply("250 AUTH PLAIN")
		case "AUTH":
			raw, _ := base64.StdEncoding.DecodeString(strings.Fields(line)[2])
			f.auth = string(raw)
			reply("235 ok")
		case "MAIL":
			f.mailFrom = line
			reply("250 ok")
		case "RCPT":
			f.rcptTo = line
			reply("250 ok")
		case "DATA":
			reply("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.data = b.String()
			reply("250 queued")
		case "QUIT":
			reply("221 bye")
			f.mu.Unlock()
			return
		default:
			reply("502 unknown")
		}
		f.mu.Unlock()
	}
}

func TestSendOverSMTP(t *testing.T) {
	f := newFakeSMTP(t)
	cfg := &config.Mail{Host: "127.0.0.1", Port: f.port(), Security: config.MailNone,
		Username: "cloud@example.test", Password: "s3cret", From: "cloud@example.test"}
	raw, _ := build(testFrom, Message{To: "bob@example.test", Subject: "Test", Body: "Hallo\n"}, time.Now(), "1@example.test")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := send(ctx, cfg, "cloud.example.test", "cloud@example.test", "bob@example.test", raw); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auth != "\x00cloud@example.test\x00s3cret" {
		t.Errorf("auth = %q", f.auth)
	}
	if f.mailFrom != "MAIL FROM:<cloud@example.test>" || !strings.HasPrefix(f.rcptTo, "RCPT TO:<bob@example.test>") {
		t.Errorf("envelope = %q / %q", f.mailFrom, f.rcptTo)
	}
	if !strings.Contains(f.data, "Subject: Test\r\n") {
		t.Errorf("data = %q", f.data)
	}
}

func TestDisabledMailer(t *testing.T) {
	m, err := New(nil, nil, nil, "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Enabled() {
		t.Fatal("mailer without config must be disabled")
	}
	if err := m.Enqueue(context.Background(), nil, Message{}); !errors.Is(err, ErrDisabled) {
		t.Errorf("Enqueue: err = %v", err)
	}
}
