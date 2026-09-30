// Package mailer implements domain.Mailer: an SMTP sender (STARTTLS or
// implicit TLS, multipart text+HTML), a development sender that only logs,
// an asynchronous queue wrapper, and the bilingual (Mongolian + English)
// transactional email templates used by the identity service.
package mailer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// DefaultTimeout bounds one SMTP transaction when the context has no deadline.
const DefaultTimeout = 30 * time.Second

// SMTP sends mail through an SMTP relay. Port 465 uses implicit TLS; any
// other port upgrades with STARTTLS when the server offers it (required when
// credentials are configured, so passwords never cross the wire in clear).
type SMTP struct {
	host string
	port string
	user string
	pass string
	from string

	// tlsConfig is used for STARTTLS / implicit TLS (tests may replace it).
	tlsConfig *tls.Config
	now       func() time.Time
}

var _ domain.Mailer = (*SMTP)(nil)

// NewSMTP returns an SMTP mailer. from may be a bare address or
// "Name <addr>". user/pass may be empty for unauthenticated relays.
func NewSMTP(host, port, user, pass, from string) *SMTP {
	if port == "" {
		port = "587"
	}
	return &SMTP{
		host: host, port: port, user: user, pass: pass, from: from,
		tlsConfig: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12},
		now:       time.Now,
	}
}

// Send delivers one message with a text and an HTML alternative.
func (m *SMTP) Send(ctx context.Context, to, subject, textBody, htmlBody string) error {
	if m.host == "" {
		return errors.New("smtp: host not configured")
	}
	fromAddr, err := mail.ParseAddress(m.from)
	if err != nil {
		return fmt.Errorf("smtp: invalid from address %q: %w", m.from, err)
	}
	toAddr, err := mail.ParseAddress(to)
	if err != nil {
		return fmt.Errorf("smtp: invalid recipient %q: %w", to, domain.ErrInvalid)
	}
	msg, err := BuildMessage(fromAddr, toAddr, subject, textBody, htmlBody, m.now())
	if err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}
	return m.deliver(ctx, fromAddr.Address, toAddr.Address, msg)
}

func (m *SMTP) deliver(ctx context.Context, from, to string, msg []byte) error {
	addr := net.JoinHostPort(m.host, m.port)
	var d net.Dialer
	var conn net.Conn
	var err error
	implicitTLS := m.port == "465"
	if implicitTLS {
		conn, err = (&tls.Dialer{NetDialer: &d, Config: m.tlsConfig}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("smtp: dial %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	// Abort a hung conversation when the context is canceled.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	c, err := smtp.NewClient(conn, m.host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp: handshake: %w", err)
	}
	defer c.Close()

	if !implicitTLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(m.tlsConfig); err != nil {
				return fmt.Errorf("smtp: starttls: %w", err)
			}
		} else if m.user != "" && !isLocalhost(m.host) {
			return errors.New("smtp: server does not support STARTTLS; refusing to send credentials in clear text")
		}
	}
	if m.user != "" {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(smtp.PlainAuth("", m.user, m.pass, m.host)); err != nil {
				return fmt.Errorf("smtp: auth: %w", err)
			}
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("smtp: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp: RCPT TO: %w", err)
	}
	wc, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp: DATA: %w", err)
	}
	if _, err := wc.Write(msg); err != nil {
		_ = wc.Close()
		return fmt.Errorf("smtp: write body: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("smtp: end DATA: %w", err)
	}
	if err := c.Quit(); err != nil {
		return fmt.Errorf("smtp: QUIT: %w", err)
	}
	return nil
}

func isLocalhost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// BuildMessage renders an RFC 5322 message with a multipart/alternative
// body (quoted-printable UTF-8 text and HTML parts). Header values are
// validated against CR/LF injection.
func BuildMessage(from, to *mail.Address, subject, textBody, htmlBody string, now time.Time) ([]byte, error) {
	if strings.ContainsAny(subject, "\r\n") {
		return nil, fmt.Errorf("smtp: subject contains a line break: %w", domain.ErrInvalid)
	}
	boundary, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	msgID, err := randomHex(12)
	if err != nil {
		return nil, err
	}
	domainPart := "callgo.mn"
	if _, d, ok := strings.Cut(from.Address, "@"); ok && d != "" {
		domainPart = d
	}
	var b bytes.Buffer
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", from.String())
	header("To", to.String())
	header("Subject", mime.QEncoding.Encode("utf-8", subject))
	header("Date", now.UTC().Format(time.RFC1123Z))
	header("Message-ID", "<"+msgID+"@"+domainPart+">")
	header("MIME-Version", "1.0")
	if htmlBody == "" {
		header("Content-Type", `text/plain; charset="utf-8"`)
		header("Content-Transfer-Encoding", "quoted-printable")
		b.WriteString("\r\n")
		if err := writeQP(&b, textBody); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}
	header("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	for _, part := range []struct{ ctype, body string }{
		{"text/plain", textBody},
		{"text/html", htmlBody},
	} {
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString(`Content-Type: ` + part.ctype + `; charset="utf-8"` + "\r\n")
		b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
		if err := writeQP(&b, part.body); err != nil {
			return nil, err
		}
		b.WriteString("\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes(), nil
}

func writeQP(b *bytes.Buffer, s string) error {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", "\r\n")
	w := quotedprintable.NewWriter(b)
	if _, err := w.Write([]byte(s)); err != nil {
		return fmt.Errorf("smtp: encode body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp: encode body: %w", err)
	}
	return nil
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Log is a development mailer: it writes each message (including its text
// body, which carries the verification / reset links) to the logger.
type Log struct {
	log zerolog.Logger
}

var _ domain.Mailer = (*Log)(nil)

// NewLog returns a mailer that only logs.
func NewLog(log zerolog.Logger) *Log {
	return &Log{log: log.With().Str("component", "mailer").Logger()}
}

// Send logs the message and never fails.
func (l *Log) Send(_ context.Context, to, subject, textBody, _ string) error {
	l.log.Info().Str("to", to).Str("subject", subject).Str("body", textBody).Msg("email (log mailer, not sent)")
	return nil
}
