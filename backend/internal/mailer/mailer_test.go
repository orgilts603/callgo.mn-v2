package mailer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// fakeSMTPServer speaks just enough SMTP for net/smtp (no STARTTLS).
type fakeSMTPServer struct {
	ln   net.Listener
	mu   sync.Mutex
	auth string
	from string
	rcpt string
	data string
}

func startFakeSMTP(t *testing.T) *fakeSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &fakeSMTPServer{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *fakeSMTPServer) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	w := func(line string) { _, _ = io.WriteString(c, line+"\r\n") }
	w("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			w("250-fake")
			w("250-AUTH PLAIN")
			w("250 8BITMIME")
		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			s.mu.Lock()
			s.auth = strings.TrimSpace(line[len("AUTH PLAIN"):])
			s.mu.Unlock()
			w("235 ok")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			s.mu.Lock()
			s.from = line[len("MAIL FROM:"):]
			s.mu.Unlock()
			w("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			s.mu.Lock()
			s.rcpt = line[len("RCPT TO:"):]
			s.mu.Unlock()
			w("250 ok")
		case cmd == "DATA":
			w("354 go ahead")
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
			s.mu.Lock()
			s.data = b.String()
			s.mu.Unlock()
			w("250 queued")
		case cmd == "QUIT":
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

func TestSMTPSendMultipart(t *testing.T) {
	srv := startFakeSMTP(t)
	host, port, err := net.SplitHostPort(srv.ln.Addr().String())
	require.NoError(t, err)
	m := NewSMTP(host, port, "user", "pass", "CallGo <noreply@callgo.mn>")

	err = m.Send(context.Background(), "Бат <bat@example.mn>", "Сайн уу — hello", "текст body", "<p>html</p>")
	require.NoError(t, err)

	srv.mu.Lock()
	defer srv.mu.Unlock()
	raw, err := base64.StdEncoding.DecodeString(srv.auth)
	require.NoError(t, err)
	assert.Equal(t, "\x00user\x00pass", string(raw))
	assert.True(t, strings.HasPrefix(srv.from, "<noreply@callgo.mn>"), srv.from)
	assert.Equal(t, "<bat@example.mn>", srv.rcpt)

	msg, err := mail.ReadMessage(strings.NewReader(srv.data))
	require.NoError(t, err)
	subj, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	require.NoError(t, err)
	assert.Equal(t, "Сайн уу — hello", subj)
	mt, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	require.NoError(t, err)
	assert.Equal(t, "multipart/alternative", mt)
	mr := multipart.NewReader(msg.Body, params["boundary"])
	var parts []string
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		b, err := io.ReadAll(p) // multipart decodes quoted-printable
		require.NoError(t, err)
		parts = append(parts, p.Header.Get("Content-Type")+"|"+string(b))
	}
	require.Len(t, parts, 2)
	assert.Contains(t, parts[0], "text/plain")
	assert.Contains(t, parts[0], "текст body")
	assert.Contains(t, parts[1], "text/html")
	assert.Contains(t, parts[1], "<p>html</p>")
}

func TestSMTPValidation(t *testing.T) {
	m := NewSMTP("127.0.0.1", "1", "", "", "noreply@callgo.mn")
	err := m.Send(context.Background(), "not an address", "s", "t", "")
	assert.ErrorIs(t, err, domain.ErrInvalid)
	_, err = BuildMessage(&mail.Address{Address: "a@b.mn"}, &mail.Address{Address: "c@d.mn"}, "evil\r\nBcc: x@y", "t", "", time.Now())
	assert.ErrorIs(t, err, domain.ErrInvalid)
	assert.Error(t, NewSMTP("", "25", "", "", "a@b.mn").Send(context.Background(), "c@d.mn", "s", "t", ""))
}

func TestIsLocalhost(t *testing.T) {
	assert.True(t, isLocalhost("127.0.0.1"))
	assert.True(t, isLocalhost("localhost"))
	assert.False(t, isLocalhost("smtp.example.com"))
}

func TestLogMailer(t *testing.T) {
	var buf bytes.Buffer
	l := NewLog(zerolog.New(&buf))
	require.NoError(t, l.Send(context.Background(), "a@b.mn", "Subject", "link https://x/verify?token=abc", "<p/>"))
	assert.Contains(t, buf.String(), "token=abc")
	assert.Contains(t, buf.String(), "a@b.mn")
}

type flakyMailer struct {
	mu    sync.Mutex
	fails int
	sent  []string
	calls int
}

func (f *flakyMailer) Send(_ context.Context, to, _, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fails > 0 {
		f.fails--
		return errors.New("temporary failure")
	}
	f.sent = append(f.sent, to)
	return nil
}

func TestQueueRetriesAndDrains(t *testing.T) {
	inner := &flakyMailer{fails: 1}
	q := NewQueue(inner, zerolog.Nop(), 4)
	q.RetryDelay = time.Millisecond
	require.NoError(t, q.Send(context.Background(), "a@b.mn", "s", "t", ""))
	require.NoError(t, q.Send(context.Background(), "c@d.mn", "s", "t", ""))
	require.NoError(t, q.Close(context.Background()))
	inner.mu.Lock()
	assert.Equal(t, []string{"a@b.mn", "c@d.mn"}, inner.sent)
	assert.Equal(t, 3, inner.calls)
	inner.mu.Unlock()
	assert.ErrorIs(t, q.Send(context.Background(), "x@y.mn", "s", "t", ""), ErrQueueClosed)
}

type blockingMailer struct{ release chan struct{} }

func (b *blockingMailer) Send(context.Context, string, string, string, string) error {
	<-b.release
	return nil
}

func TestQueueFull(t *testing.T) {
	b := &blockingMailer{release: make(chan struct{})}
	q := NewQueue(b, zerolog.Nop(), 1)
	// First message is picked up by the worker (blocked), second fills the buffer.
	require.NoError(t, q.Send(context.Background(), "1@b.mn", "s", "t", ""))
	require.Eventually(t, func() bool { return len(q.ch) == 0 }, time.Second, time.Millisecond)
	require.NoError(t, q.Send(context.Background(), "2@b.mn", "s", "t", ""))
	assert.ErrorIs(t, q.Send(context.Background(), "3@b.mn", "s", "t", ""), ErrQueueFull)
	close(b.release)
	require.NoError(t, q.Close(context.Background()))
}

func TestTemplates(t *testing.T) {
	tp := NewTemplates("https://app.callgo.mn/")
	v, err := tp.Verification("Бат", "tok+/=", 24*time.Hour)
	require.NoError(t, err)
	link := "https://app.callgo.mn/verify-email?token=tok%2B%2F%3D"
	assert.Contains(t, v.Text, link)
	assert.Contains(t, v.Text, "Сайн байна уу, Бат!")
	assert.Contains(t, v.Text, "Hello Бат,")
	assert.Contains(t, v.Text, "1 day")
	assert.Contains(t, v.HTML, `href="https://app.callgo.mn/verify-email?token=tok%2B%2F%3D"`)
	assert.Contains(t, v.Subject, "Verify your email")
	assert.NotContains(t, v.Subject, "\n")

	inv, err := tp.Invitation("Номин <Трейд>", "Сараа", "admin", "abc", 7*24*time.Hour)
	require.NoError(t, err)
	assert.Contains(t, inv.Text, "https://app.callgo.mn/accept-invitation?token=abc")
	assert.Contains(t, inv.Text, "админ")
	assert.Contains(t, inv.Text, "as an admin")
	assert.Contains(t, inv.Text, "7 хоног")
	assert.Contains(t, inv.HTML, "Номин &lt;Трейд&gt;", "HTML must be escaped")

	rs, err := tp.PasswordReset("", "xyz", time.Hour)
	require.NoError(t, err)
	assert.Contains(t, rs.Text, "https://app.callgo.mn/reset-password?token=xyz")
	assert.Contains(t, rs.Text, "1 hour")
	assert.Contains(t, rs.Text, "1 цаг")
	assert.Contains(t, rs.Subject, "Reset your password")
	assert.Equal(t, "30 minutes", durationEN(30*time.Minute))
}
