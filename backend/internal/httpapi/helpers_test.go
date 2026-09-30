package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

const (
	testAgentToken = "agent-secret"
	testLKKey      = "APIkey"
	testLKSecret   = "lk-secret-lk-secret-lk-secret-123"
	adminPassword  = "admin1234"
)

var testJWT = []byte("jwt-secret")

type env struct {
	t      *testing.T
	db     *fakeDB
	tel    *fakeTelephony
	bus    *fakeBus
	hub    *fakeHub
	ctl    *fakeCampaigns
	dnc    *fakeDNC
	lex    *fakeLexiconEngine
	tester *fakeTester
	kn     *fakeKnowledge
	srv    *server
	h      http.Handler

	org, org2               domain.Organization
	admin, operator, other  domain.User
	adminTok, opTok, othTok string
}

func newEnv(t *testing.T, mutate ...func(*Deps, *Config)) *env {
	t.Helper()
	db := newFakeDB()
	e := &env{t: t, db: db, tel: &fakeTelephony{}, bus: &fakeBus{}, hub: &fakeHub{}, ctl: &fakeCampaigns{db: db}, dnc: newFakeDNC(),
		lex: &fakeLexiconEngine{}, tester: &fakeTester{}, kn: newFakeKnowledge()}
	ctx := context.Background()
	now := time.Now().UTC()
	e.org = domain.Organization{ID: uuid.New(), Name: "Demo", Slug: "demo", CreatedAt: now}
	e.org2 = domain.Organization{ID: uuid.New(), Name: "Other", Slug: "other", CreatedAt: now}
	require.NoError(t, db.CreateOrg(ctx, &e.org))
	require.NoError(t, db.CreateOrg(ctx, &e.org2))
	hash, err := auth.HashPassword(adminPassword)
	require.NoError(t, err)
	e.admin = domain.User{ID: uuid.New(), OrgID: e.org.ID, Email: "admin@callgo.mn", Name: "Admin", Role: domain.RoleOwner, PasswordHash: hash}
	e.operator = domain.User{ID: uuid.New(), OrgID: e.org.ID, Email: "op@callgo.mn", Name: "Op", Role: domain.RoleOperator, PasswordHash: hash}
	e.other = domain.User{ID: uuid.New(), OrgID: e.org2.ID, Email: "x@other.mn", Name: "X", Role: domain.RoleAdmin, PasswordHash: hash}
	for _, u := range []*domain.User{&e.admin, &e.operator, &e.other} {
		require.NoError(t, db.CreateUser(ctx, u))
	}
	e.adminTok = e.token(e.admin)
	e.opTok = e.token(e.operator)
	e.othTok = e.token(e.other)

	deps := Deps{
		Org: db, SIPNumber: db, AgentProfile: db, LLMConfig: db, Call: db, Contact: db, Campaign: db, Lexicon: db,
		DNC:       e.dnc,
		Telephony: e.tel, Bus: e.bus, Live: e.hub, Campaigns: e.ctl, TargetParser: listParser{}, ContactParser: listParser{},
		LexiconEngine: e.lex, LLMTester: e.tester,
		Knowledge: e.kn, KnowledgeRepo: e.kn,
	}
	cfg := Config{JWTSecret: testJWT, AgentToken: testAgentToken, LiveKitAPIKey: testLKKey, LiveKitAPISecret: testLKSecret}
	for _, m := range mutate {
		m(&deps, &cfg)
	}
	e.srv = newServer(deps, cfg, zerolog.Nop())
	e.h = e.srv.routes()
	return e
}

func (e *env) token(u domain.User) string {
	tok, err := auth.IssueToken(testJWT, auth.Claims{UserID: u.ID, OrgID: u.OrgID, Role: u.Role}, time.Hour)
	require.NoError(e.t, err)
	return tok
}

func (e *env) do(method, path, token string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		if s, ok := body.(string); ok {
			rd = bytes.NewBufferString(s)
		} else {
			b, err := json.Marshal(body)
			require.NoError(e.t, err)
			rd = bytes.NewReader(b)
		}
	}
	r := httptest.NewRequest(method, path, rd)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func (e *env) agent(method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(e.t, err)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rd)
	r.Header.Set(auth.AgentTokenHeader, testAgentToken)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func (e *env) multipart(path, token string, fields map[string]string, file string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.upload(path, token, fields, "data.csv", []byte(file))
}

// upload posts a multipart form with an optional "file" part named filename.
func (e *env) upload(path, token string, fields map[string]string, filename string, file []byte) *httptest.ResponseRecorder {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		require.NoError(e.t, mw.WriteField(k, v))
	}
	if len(file) > 0 {
		fw, err := mw.CreateFormFile("file", filename)
		require.NoError(e.t, err)
		_, err = fw.Write(file)
		require.NoError(e.t, err)
	}
	require.NoError(e.t, mw.Close())
	r := httptest.NewRequest(http.MethodPost, path, &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &v), w.Body.String())
	return v
}

type errBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func requireErr(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	require.Equal(t, status, w.Code, w.Body.String())
	require.Equal(t, code, decode[errBody](t, w).Error.Code)
}

// --- seed helpers ---

func (e *env) seedProfile(orgID uuid.UUID, llmID *uuid.UUID) domain.AgentProfile {
	p := domain.AgentProfile{ID: uuid.New(), OrgID: orgID, Name: "Sales", Language: "mn", LLMConfigID: llmID, Tools: []string{"end_call"}, CreatedAt: time.Now()}
	require.NoError(e.t, e.db.CreateAgentProfile(context.Background(), &p))
	return p
}

func (e *env) seedNumber(orgID uuid.UUID, number string, profileID *uuid.UUID) domain.SIPNumber {
	n := domain.SIPNumber{ID: uuid.New(), OrgID: orgID, Number: number, Label: "Main", AgentProfileID: profileID,
		AllowInbound: true, AllowOutbound: true, Active: true}
	require.NoError(e.t, e.db.CreateSIPNumber(context.Background(), &n))
	return n
}

func (e *env) seedCall(orgID uuid.UUID, status domain.CallStatus, mutate ...func(*domain.Call)) domain.Call {
	id := uuid.New()
	c := domain.Call{ID: id, OrgID: orgID, Direction: domain.DirectionInbound, Status: status, FromNumber: "+97699110000",
		ToNumber: "+97677001234", RoomName: "call-" + id.String(), StartedAt: time.Now().UTC()}
	if status == domain.StatusActive {
		at := time.Now().UTC().Add(-30 * time.Second)
		c.AnsweredAt = &at
	}
	for _, m := range mutate {
		m(&c)
	}
	require.NoError(e.t, e.db.CreateCall(context.Background(), &c))
	return c
}

func (e *env) seedContact(orgID uuid.UUID, phone, name string) domain.Contact {
	c := domain.Contact{OrgID: orgID, Phone: phone, Name: name, Tags: []string{}, Meta: map[string]string{}}
	require.NoError(e.t, e.db.UpsertContact(context.Background(), &c))
	return c
}

func (e *env) seedLLM(orgID uuid.UUID, name, key string, isDefault bool, fallback *uuid.UUID) domain.LLMConfig {
	c := domain.LLMConfig{ID: uuid.New(), OrgID: orgID, Name: name, Provider: domain.ProviderOpenAI, Model: "gpt-4.1-mini",
		APIKey: key, APIKeyHint: keyHint(key), Temperature: 0.5, MaxTokens: 256, IsDefault: isDefault, FallbackID: fallback}
	require.NoError(e.t, e.db.CreateLLMConfig(context.Background(), &c))
	return c
}

func (e *env) call(id uuid.UUID) domain.Call {
	c, err := e.db.GetCall(context.Background(), id)
	require.NoError(e.t, err)
	return *c
}
