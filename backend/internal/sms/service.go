package sms

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/phone"
)

// Sentinel errors of the service.
var (
	// ErrFeatureUnavailable: the org's plan lacks the `sms` feature.
	ErrFeatureUnavailable = errors.New("sms feature is not available on this plan")
	// ErrNotConfigured: the org has no SMS gateway configured.
	ErrNotConfigured = errors.New("sms gateway is not configured")
	// ErrSendFailed wraps a gateway failure; the (failed) message is still
	// returned and stored.
	ErrSendFailed = errors.New("sms send failed")
)

// FeatureSMS is the plan feature that gates SMS.
const FeatureSMS = "sms"

// MaxBodyRunes bounds a message body.
const MaxBodyRunes = 1000

// SettingsKey is the key inside org.settings that holds the SMS config.
const SettingsKey = "sms"

// SenderFactory builds the org's sender from its settings map (the whole
// org.settings, the factory reads settings["sms"]). It returns
// ErrNotConfigured when no gateway is set up.
type SenderFactory func(orgSettings map[string]any) (domain.SMSSender, error)

// Meter records billable SMS usage (UsageSMS).
type Meter interface {
	RecordSMS(ctx context.Context, orgID uuid.UUID, callID *uuid.UUID) error
}

// SettingsLoader reads org.settings (the integrator adapts OrgRepository.GetOrg).
type SettingsLoader interface {
	OrgSettings(ctx context.Context, orgID uuid.UUID) (map[string]any, error)
}

// SettingsLoaderFunc adapts a function to SettingsLoader.
type SettingsLoaderFunc func(ctx context.Context, orgID uuid.UUID) (map[string]any, error)

// OrgSettings implements SettingsLoader.
func (f SettingsLoaderFunc) OrgSettings(ctx context.Context, orgID uuid.UUID) (map[string]any, error) {
	return f(ctx, orgID)
}

// Crypto encrypts secrets stored in org.settings (AES-GCM in production).
type Crypto struct {
	Encrypt func([]byte) ([]byte, error)
	Decrypt func([]byte) ([]byte, error)
}

// SMSConfig is the org's SMS setup as exposed to the API (no secrets).
type SMSConfig struct {
	Provider     string `json:"provider"` // "" | "mock" | "http"
	URL          string `json:"url,omitempty"`
	From         string `json:"from"`
	Method       string `json:"method,omitempty"`
	BodyTemplate string `json:"bodyTemplate,omitempty"`
	AuthHeader   string `json:"authHeader,omitempty"`
	HasAPIKey    bool   `json:"hasApiKey"`
	Configured   bool   `json:"configured"`
}

// ConfigInput is a PUT /api/sms/config request. A nil APIKey keeps the stored
// key; an empty one clears it.
type ConfigInput struct {
	Provider     string
	URL          string
	APIKey       *string
	From         string
	Method       string
	BodyTemplate string
	AuthHeader   string
}

// Service sends and records SMS for organisations.
type Service struct {
	repo     domain.IntegrationsRepository
	settings SettingsLoader
	factory  SenderFactory
	meter    Meter
	ent      domain.Entitlements
	crypto   Crypto
	log      zerolog.Logger
	now      func() time.Time
}

// Option customises a Service.
type Option func(*Service)

// WithCrypto sets the secret cipher used by SetConfig (required to store an
// API key). Use the same functions for DefaultFactory.
func WithCrypto(c Crypto) Option { return func(s *Service) { s.crypto = c } }

// WithClock overrides time.Now (tests).
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// New builds a Service. meter and ent may be nil (no metering / every feature
// allowed).
func New(repo domain.IntegrationsRepository, settings SettingsLoader, factory SenderFactory, meter Meter, ent domain.Entitlements, log zerolog.Logger, opts ...Option) *Service {
	s := &Service{
		repo: repo, settings: settings, factory: factory, meter: meter, ent: ent,
		log: log.With().Str("component", "sms").Logger(), now: time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Send validates the request, sends the text through the org's gateway and
// records it. On a gateway failure it returns the stored failed message and an
// error wrapping ErrSendFailed.
func (s *Service) Send(ctx context.Context, orgID uuid.UUID, callID *uuid.UUID, to, body string) (*domain.SMSMessage, error) {
	if s.ent != nil {
		ok, err := s.ent.HasFeature(ctx, orgID, FeatureSMS)
		if err != nil {
			return nil, fmt.Errorf("check sms feature: %w", err)
		}
		if !ok {
			return nil, ErrFeatureUnavailable
		}
	}
	e164, err := phone.Normalize(to)
	if err != nil {
		return nil, fmt.Errorf("%w: to: %v", domain.ErrInvalid, err)
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, fmt.Errorf("%w: body is empty", domain.ErrInvalid)
	}
	if utf8.RuneCountInString(body) > MaxBodyRunes {
		return nil, fmt.Errorf("%w: body exceeds %d characters", domain.ErrInvalid, MaxBodyRunes)
	}
	settings, err := s.settings.OrgSettings(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("load org settings: %w", err)
	}
	sender, err := s.factory(settings)
	if err != nil {
		return nil, err
	}

	now := s.now().UTC()
	msg := &domain.SMSMessage{
		ID: uuid.New(), OrgID: orgID, CallID: callID, To: e164, Body: body,
		Provider: sender.Name(), Status: "queued", CreatedAt: now,
	}
	if err := s.repo.CreateSMS(ctx, msg); err != nil {
		return nil, fmt.Errorf("store sms: %w", err)
	}
	ref, sendErr := sender.Send(ctx, e164, body)
	if sendErr != nil {
		msg.Status = "failed"
		msg.Error = truncate(sendErr.Error(), 500)
	} else {
		sent := s.now().UTC()
		msg.Status, msg.ProviderRef, msg.SentAt = "sent", ref, &sent
	}
	if err := s.repo.UpdateSMS(ctx, msg); err != nil {
		s.log.Error().Err(err).Str("sms", msg.ID.String()).Msg("update sms status")
	}
	if sendErr != nil {
		return msg, fmt.Errorf("%w: %v", ErrSendFailed, sendErr)
	}
	if s.meter != nil {
		if err := s.meter.RecordSMS(ctx, orgID, callID); err != nil {
			s.log.Error().Err(err).Str("org", orgID.String()).Msg("record sms usage")
		}
	}
	return msg, nil
}

// Config extracts the SMS configuration from org.settings.
func (s *Service) Config(orgSettings map[string]any) SMSConfig {
	return ConfigFromSettings(orgSettings)
}

// ConfigFromSettings extracts the SMS configuration from org.settings.
func ConfigFromSettings(orgSettings map[string]any) SMSConfig {
	raw, _ := orgSettings[SettingsKey].(map[string]any)
	c := SMSConfig{
		Provider:     str(raw, "provider"),
		URL:          str(raw, "url"),
		From:         str(raw, "from"),
		Method:       str(raw, "method"),
		BodyTemplate: str(raw, "bodyTemplate"),
		AuthHeader:   str(raw, "authHeader"),
		HasAPIKey:    str(raw, "apiKey") != "",
	}
	switch c.Provider {
	case ProviderMock:
		c.Configured = true
	case ProviderHTTP:
		c.Configured = c.URL != ""
	}
	return c
}

// SetConfig validates in and returns the settings fragment to merge into
// org.settings: {"sms": {...}}. The API key is encrypted with the configured
// Crypto; when in.APIKey is nil the key from current (org.settings) is kept.
func (s *Service) SetConfig(current map[string]any, in ConfigInput) (map[string]any, error) {
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	in.From = strings.TrimSpace(in.From)
	if err := validateFrom(in.From); err != nil {
		return nil, err
	}
	out := map[string]any{"provider": in.Provider, "from": in.From}
	switch in.Provider {
	case ProviderMock:
	case ProviderHTTP:
		if err := ValidateGatewayURL(in.URL); err != nil {
			return nil, err
		}
		if err := ValidateBodyTemplate(in.BodyTemplate); err != nil {
			return nil, err
		}
		method := strings.ToUpper(strings.TrimSpace(in.Method))
		switch method {
		case "", "POST", "PUT", "GET", "DELETE":
		default:
			return nil, fmt.Errorf("%w: method must be POST, PUT, GET or DELETE", domain.ErrInvalid)
		}
		authHeader := strings.TrimSpace(in.AuthHeader)
		if authHeader != "" && authHeader != "-" && !validHeaderName(authHeader) {
			return nil, fmt.Errorf("%w: invalid authHeader", domain.ErrInvalid)
		}
		out["url"] = strings.TrimSpace(in.URL)
		out["method"] = method
		out["bodyTemplate"] = in.BodyTemplate
		out["authHeader"] = authHeader
		switch {
		case in.APIKey == nil:
			if prev, _ := current[SettingsKey].(map[string]any); prev != nil {
				if k := str(prev, "apiKey"); k != "" {
					out["apiKey"] = k
				}
			}
		case *in.APIKey != "":
			if s.crypto.Encrypt == nil {
				return nil, errors.New("sms: encryption is not configured")
			}
			enc, err := s.crypto.Encrypt([]byte(*in.APIKey))
			if err != nil {
				return nil, fmt.Errorf("encrypt sms api key: %w", err)
			}
			out["apiKey"] = base64.StdEncoding.EncodeToString(enc)
		}
	default:
		return nil, fmt.Errorf("%w: provider must be \"mock\" or \"http\"", domain.ErrInvalid)
	}
	return map[string]any{SettingsKey: out}, nil
}

// DefaultFactory builds the mock or HTTP sender from org.settings["sms"],
// decrypting the API key with dec.
func DefaultFactory(dec func([]byte) ([]byte, error), opts ...FactoryOption) SenderFactory {
	var fo factoryOpts
	for _, o := range opts {
		o(&fo)
	}
	return func(orgSettings map[string]any) (domain.SMSSender, error) {
		c := ConfigFromSettings(orgSettings)
		raw, _ := orgSettings[SettingsKey].(map[string]any)
		switch c.Provider {
		case ProviderMock:
			if fo.mock != nil {
				return fo.mock, nil
			}
			return NewMock(), nil
		case ProviderHTTP:
			var key string
			if enc := str(raw, "apiKey"); enc != "" {
				if dec == nil {
					return nil, errors.New("sms: decryption is not configured")
				}
				b, err := base64.StdEncoding.DecodeString(enc)
				if err != nil {
					return nil, fmt.Errorf("decode sms api key: %w", err)
				}
				plain, err := dec(b)
				if err != nil {
					return nil, fmt.Errorf("decrypt sms api key: %w", err)
				}
				key = string(plain)
			}
			return NewHTTP(HTTPConfig{
				URL: c.URL, APIKey: key, From: c.From, Method: c.Method,
				BodyTemplate: c.BodyTemplate, AuthHeader: c.AuthHeader, Client: fo.client,
			})
		default:
			return nil, ErrNotConfigured
		}
	}
}

type factoryOpts struct {
	client *http.Client
	mock   *Mock
}

// FactoryOption customises DefaultFactory.
type FactoryOption func(*factoryOpts)

// WithHTTPClient sets the HTTP client of the gateway sender.
func WithHTTPClient(c *http.Client) FactoryOption { return func(o *factoryOpts) { o.client = c } }

// WithMock makes provider "mock" resolve to m (tests, dev inspection).
func WithMock(m *Mock) FactoryOption { return func(o *factoryOpts) { o.mock = m } }

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// validateFrom accepts an E.164 number, a short code or an alphanumeric
// sender id (max 32 printable characters).
func validateFrom(from string) error {
	if utf8.RuneCountInString(from) > 32 {
		return fmt.Errorf("%w: from is longer than 32 characters", domain.ErrInvalid)
	}
	for _, r := range from {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: from contains control characters", domain.ErrInvalid)
		}
	}
	return nil
}

func validHeaderName(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return s != ""
}
