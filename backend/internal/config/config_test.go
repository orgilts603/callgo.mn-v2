package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lookupFrom(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(lookupFrom(nil))
	require.NoError(t, err)

	assert.Equal(t, DefaultDatabaseURL, cfg.DatabaseURL)
	assert.Equal(t, ":8080", cfg.HTTPAddr)
	assert.Equal(t, DevJWTSecret, cfg.JWTSecret)
	assert.Equal(t, DevAgentToken, cfg.AgentToken)
	assert.Equal(t, "admin@callgo.mn", cfg.AdminEmail)
	assert.Equal(t, "admin1234", cfg.AdminPassword)
	assert.False(t, cfg.AllowSignup)
	assert.Equal(t, []string{"http://localhost:5173"}, cfg.CORSOrigins)
	assert.Equal(t, EnvDev, cfg.Env)
	assert.True(t, cfg.LogPretty)
	assert.True(t, cfg.MockTelephony)
	assert.False(t, cfg.Simulator)
	assert.Equal(t, "ws://localhost:7880", cfg.LiveKit.URL)
	assert.Equal(t, "devkey", cfg.LiveKit.APIKey)
	assert.Equal(t, "callgo", cfg.LiveKit.AgentName)
	assert.Equal(t, cfg.LiveKit.APIKey, cfg.LiveKit.WebhookKeyPair.APIKey)
	assert.Equal(t, cfg.LiveKit.APISecret, cfg.LiveKit.WebhookKeyPair.APISecret)
	assert.Equal(t, 5060, cfg.SIP.AsteriskPort)
	assert.Equal(t, "udp", cfg.SIP.Transport)
	assert.Equal(t, 30*time.Second, cfg.SIP.RingTimeout)
	assert.Equal(t, 20*time.Minute, cfg.SIP.MaxCallDuration)
	assert.Equal(t, "http://localhost:8090", cfg.AgentWorkerURL)

	sum := sha256.Sum256([]byte(DevJWTSecret))
	assert.Equal(t, sum[:], cfg.EncryptionKey)
	assert.True(t, cfg.EncryptionKeyDerived)
	assert.Len(t, cfg.Warnings, 2, "jwt secret + derived key warnings")
}

func TestLoadOverrides(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	cfg, err := load(lookupFrom(map[string]string{
		"CALLGO_HTTP_ADDR":        ":9999",
		"CALLGO_ENCRYPTION_KEY":   base64.StdEncoding.EncodeToString(key),
		"CALLGO_ALLOW_SIGNUP":     "true",
		"CALLGO_CORS_ORIGINS":     "https://a.mn, https://b.mn ,",
		"SIP_ALLOWED_ADDRESSES":   "10.0.0.1,10.0.0.2/24",
		"SIP_RING_TIMEOUT":        "45s",
		"SIP_ASTERISK_PORT":       "5070",
		"CALLGO_SIMULATOR":        "1",
		"LIVEKIT_WEBHOOK_API_KEY": "hook",
		"LIVEKIT_API_KEY":         "k",
		"CALLGO_AGENT_WORKER_URL": "http://worker:8090/",
	}))
	require.NoError(t, err)
	assert.Equal(t, ":9999", cfg.HTTPAddr)
	assert.Equal(t, key, cfg.EncryptionKey)
	assert.False(t, cfg.EncryptionKeyDerived)
	assert.True(t, cfg.AllowSignup)
	assert.Equal(t, []string{"https://a.mn", "https://b.mn"}, cfg.CORSOrigins)
	assert.Equal(t, []string{"10.0.0.1", "10.0.0.2/24"}, cfg.SIP.AllowedAddresses)
	assert.Equal(t, 45*time.Second, cfg.SIP.RingTimeout)
	assert.Equal(t, 5070, cfg.SIP.AsteriskPort)
	assert.True(t, cfg.Simulator)
	assert.Equal(t, "hook", cfg.LiveKit.WebhookKeyPair.APIKey)
	assert.Equal(t, "k", cfg.LiveKit.APIKey)
	assert.Equal(t, "http://worker:8090", cfg.AgentWorkerURL)
}

func TestLoadMalformedValues(t *testing.T) {
	_, err := load(lookupFrom(map[string]string{
		"CALLGO_ALLOW_SIGNUP":   "maybe",
		"SIP_RING_TIMEOUT":      "soon",
		"SIP_ASTERISK_PORT":     "abc",
		"CALLGO_ENCRYPTION_KEY": "!!!",
	}))
	require.Error(t, err)
	for _, want := range []string{"CALLGO_ALLOW_SIGNUP", "SIP_RING_TIMEOUT", "SIP_ASTERISK_PORT", "CALLGO_ENCRYPTION_KEY"} {
		assert.Contains(t, err.Error(), want)
	}
}

func TestEncryptionKeyWrongLength(t *testing.T) {
	_, err := load(lookupFrom(map[string]string{
		"CALLGO_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString([]byte("short")),
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must decode to 32 bytes")
}

func TestProdRejectsDevDefaults(t *testing.T) {
	cfg, err := load(lookupFrom(map[string]string{"CALLGO_ENV": "prod"}))
	require.Error(t, err)
	for _, want := range []string{
		"CALLGO_JWT_SECRET", "CALLGO_ENCRYPTION_KEY", "CALLGO_AGENT_TOKEN",
		"CALLGO_ADMIN_PASSWORD", "LIVEKIT_API_KEY",
	} {
		assert.Contains(t, err.Error(), want)
	}
	assert.True(t, cfg.IsProd())
	assert.False(t, cfg.LogPretty, "prod logs JSON by default")
	assert.NotEmpty(t, cfg.Warnings, "mock telephony warning")
}

func TestProdValid(t *testing.T) {
	cfg, err := load(lookupFrom(map[string]string{
		"CALLGO_ENV":              "prod",
		"CALLGO_JWT_SECRET":       strings.Repeat("s", 40),
		"CALLGO_ENCRYPTION_KEY":   base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)),
		"CALLGO_AGENT_TOKEN":      strings.Repeat("t", 32),
		"CALLGO_ADMIN_PASSWORD":   "a-strong-password",
		"LIVEKIT_API_KEY":         "APIabc",
		"LIVEKIT_API_SECRET":      strings.Repeat("x", 40),
		"CALLGO_MOCK_TELEPHONY":   "false",
		"CALLGO_CORS_ORIGINS":     "https://app.callgo.mn",
		"CALLGO_AGENT_WORKER_URL": "http://agent:8090",
	}))
	require.NoError(t, err)
	assert.Empty(t, cfg.Warnings)
}

func TestProdRejectsWildcardCORS(t *testing.T) {
	cfg, _ := load(lookupFrom(nil))
	cfg.Env = EnvProd
	cfg.CORSOrigins = []string{"*"}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wildcard")
}

func TestValidateBadValues(t *testing.T) {
	cfg, err := load(lookupFrom(nil))
	require.NoError(t, err)
	cfg.SIP.Transport = "carrier-pigeon"
	cfg.LogLevel = "loud"
	cfg.Campaign.MaxConcurrency = 0
	err = cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SIP_TRANSPORT")
	assert.Contains(t, err.Error(), "CALLGO_LOG_LEVEL")
	assert.Contains(t, err.Error(), "MAX_CONCURRENCY")
}

func TestNewLogger(t *testing.T) {
	cfg, err := load(lookupFrom(map[string]string{"CALLGO_LOG_LEVEL": "warn", "CALLGO_LOG_PRETTY": "false"}))
	require.NoError(t, err)
	l := NewLogger(cfg)
	assert.False(t, l.Debug().Enabled())
	assert.True(t, l.Warn().Enabled())
}

// TestEnvExampleLoads guards backend/.env.example against drifting from the
// loader: it must parse and produce a valid dev configuration.
func TestEnvExampleLoads(t *testing.T) {
	env, err := godotenv.Read("../../.env.example")
	require.NoError(t, err)
	require.NotEmpty(t, env)

	cfg, err := load(lookupFrom(env))
	require.NoError(t, err)
	assert.Equal(t, EnvDev, cfg.Env)
	assert.Equal(t, "callgo", cfg.LiveKit.AgentName)
	assert.Equal(t, 20, cfg.Campaign.MaxConcurrency)
	assert.Empty(t, cfg.SIP.AllowedAddresses)
}
