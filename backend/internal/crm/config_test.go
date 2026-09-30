package crm

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func TestKeyCipher(t *testing.T) {
	for _, key := range [][]byte{testKey, []byte("short"), nil, []byte(strings.Repeat("k", 64))} {
		c := newKeyCipher(key)
		enc, err := c.encrypt("sk-secret-value")
		require.NoError(t, err)
		enc2, err := c.encrypt("sk-secret-value")
		require.NoError(t, err)
		require.NotEqual(t, enc, enc2, "random nonce per encryption")
		plain, err := c.decrypt(enc)
		require.NoError(t, err)
		require.Equal(t, "sk-secret-value", plain)
	}

	empty, err := newKeyCipher(testKey).encrypt("")
	require.NoError(t, err)
	require.Empty(t, empty)
	plain, err := newKeyCipher(testKey).decrypt("")
	require.NoError(t, err)
	require.Empty(t, plain)

	enc, err := newKeyCipher(testKey).encrypt("secret")
	require.NoError(t, err)
	_, err = newKeyCipher([]byte("another key")).decrypt(enc)
	require.Error(t, err)
	_, err = newKeyCipher(testKey).decrypt("!!!not base64")
	require.Error(t, err)
	_, err = newKeyCipher(testKey).decrypt(base64.StdEncoding.EncodeToString([]byte("tiny")))
	require.Error(t, err)
}

func TestAPIKeyHint(t *testing.T) {
	cases := map[string]string{
		"":                             "",
		"abc":                          "…",
		"abcdefghi":                    "…hi",
		"sk-proj-abcdefghijklmnopq9Zt": "sk-…q9Zt",
		"gsk_0123456789":               "gsk…6789",
		"ключ-на-кириллице-123":        "клю…-123",
	}
	for in, want := range cases {
		require.Equal(t, want, apiKeyHint(in), in)
	}
}

func TestLLMConfigs(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "acme")

	const secret = "sk-proj-abcdefghijklmnopq9Zt"
	a := &domain.LLMConfig{OrgID: org.ID, Name: "GPT", Provider: domain.ProviderOpenAI, Model: "gpt-4o-mini",
		APIKey: secret, Temperature: 0.4, MaxTokens: 512, IsDefault: true}
	require.NoError(t, s.CreateLLMConfig(ctx, a))
	require.NotEqual(t, uuid.Nil, a.ID)
	require.Equal(t, "sk-…q9Zt", a.APIKeyHint)

	// Stored encrypted: no plaintext, valid base64 of nonce+ciphertext.
	var enc string
	require.NoError(t, testPool.QueryRow(ctx, `SELECT api_key_enc FROM llm_configs WHERE id = $1`, a.ID).Scan(&enc))
	require.NotContains(t, enc, secret)
	raw, err := base64.StdEncoding.DecodeString(enc)
	require.NoError(t, err)
	require.Greater(t, len(raw), len(secret))

	got, err := s.GetLLMConfig(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, secret, got.APIKey)
	require.Equal(t, "sk-…q9Zt", got.APIKeyHint)
	require.InDelta(t, 0.4, got.Temperature, 1e-6)
	require.Equal(t, 512, got.MaxTokens)

	// A store with a different key cannot decrypt.
	_, err = New(testPool, []byte("wrong key")).GetLLMConfig(ctx, a.ID)
	require.Error(t, err)

	b := &domain.LLMConfig{OrgID: org.ID, Name: "Local", Provider: domain.ProviderOllama, Model: "llama3.1",
		BaseURL: "http://ollama:11434", FallbackID: &a.ID}
	require.NoError(t, s.CreateLLMConfig(ctx, b))
	require.Empty(t, b.APIKeyHint)

	list, err := s.ListLLMConfigs(ctx, org.ID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, a.ID, list[0].ID, "default first")
	for _, c := range list {
		require.Empty(t, c.APIKey, "list never returns decrypted keys")
	}
	require.Equal(t, "sk-…q9Zt", list[0].APIKeyHint)
	require.Equal(t, a.ID, *list[1].FallbackID)

	def, err := s.GetDefaultLLMConfig(ctx, org.ID)
	require.NoError(t, err)
	require.Equal(t, a.ID, def.ID)
	require.Equal(t, secret, def.APIKey)

	// Update without key keeps the stored key; making b default clears a.
	b.IsDefault = true
	b.Model = "llama3.2"
	b.APIKey = ""
	require.NoError(t, s.UpdateLLMConfig(ctx, b))
	def, err = s.GetDefaultLLMConfig(ctx, org.ID)
	require.NoError(t, err)
	require.Equal(t, b.ID, def.ID)
	require.Equal(t, "llama3.2", def.Model)
	gotA, err := s.GetLLMConfig(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, gotA.IsDefault)

	a.APIKey = ""
	a.Name = "GPT renamed"
	a.IsDefault = false
	require.NoError(t, s.UpdateLLMConfig(ctx, a))
	require.Equal(t, "sk-…q9Zt", a.APIKeyHint)
	gotA, err = s.GetLLMConfig(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, secret, gotA.APIKey, "empty APIKey on update keeps the stored key")
	require.Equal(t, "GPT renamed", gotA.Name)

	a.APIKey = "sk-new-key-0000000000ABCD"
	require.NoError(t, s.UpdateLLMConfig(ctx, a))
	require.Equal(t, "sk-…ABCD", a.APIKeyHint)
	gotA, err = s.GetLLMConfig(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "sk-new-key-0000000000ABCD", gotA.APIKey)

	// Deleting a config referenced as fallback / by a profile nulls the refs.
	p := &domain.AgentProfile{OrgID: org.ID, Name: "P", LLMConfigID: &a.ID}
	require.NoError(t, s.CreateAgentProfile(ctx, p))
	require.NoError(t, s.DeleteLLMConfig(ctx, a.ID))
	gotB, err := s.GetLLMConfig(ctx, b.ID)
	require.NoError(t, err)
	require.Nil(t, gotB.FallbackID)
	gotP, err := s.GetAgentProfile(ctx, p.ID)
	require.NoError(t, err)
	require.Nil(t, gotP.LLMConfigID)

	// Not found / fallback when no default.
	requireErrIs(t, s.DeleteLLMConfig(ctx, a.ID), domain.ErrNotFound)
	_, err = s.GetLLMConfig(ctx, a.ID)
	requireErrIs(t, err, domain.ErrNotFound)
	requireErrIs(t, s.UpdateLLMConfig(ctx, &domain.LLMConfig{ID: uuid.New(), Name: "x", Provider: "openai", Model: "m", IsDefault: true}), domain.ErrNotFound)
	requireErrIs(t, s.UpdateLLMConfig(ctx, &domain.LLMConfig{ID: uuid.New(), Name: "x", Provider: "openai", Model: "m"}), domain.ErrNotFound)

	empty := newOrg(t, ctx, s, "empty")
	_, err = s.GetDefaultLLMConfig(ctx, empty.ID)
	requireErrIs(t, err, domain.ErrNotFound)
	c := &domain.LLMConfig{OrgID: empty.ID, Name: "only", Provider: domain.ProviderGroq, Model: "llama"}
	require.NoError(t, s.CreateLLMConfig(ctx, c))
	def, err = s.GetDefaultLLMConfig(ctx, empty.ID)
	require.NoError(t, err)
	require.Equal(t, c.ID, def.ID, "oldest config is the implicit default")
}

func TestAgentProfiles(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "acme")

	p := &domain.AgentProfile{OrgID: org.ID, Name: "Sales", SystemPrompt: "sp", Greeting: "hi",
		STTProvider: "faster_whisper", STTModel: "large-v3", TTSProvider: "piper", TTSVoice: "v",
		MaxDurationSec: 300, Tools: []string{"end_call", "transfer_call"}, TransferNumber: "+97699000000"}
	require.NoError(t, s.CreateAgentProfile(ctx, p))
	require.Equal(t, "mn", p.Language)

	got, err := s.GetAgentProfile(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"end_call", "transfer_call"}, got.Tools)
	require.Equal(t, "+97699000000", got.TransferNumber)
	require.Equal(t, 300, got.MaxDurationSec)
	require.Nil(t, got.LLMConfigID)

	p2 := &domain.AgentProfile{OrgID: org.ID, Name: "Support"}
	require.NoError(t, s.CreateAgentProfile(ctx, p2))
	got, err = s.GetAgentProfile(ctx, p2.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Tools)
	require.Empty(t, got.Tools)

	p.Name = "Sales v2"
	p.Tools = nil
	p.Language = "en"
	require.NoError(t, s.UpdateAgentProfile(ctx, p))
	got, err = s.GetAgentProfile(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, "Sales v2", got.Name)
	require.Equal(t, "en", got.Language)
	require.Empty(t, got.Tools)
	require.True(t, !got.UpdatedAt.Before(got.CreatedAt))

	p.LLMConfigID = ptr(uuid.New())
	requireErrIs(t, s.UpdateAgentProfile(ctx, p), domain.ErrInvalid)

	list, err := s.ListAgentProfiles(ctx, org.ID)
	require.NoError(t, err)
	require.Len(t, list, 2)

	require.NoError(t, s.DeleteAgentProfile(ctx, p2.ID))
	requireErrIs(t, s.DeleteAgentProfile(ctx, p2.ID), domain.ErrNotFound)
	_, err = s.GetAgentProfile(ctx, p2.ID)
	requireErrIs(t, err, domain.ErrNotFound)
	requireErrIs(t, s.UpdateAgentProfile(ctx, &domain.AgentProfile{ID: uuid.New()}), domain.ErrNotFound)
}

func TestSIPNumbers(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "acme")
	other := newOrg(t, ctx, s, "other")
	p := &domain.AgentProfile{OrgID: org.ID, Name: "P"}
	require.NoError(t, s.CreateAgentProfile(ctx, p))

	n := &domain.SIPNumber{OrgID: org.ID, Number: "+97677001234", Label: "Main", AgentProfileID: &p.ID,
		AllowInbound: true, AllowOutbound: false, Active: true, AsteriskEndpoint: "trunk-1"}
	require.NoError(t, s.CreateSIPNumber(ctx, n))
	require.NotEqual(t, uuid.Nil, n.ID)

	requireErrIs(t, s.CreateSIPNumber(ctx, &domain.SIPNumber{OrgID: other.ID, Number: "+97677001234"}), domain.ErrConflict)

	got, err := s.GetSIPNumberByNumber(ctx, "+97677001234")
	require.NoError(t, err)
	require.Equal(t, n.ID, got.ID)
	require.Equal(t, org.ID, got.OrgID)
	require.Equal(t, p.ID, *got.AgentProfileID)
	require.True(t, got.AllowInbound)
	require.False(t, got.AllowOutbound)
	require.Equal(t, "trunk-1", got.AsteriskEndpoint)

	n.InboundTrunkID, n.OutboundTrunkID, n.DispatchRuleID = "ST_in", "ST_out", "SDR_1"
	n.AllowOutbound = true
	require.NoError(t, s.UpdateSIPNumber(ctx, n))
	got, err = s.GetSIPNumber(ctx, n.ID)
	require.NoError(t, err)
	require.Equal(t, "ST_in", got.InboundTrunkID)
	require.Equal(t, "ST_out", got.OutboundTrunkID)
	require.Equal(t, "SDR_1", got.DispatchRuleID)
	require.True(t, got.AllowOutbound)

	n2 := &domain.SIPNumber{OrgID: org.ID, Number: "+97677001000"}
	require.NoError(t, s.CreateSIPNumber(ctx, n2))
	n2.Number = "+97677001234"
	requireErrIs(t, s.UpdateSIPNumber(ctx, n2), domain.ErrConflict)

	list, err := s.ListSIPNumbers(ctx, org.ID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, "+97677001000", list[0].Number)

	// Deleting the profile unbinds the number.
	require.NoError(t, s.DeleteAgentProfile(ctx, p.ID))
	got, err = s.GetSIPNumber(ctx, n.ID)
	require.NoError(t, err)
	require.Nil(t, got.AgentProfileID)

	require.NoError(t, s.DeleteSIPNumber(ctx, n.ID))
	requireErrIs(t, s.DeleteSIPNumber(ctx, n.ID), domain.ErrNotFound)
	_, err = s.GetSIPNumber(ctx, n.ID)
	requireErrIs(t, err, domain.ErrNotFound)
	_, err = s.GetSIPNumberByNumber(ctx, "+97677001234")
	requireErrIs(t, err, domain.ErrNotFound)
	requireErrIs(t, s.UpdateSIPNumber(ctx, &domain.SIPNumber{ID: uuid.New(), Number: "+1"}), domain.ErrNotFound)
}
