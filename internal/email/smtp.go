// Package email sends Watchtower's personal notifications and digests over
// SMTP, which every mail provider supports, and renders their content.
package email

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// TLS modes. "tls" is implicit TLS (SMTPS, usually port 465); "starttls"
// upgrades a plain connection (usually port 587) and fails if the server
// does not offer it; "none" is for a relay on a trusted network.
const (
	TLSImplicit = "tls"
	TLSStart    = "starttls"
	TLSNone     = "none"
)

// SMTP is a mail server Watchtower submits to.
type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	TLS      string
	From     mail.Address
	// TLSConfig overrides certificate verification, for tests.
	TLSConfig *tls.Config
}

// Message is one email to one recipient.
type Message struct {
	To      mail.Address
	Subject string
	Text    string
	HTML    string
}

// Sender delivers messages.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

const smtpTimeout = 30 * time.Second

// Send submits m. Every network step is bounded by ctx or smtpTimeout.
func (s *SMTP) Send(ctx context.Context, m Message) error {
	body, err := s.build(m, time.Now())
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	d := net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", addr, err)
	}
	deadline := time.Now().Add(smtpTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	if err := conn.SetDeadline(deadline); err != nil {
		conn.Close()
		return err
	}
	// Cancellation interrupts whatever the connection is doing.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	tlsConf := s.tlsConfig()
	if s.TLS == TLSImplicit {
		tc := tls.Client(conn, tlsConf)
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return fmt.Errorf("TLS with %s: %w", addr, err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMTP greeting from %s: %w", addr, err)
	}
	defer c.Close()
	if err := c.Hello(s.helloName()); err != nil {
		return fmt.Errorf("SMTP hello: %w", err)
	}
	if s.TLS == TLSStart {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("the mail server does not offer STARTTLS; set WATCHTOWER_SMTP_TLS=tls for implicit TLS, or none for a trusted relay")
		}
		if err := c.StartTLS(tlsConf); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}
	if s.Username != "" {
		// PlainAuth refuses to send credentials over an unencrypted
		// connection to anything but localhost.
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return fmt.Errorf("SMTP authentication: %w", err)
		}
	}
	if err := c.Mail(s.From.Address); err != nil {
		return fmt.Errorf("SMTP sender %s: %w", s.From.Address, err)
	}
	if err := c.Rcpt(m.To.Address); err != nil {
		return fmt.Errorf("SMTP recipient: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("SMTP data: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("SMTP data: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP data: %w", err)
	}
	return c.Quit()
}

func (s *SMTP) tlsConfig() *tls.Config {
	if s.TLSConfig != nil {
		c := s.TLSConfig.Clone()
		if c.ServerName == "" {
			c.ServerName = s.Host
		}
		return c
	}
	return &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
}

// helloName identifies this client; the sender's domain is a stable,
// meaningful choice that does not reveal the host's internal name.
func (s *SMTP) helloName() string {
	if i := strings.LastIndexByte(s.From.Address, '@'); i >= 0 {
		return s.From.Address[i+1:]
	}
	return "localhost"
}

// oneLine strips characters that could start a new header line.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return ' '
		}
		return r
	}, s)
}

// build renders m as a multipart/alternative message with text and HTML parts.
func (s *SMTP) build(m Message, now time.Time) ([]byte, error) {
	if _, err := mail.ParseAddress(m.To.Address); err != nil {
		return nil, fmt.Errorf("recipient address: %w", err)
	}
	var id [12]byte
	rand.Read(id[:])
	from := mail.Address{Name: oneLine(s.From.Name), Address: s.From.Address}
	to := mail.Address{Name: oneLine(m.To.Name), Address: m.To.Address}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := []struct{ k, v string }{
		{"From", from.String()},
		{"To", to.String()},
		{"Subject", mime.QEncoding.Encode("utf-8", oneLine(m.Subject))},
		{"Date", now.Format(time.RFC1123Z)},
		{"Message-ID", "<" + hex.EncodeToString(id[:]) + "@" + s.helloName() + ">"},
		{"MIME-Version", "1.0"},
		{"Auto-Submitted", "auto-generated"},
		{"X-Auto-Response-Suppress", "All"},
		{"Content-Type", `multipart/alternative; boundary="` + mw.Boundary() + `"`},
	}
	var head bytes.Buffer
	for _, kv := range h {
		head.WriteString(kv.k + ": " + kv.v + "\r\n")
	}
	head.WriteString("\r\n")
	for _, part := range []struct{ ctype, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
		if part.body == "" {
			continue
		}
		pw, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.ctype + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		qw := quotedprintable.NewWriter(pw)
		if _, err := qw.Write([]byte(part.body)); err != nil {
			return nil, err
		}
		if err := qw.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return append(head.Bytes(), buf.Bytes()...), nil
}
