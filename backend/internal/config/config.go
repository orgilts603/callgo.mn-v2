// Package config loads and validates the backend's runtime configuration from
// environment variables (optionally seeded from a .env file).
package config

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Environment names for CALLGO_ENV.
const (
	EnvDev  = "dev"
	EnvProd = "prod"
)

// Development defaults. They are rejected by Validate in prod.
const (
	DevJWTSecret       = "dev-secret-change-me"
	DevAgentToken      = "dev-agent-token"
	DevAdminPassword   = "admin1234"
	DevLiveKitSecret   = "secret"
	DefaultDatabaseURL = "postgres://callgo:callgo@localhost:5432/callgo?sslmode=disable"

	// minProdSecretLen is the minimum length of the JWT secret in prod.
	minProdSecretLen = 32
	// encryptionKeyLen is the AES-256 key length in bytes.
	encryptionKeyLen = 32
)

// KeyPair is a LiveKit API key and secret.
type KeyPair struct {
	APIKey    string
	APISecret string
}

// LiveKit configures the LiveKit server connection and agent dispatch.
type LiveKit struct {
	// URL is the LiveKit server URL (ws://, wss://, http:// or https://).
	URL       string
	APIKey    string
	APISecret string
	// AgentName is the agent name used for explicit dispatch.
	AgentName string
	// WebhookKeyPair verifies incoming LiveKit webhooks. It defaults to the
	// API key pair.
	WebhookKeyPair KeyPair
}

// SIP configures the SIP trunks created in LiveKit towards Asterisk.
type SIP struct {
	AsteriskHost     string
	AsteriskPort     int
	AllowedAddresses []string
	// Transport is udp, tcp or tls.
	Transport    string
	AuthUsername string
	AuthPassword string
	// InboundAuthUsername / InboundAuthPassword optionally require digest
	// auth on the LiveKit inbound trunk (Asterisk must send credentials).
	InboundAuthUsername string
	InboundAuthPassword string
	RingTimeout         time.Duration
	// MaxCallDuration is the hard cap on a single call.
	MaxCallDuration time.Duration
}

// Campaign configures the outbound dialer engine.
type Campaign struct {
	PollInterval   time.Duration
	RetryBackoff   time.Duration
	MaxConcurrency int
}

// Config is the complete backend configuration.
type Config struct {
	DatabaseURL string
	HTTPAddr    string

	JWTSecret string
	// AgentToken authenticates the Python agent worker (X-Agent-Token).
	AgentToken string
	// EncryptionKey is the 32-byte AES-256 key used to encrypt API keys at rest.
	EncryptionKey []byte
	// EncryptionKeyDerived is true when EncryptionKey was derived from
	// JWTSecret because CALLGO_ENCRYPTION_KEY was not set.
	EncryptionKeyDerived bool

	AdminEmail    string
	AdminPassword string
	AllowSignup   bool
	CORSOrigins   []string

	// Env is "dev" or "prod".
	Env       string
	LogLevel  string
	LogPretty bool

	MockTelephony     bool
	Simulator         bool
	SimulatorInterval time.Duration
	// KnowledgeFakeEmbeddings uses a deterministic offline embedder for
	// knowledge bases whose LLM config has no embeddings API (dev only).
	KnowledgeFakeEmbeddings bool

	LiveKit  LiveKit
	SIP      SIP
	Campaign Campaign

	// AgentWorkerURL is the base URL of the Python agent worker's HTTP API,
	// used by the LLM test proxy.
	AgentWorkerURL  string
	ShutdownTimeout time.Duration

	// Warnings are non-fatal configuration problems found by Load (dev
	// defaults in use, mock telephony enabled in prod, ...). The caller should
	// log them once a logger exists; see LogWarnings.
	Warnings []string
}

// IsProd reports whether the backend runs in production mode.
func (c Config) IsProd() bool { return c.Env == EnvProd }

// Load reads configuration from the environment. If a .env file exists in the
// working directory it is loaded first, but real environment variables always
// take precedence. Load returns the assembled Config together with an error
// joining every malformed or (in prod) invalid value.
func Load() (Config, error) {
	// A missing .env file is normal; godotenv never overrides existing vars.
	_ = godotenv.Load()
	return load(os.LookupEnv)
}

// loader accumulates parse errors while reading values.
type loader struct {
	lookup func(string) (string, bool)
	errs   []error
}

func (l *loader) str(key, def string) string {
	if v, ok := l.lookup(key); ok {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return def
}

func (l *loader) raw(key string) (string, bool) {
	v, ok := l.lookup(key)
	v = strings.TrimSpace(v)
	return v, ok && v != ""
}

func (l *loader) boolean(key string, def bool) bool {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: invalid boolean %q", key, v))
		return def
	}
	return b
}

func (l *loader) integer(key string, def int) int {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: invalid integer %q", key, v))
		return def
	}
	return n
}

func (l *loader) duration(key string, def time.Duration) time.Duration {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: invalid duration %q", key, v))
		return def
	}
	return d
}

func (l *loader) list(key string, def []string) []string {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	return splitList(v)
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func load(lookup func(string) (string, bool)) (Config, error) {
	l := &loader{lookup: lookup}
	var warn []string

	env := strings.ToLower(l.str("CALLGO_ENV", EnvDev))
	switch env {
	case "production":
		env = EnvProd
	case "development", "local":
		env = EnvDev
	}
	if env != EnvDev && env != EnvProd {
		l.errs = append(l.errs, fmt.Errorf("CALLGO_ENV: must be %q or %q, got %q", EnvDev, EnvProd, env))
		env = EnvDev
	}
	prod := env == EnvProd

	cfg := Config{
		Env:         env,
		DatabaseURL: l.str("CALLGO_DATABASE_URL", DefaultDatabaseURL),
		HTTPAddr:    l.str("CALLGO_HTTP_ADDR", ":8080"),
		AgentToken:  l.str("CALLGO_AGENT_TOKEN", DevAgentToken),

		AdminEmail:    l.str("CALLGO_ADMIN_EMAIL", "admin@callgo.mn"),
		AdminPassword: l.str("CALLGO_ADMIN_PASSWORD", DevAdminPassword),
		AllowSignup:   l.boolean("CALLGO_ALLOW_SIGNUP", false),
		CORSOrigins:   l.list("CALLGO_CORS_ORIGINS", []string{"http://localhost:5173"}),

		LogLevel:  strings.ToLower(l.str("CALLGO_LOG_LEVEL", "info")),
		LogPretty: l.boolean("CALLGO_LOG_PRETTY", !prod),

		MockTelephony:           l.boolean("CALLGO_MOCK_TELEPHONY", true),
		Simulator:               l.boolean("CALLGO_SIMULATOR", false),
		SimulatorInterval:       l.duration("CALLGO_SIMULATOR_INTERVAL", 4*time.Second),
		KnowledgeFakeEmbeddings: l.boolean("CALLGO_EMBED_FAKE", false),

		AgentWorkerURL:  strings.TrimRight(l.str("CALLGO_AGENT_WORKER_URL", "http://localhost:8090"), "/"),
		ShutdownTimeout: l.duration("CALLGO_SHUTDOWN_TIMEOUT", 15*time.Second),
	}

	// JWT secret.
	if v, ok := l.raw("CALLGO_JWT_SECRET"); ok {
		cfg.JWTSecret = v
	} else {
		cfg.JWTSecret = DevJWTSecret
		if !prod {
			warn = append(warn, "CALLGO_JWT_SECRET not set: using insecure development default")
		}
	}

	// Encryption key.
	if v, ok := l.raw("CALLGO_ENCRYPTION_KEY"); ok {
		key, err := base64.StdEncoding.DecodeString(v)
		switch {
		case err != nil:
			l.errs = append(l.errs, errors.New("CALLGO_ENCRYPTION_KEY: not valid base64"))
		case len(key) != encryptionKeyLen:
			l.errs = append(l.errs, fmt.Errorf("CALLGO_ENCRYPTION_KEY: must decode to %d bytes, got %d", encryptionKeyLen, len(key)))
		default:
			cfg.EncryptionKey = key
		}
	}
	if cfg.EncryptionKey == nil {
		sum := sha256.Sum256([]byte(cfg.JWTSecret))
		cfg.EncryptionKey = sum[:]
		cfg.EncryptionKeyDerived = true
		if !prod {
			warn = append(warn, "CALLGO_ENCRYPTION_KEY not set: deriving encryption key from JWT secret")
		}
	}

	// LiveKit.
	cfg.LiveKit = LiveKit{
		URL:       l.str("LIVEKIT_URL", "ws://localhost:7880"),
		APIKey:    l.str("LIVEKIT_API_KEY", "devkey"),
		APISecret: l.str("LIVEKIT_API_SECRET", DevLiveKitSecret),
		AgentName: l.str("LIVEKIT_AGENT_NAME", "callgo"),
	}
	cfg.LiveKit.WebhookKeyPair = KeyPair{
		APIKey:    l.str("LIVEKIT_WEBHOOK_API_KEY", cfg.LiveKit.APIKey),
		APISecret: l.str("LIVEKIT_WEBHOOK_API_SECRET", cfg.LiveKit.APISecret),
	}

	// SIP.
	cfg.SIP = SIP{
		AsteriskHost:        l.str("SIP_ASTERISK_HOST", "localhost"),
		AsteriskPort:        l.integer("SIP_ASTERISK_PORT", 5060),
		AllowedAddresses:    l.list("SIP_ALLOWED_ADDRESSES", nil),
		Transport:           strings.ToLower(l.str("SIP_TRANSPORT", "udp")),
		AuthUsername:        l.str("SIP_AUTH_USERNAME", ""),
		AuthPassword:        l.str("SIP_AUTH_PASSWORD", ""),
		InboundAuthUsername: l.str("SIP_INBOUND_AUTH_USERNAME", ""),
		InboundAuthPassword: l.str("SIP_INBOUND_AUTH_PASSWORD", ""),
		RingTimeout:         l.duration("SIP_RING_TIMEOUT", 30*time.Second),
		MaxCallDuration:     l.duration("SIP_MAX_CALL_DURATION", 20*time.Minute),
	}

	// Campaign engine.
	cfg.Campaign = Campaign{
		PollInterval:   l.duration("CALLGO_CAMPAIGN_POLL_INTERVAL", 2*time.Second),
		RetryBackoff:   l.duration("CALLGO_CAMPAIGN_RETRY_BACKOFF", 5*time.Minute),
		MaxConcurrency: l.integer("CALLGO_CAMPAIGN_MAX_CONCURRENCY", 20),
	}

	if prod {
		if cfg.MockTelephony {
			warn = append(warn, "CALLGO_MOCK_TELEPHONY is enabled in prod: no real calls will be placed")
		}
		if cfg.Simulator {
			warn = append(warn, "CALLGO_SIMULATOR is enabled in prod: fake calls will be generated")
		}
	}
	cfg.Warnings = warn

	err := errors.Join(l.errs...)
	if verr := cfg.Validate(); verr != nil {
		err = errors.Join(err, verr)
	}
	return cfg, err
}

// Validate checks the configuration for values that are unacceptable. Basic
// sanity checks apply in every environment; insecure development defaults are
// additionally rejected when Env is "prod". All problems are joined.
func (c Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if c.Env != EnvDev && c.Env != EnvProd {
		add("CALLGO_ENV: must be %q or %q, got %q", EnvDev, EnvProd, c.Env)
	}
	if c.DatabaseURL == "" {
		add("CALLGO_DATABASE_URL: must not be empty")
	}
	if c.HTTPAddr == "" {
		add("CALLGO_HTTP_ADDR: must not be empty")
	}
	if len(c.EncryptionKey) != encryptionKeyLen {
		add("encryption key must be %d bytes, got %d", encryptionKeyLen, len(c.EncryptionKey))
	}
	if _, err := parseLogLevel(c.LogLevel); err != nil {
		add("CALLGO_LOG_LEVEL: %v", err)
	}
	switch c.SIP.Transport {
	case "udp", "tcp", "tls", "auto":
	default:
		add("SIP_TRANSPORT: must be udp, tcp or tls, got %q", c.SIP.Transport)
	}
	if c.SIP.AsteriskPort < 1 || c.SIP.AsteriskPort > 65535 {
		add("SIP_ASTERISK_PORT: out of range: %d", c.SIP.AsteriskPort)
	}
	if c.SIP.RingTimeout <= 0 {
		add("SIP_RING_TIMEOUT: must be positive")
	}
	if c.SIP.MaxCallDuration <= 0 {
		add("SIP_MAX_CALL_DURATION: must be positive")
	}
	if c.Campaign.PollInterval <= 0 {
		add("CALLGO_CAMPAIGN_POLL_INTERVAL: must be positive")
	}
	if c.Campaign.RetryBackoff < 0 {
		add("CALLGO_CAMPAIGN_RETRY_BACKOFF: must not be negative")
	}
	if c.Campaign.MaxConcurrency < 1 {
		add("CALLGO_CAMPAIGN_MAX_CONCURRENCY: must be at least 1")
	}
	if c.ShutdownTimeout <= 0 {
		add("CALLGO_SHUTDOWN_TIMEOUT: must be positive")
	}
	if c.Simulator && c.SimulatorInterval <= 0 {
		add("CALLGO_SIMULATOR_INTERVAL: must be positive")
	}
	if u, err := url.Parse(c.AgentWorkerURL); err != nil || u.Scheme == "" || u.Host == "" {
		add("CALLGO_AGENT_WORKER_URL: must be an absolute URL, got %q", c.AgentWorkerURL)
	}

	if c.Env == EnvProd {
		if c.JWTSecret == DevJWTSecret || len(c.JWTSecret) < minProdSecretLen {
			add("CALLGO_JWT_SECRET: must be set to a random value of at least %d characters in prod", minProdSecretLen)
		}
		if c.EncryptionKeyDerived {
			add("CALLGO_ENCRYPTION_KEY: must be set in prod (generate with `openssl rand -base64 32`)")
		}
		if c.AgentToken == DevAgentToken || len(c.AgentToken) < 16 {
			add("CALLGO_AGENT_TOKEN: must be set to a random value of at least 16 characters in prod")
		}
		if c.AdminPassword == DevAdminPassword {
			add("CALLGO_ADMIN_PASSWORD: must not use the development default in prod")
		}
		if c.LiveKit.APISecret == DevLiveKitSecret || c.LiveKit.APIKey == "devkey" {
			add("LIVEKIT_API_KEY/LIVEKIT_API_SECRET: must not use the development defaults in prod")
		}
		for _, o := range c.CORSOrigins {
			if o == "*" {
				add("CALLGO_CORS_ORIGINS: wildcard origin is not allowed in prod")
				break
			}
		}
	}
	return errors.Join(errs...)
}
