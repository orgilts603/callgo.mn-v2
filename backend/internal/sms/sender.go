// Package sms sends text messages through a per-organisation gateway (a mock
// for development or a generic HTTP gateway) and records them for billing.
package sms

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Provider names accepted in the org SMS settings.
const (
	ProviderMock = "mock"
	ProviderHTTP = "http"
)

// SentMessage is one message recorded by Mock.
type SentMessage struct {
	To, Body string
}

// Mock records sends instead of delivering them.
type Mock struct {
	mu   sync.Mutex
	sent []SentMessage
	// Err, when set, is returned by Send (the message is not recorded).
	Err error
}

var _ domain.SMSSender = (*Mock)(nil)

// NewMock returns an empty recording sender.
func NewMock() *Mock { return &Mock{} }

// Name implements domain.SMSSender.
func (m *Mock) Name() string { return ProviderMock }

// Send implements domain.SMSSender.
func (m *Mock) Send(_ context.Context, to, body string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return "", m.Err
	}
	m.sent = append(m.sent, SentMessage{To: to, Body: body})
	return "mock-" + strconv.Itoa(len(m.sent)), nil
}

// Sent returns a copy of the recorded messages.
func (m *Mock) Sent() []SentMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]SentMessage(nil), m.sent...)
}

// HTTPConfig configures the generic HTTP gateway.
//
// URL and BodyTemplate are Go text/templates rendered with To, Body, From and
// APIKey (all strings). Helpers: json (JSON string literal, quotes included)
// and the built-in urlquery. Examples:
//
//	BodyTemplate: {"to":{{json .To}},"from":{{json .From}},"text":{{json .Body}}}
//	BodyTemplate: to={{urlquery .To}}&from={{urlquery .From}}&text={{urlquery .Body}}
//	Method GET, URL: https://gw.example/send?key={{urlquery .APIKey}}&to={{urlquery .To}}&text={{urlquery .Body}}
type HTTPConfig struct {
	URL    string
	APIKey string
	From   string
	// Method defaults to POST. GET/DELETE send no body.
	Method string
	// BodyTemplate defaults to DefaultBodyTemplate.
	BodyTemplate string
	// AuthHeader names the header that carries APIKey. "Authorization"
	// (default) sends "Bearer <key>"; any other name sends the raw key.
	// "-" disables the header (the key may then be used in the templates).
	AuthHeader string
	// ContentType overrides detection (JSON when the body starts with { or [,
	// form-encoded otherwise).
	ContentType string
	// Client defaults to a client with a 15s timeout. Redirects are not followed.
	Client *http.Client
}

// DefaultBodyTemplate is the JSON body used when none is configured.
const DefaultBodyTemplate = `{"to":{{json .To}},"from":{{json .From}},"text":{{json .Body}}}`

// HTTP is a generic SMS gateway client.
type HTTP struct {
	cfg     HTTPConfig
	urlTpl  *template.Template
	bodyTpl *template.Template
	client  *http.Client
}

var _ domain.SMSSender = (*HTTP)(nil)

type tplData struct{ To, Body, From, APIKey string }

var tplFuncs = template.FuncMap{
	"json": func(s string) (string, error) {
		b, err := json.Marshal(s)
		return string(b), err
	},
}

// NewHTTP validates cfg and builds the sender.
func NewHTTP(cfg HTTPConfig) (*HTTP, error) {
	if err := ValidateGatewayURL(cfg.URL); err != nil {
		return nil, err
	}
	cfg.Method = strings.ToUpper(strings.TrimSpace(cfg.Method))
	if cfg.Method == "" {
		cfg.Method = http.MethodPost
	}
	switch cfg.Method {
	case http.MethodPost, http.MethodPut, http.MethodGet, http.MethodDelete:
	default:
		return nil, fmt.Errorf("%w: unsupported sms method %q", domain.ErrInvalid, cfg.Method)
	}
	if strings.TrimSpace(cfg.BodyTemplate) == "" {
		cfg.BodyTemplate = DefaultBodyTemplate
	}
	if cfg.AuthHeader == "" {
		cfg.AuthHeader = "Authorization"
	}
	urlTpl, err := template.New("url").Funcs(tplFuncs).Option("missingkey=error").Parse(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("%w: sms url template: %v", domain.ErrInvalid, err)
	}
	bodyTpl, err := template.New("body").Funcs(tplFuncs).Option("missingkey=error").Parse(cfg.BodyTemplate)
	if err != nil {
		return nil, fmt.Errorf("%w: sms body template: %v", domain.ErrInvalid, err)
	}
	client := http.Client{Timeout: 15 * time.Second}
	if cfg.Client != nil {
		client = *cfg.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTP{cfg: cfg, urlTpl: urlTpl, bodyTpl: bodyTpl, client: &client}, nil
}

// ValidateGatewayURL requires https (http is allowed for localhost only).
// The URL may contain template actions, which are checked after rendering
// sample values.
func ValidateGatewayURL(raw string) error {
	sample := strings.TrimSpace(raw)
	if sample == "" {
		return fmt.Errorf("%w: sms gateway url is required", domain.ErrInvalid)
	}
	if strings.Contains(sample, "{{") {
		t, err := template.New("u").Funcs(tplFuncs).Parse(sample)
		if err != nil {
			return fmt.Errorf("%w: sms url template: %v", domain.ErrInvalid, err)
		}
		var b bytes.Buffer
		if err := t.Execute(&b, tplData{To: "+97699112233", Body: "x", From: "x", APIKey: "x"}); err != nil {
			return fmt.Errorf("%w: sms url template: %v", domain.ErrInvalid, err)
		}
		sample = b.String()
	}
	u, err := url.Parse(sample)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%w: sms gateway url is not a valid URL", domain.ErrInvalid)
	}
	switch u.Scheme {
	case "https":
	case "http":
		h := u.Hostname()
		if h != "localhost" && h != "127.0.0.1" && h != "::1" {
			return fmt.Errorf("%w: sms gateway url must use https", domain.ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: sms gateway url must use https", domain.ErrInvalid)
	}
	return nil
}

// ValidateBodyTemplate parses and dry-runs a body template.
func ValidateBodyTemplate(tpl string) error {
	if strings.TrimSpace(tpl) == "" {
		return nil
	}
	t, err := template.New("body").Funcs(tplFuncs).Option("missingkey=error").Parse(tpl)
	if err != nil {
		return fmt.Errorf("%w: sms body template: %v", domain.ErrInvalid, err)
	}
	if err := t.Execute(io.Discard, tplData{To: "+97699112233", Body: "x", From: "x", APIKey: "x"}); err != nil {
		return fmt.Errorf("%w: sms body template: %v", domain.ErrInvalid, err)
	}
	return nil
}

// Name implements domain.SMSSender.
func (h *HTTP) Name() string { return ProviderHTTP }

const maxGatewayBody = 4096

// Send implements domain.SMSSender. Any 2xx answer is success; the provider
// reference is taken from a JSON response field id/message_id/messageId/
// msg_id/request_id when present.
func (h *HTTP) Send(ctx context.Context, to, body string) (string, error) {
	data := tplData{To: to, Body: body, From: h.cfg.From, APIKey: h.cfg.APIKey}
	var u bytes.Buffer
	if err := h.urlTpl.Execute(&u, data); err != nil {
		return "", fmt.Errorf("render sms url: %w", err)
	}
	var payload []byte
	if h.cfg.Method == http.MethodPost || h.cfg.Method == http.MethodPut {
		var b bytes.Buffer
		if err := h.bodyTpl.Execute(&b, data); err != nil {
			return "", fmt.Errorf("render sms body: %w", err)
		}
		payload = b.Bytes()
	}
	req, err := http.NewRequestWithContext(ctx, h.cfg.Method, u.String(), bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("build sms request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", h.contentType(payload))
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "CallGo-SMS/1.0")
	if h.cfg.APIKey != "" && h.cfg.AuthHeader != "-" {
		if strings.EqualFold(h.cfg.AuthHeader, "Authorization") {
			req.Header.Set("Authorization", "Bearer "+h.cfg.APIKey)
		} else {
			req.Header.Set(h.cfg.AuthHeader, h.cfg.APIKey)
		}
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("sms gateway request: %w", redactKey(err, h.cfg.APIKey))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxGatewayBody))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return "", fmt.Errorf("sms gateway answered http %d: %s", resp.StatusCode, msg)
	}
	return providerRef(raw), nil
}

func (h *HTTP) contentType(body []byte) string {
	if h.cfg.ContentType != "" {
		return h.cfg.ContentType
	}
	if t := bytes.TrimSpace(body); len(t) > 0 && (t[0] == '{' || t[0] == '[') {
		return "application/json"
	}
	return "application/x-www-form-urlencoded"
}

// redactKey removes the API key from transport errors, which embed the URL.
func redactKey(err error, key string) error {
	if key == "" {
		return err
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s %s: %s", ue.Op, strings.ReplaceAll(ue.URL, key, "***"), strings.ReplaceAll(ue.Err.Error(), key, "***"))
	}
	return errors.New(strings.ReplaceAll(err.Error(), key, "***"))
}

func providerRef(raw []byte) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, k := range []string{"id", "message_id", "messageId", "msg_id", "request_id"} {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		}
	}
	return ""
}
