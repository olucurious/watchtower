package email

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a minimal submission server: EHLO, optional STARTTLS, AUTH
// PLAIN, MAIL, RCPT, DATA and QUIT.
type fakeSMTP struct {
	addr     string
	tls      *tls.Config
	implicit bool // TLS from the first byte
	starttls bool // offer STARTTLS

	mu       sync.Mutex
	auth     string // decoded AUTH PLAIN credentials
	authTLS  bool   // whether AUTH arrived over TLS
	from, to string
	data     string
}

func selfSigned(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mail.test"}, DNSNames: []string{"mail.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}, pool
}

func startFake(t *testing.T, f *fakeSMTP) { startFakeOn(t, f, "127.0.0.1:0") }

func startFakeOn(t *testing.T, f *fakeSMTP, listen string) {
	t.Helper()
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f.addr = ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	secure := false
	if f.implicit {
		c = tls.Server(c, f.tls)
		secure = true
	}
	r, w := bufio.NewReader(c), c
	say := func(s string) { io.WriteString(w, s+"\r\n") }
	say("220 mail.test ready")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO"):
			if f.starttls && !secure {
				say("250-mail.test")
				say("250-STARTTLS")
			} else {
				say("250-mail.test")
			}
			say("250 AUTH PLAIN")
		case upper == "STARTTLS":
			say("220 go ahead")
			tc := tls.Server(c, f.tls)
			if tc.Handshake() != nil {
				return
			}
			c, secure = tc, true
			r, w = bufio.NewReader(tc), tc
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(cmd[len("AUTH PLAIN"):]))
			f.mu.Lock()
			f.auth, f.authTLS = strings.ReplaceAll(string(raw), "\x00", "|"), secure
			f.mu.Unlock()
			say("235 ok")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			f.mu.Lock()
			f.from = cmd[len("MAIL FROM:"):]
			f.mu.Unlock()
			say("250 ok")
		case strings.HasPrefix(upper, "RCPT TO:"):
			f.mu.Lock()
			f.to = cmd[len("RCPT TO:"):]
			f.mu.Unlock()
			say("250 ok")
		case upper == "DATA":
			say("354 send it")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			f.mu.Lock()
			f.data = b.String()
			f.mu.Unlock()
			say("250 queued")
		case upper == "QUIT":
			say("221 bye")
			return
		default:
			say("502 not implemented")
		}
	}
}

func client(f *fakeSMTP, mode string, pool interface{}) *SMTP {
	host, port, _ := net.SplitHostPort(f.addr)
	p := 0
	for _, ch := range port {
		p = p*10 + int(ch-'0')
	}
	s := &SMTP{Host: host, Port: p, Username: "api_token", Password: "s3cret", TLS: mode,
		From: mail.Address{Name: "Watchtower", Address: "errors@example.com"}}
	if pool != nil {
		s.TLSConfig = &tls.Config{RootCAs: pool.(*x509.CertPool), ServerName: "mail.test"}
	}
	return s
}

func TestSendModes(t *testing.T) {
	serverTLS, pool := selfSigned(t)
	msg := Message{To: mail.Address{Name: "Ada", Address: "ada@example.com"}, Subject: "Héllo\r\nBcc: victim@example.com", Text: "plain ünïcode", HTML: "<p>html</p>"}
	for _, tc := range []struct {
		name string
		fake *fakeSMTP
		mode string
	}{
		{"implicit TLS", &fakeSMTP{tls: serverTLS, implicit: true}, TLSImplicit},
		{"STARTTLS", &fakeSMTP{tls: serverTLS, starttls: true}, TLSStart},
	} {
		t.Run(tc.name, func(t *testing.T) {
			startFake(t, tc.fake)
			if err := client(tc.fake, tc.mode, pool).Send(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			f := tc.fake
			if f.auth != "|api_token|s3cret" || !f.authTLS {
				t.Errorf("credentials %q over TLS=%v", f.auth, f.authTLS)
			}
			if f.from != "<errors@example.com>" || f.to != "<ada@example.com>" {
				t.Errorf("envelope %s -> %s", f.from, f.to)
			}
			m, err := mail.ReadMessage(strings.NewReader(f.data))
			if err != nil {
				t.Fatal(err)
			}
			subject, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
			if subject != "Héllo  Bcc: victim@example.com" || m.Header.Get("Bcc") != "" {
				t.Errorf("subject %q must stay one header", subject)
			}
			_, params, _ := mime.ParseMediaType(m.Header.Get("Content-Type"))
			mr := multipart.NewReader(m.Body, params["boundary"])
			var parts []string
			for {
				p, err := mr.NextPart() // decodes quoted-printable
				if err != nil {
					break
				}
				b, _ := io.ReadAll(p)
				parts = append(parts, p.Header.Get("Content-Type")+"="+string(b))
			}
			if len(parts) != 2 || parts[0] != "text/plain; charset=utf-8=plain ünïcode" || parts[1] != "text/html; charset=utf-8=<p>html</p>" {
				t.Errorf("parts %q", parts)
			}
		})
	}
}

func TestSendRefusesMissingSTARTTLS(t *testing.T) {
	serverTLS, pool := selfSigned(t)
	f := &fakeSMTP{tls: serverTLS} // offers no STARTTLS
	startFake(t, f)
	err := client(f, TLSStart, pool).Send(context.Background(), Message{To: mail.Address{Address: "ada@example.com"}, Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") || f.auth != "" {
		t.Errorf("err=%v auth=%q: must not fall back to plaintext", err, f.auth)
	}
}

func TestSendWithoutTLSKeepsCredentialsLocal(t *testing.T) {
	// A relay on the same host may be used without TLS.
	local := &fakeSMTP{}
	startFake(t, local)
	if err := client(local, TLSNone, nil).Send(context.Background(), Message{To: mail.Address{Address: "ada@example.com"}, Text: "x"}); err != nil {
		t.Fatal(err)
	}
	// Anywhere else, credentials are never sent over an unencrypted connection.
	remote := &fakeSMTP{}
	startFakeOn(t, remote, "127.0.0.2:0")
	err := client(remote, TLSNone, nil).Send(context.Background(), Message{To: mail.Address{Address: "ada@example.com"}, Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "unencrypted connection") || remote.auth != "" {
		t.Errorf("err=%v auth=%q: credentials sent in the clear", err, remote.auth)
	}
}

func TestSendRejectsBadRecipient(t *testing.T) {
	s := &SMTP{Host: "127.0.0.1", Port: 1, From: mail.Address{Address: "errors@example.com"}}
	if err := s.Send(context.Background(), Message{To: mail.Address{Address: "not an address"}}); err == nil || !strings.Contains(err.Error(), "recipient") {
		t.Errorf("err=%v", err)
	}
}
