package httpapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func TestHealth(t *testing.T) {
	e := newEnv(t)
	assert.Equal(t, http.StatusOK, e.do(http.MethodGet, "/healthz", "", nil).Code)
	assert.Equal(t, http.StatusOK, e.do(http.MethodGet, "/readyz", "", nil).Code)

	down := newEnv(t, func(d *Deps, _ *Config) { d.Ready = func(context.Context) error { return errors.New("db down") } })
	assert.Equal(t, http.StatusServiceUnavailable, down.do(http.MethodGet, "/readyz", "", nil).Code)
	requireErr(t, e.do(http.MethodGet, "/api/nope", e.adminTok, nil), http.StatusNotFound, "not_found")
}

func TestLogin(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name   string
		body   any
		status int
	}{
		{"ok", map[string]string{"email": "ADMIN@callgo.mn ", "password": adminPassword}, http.StatusOK},
		{"wrong password", map[string]string{"email": "admin@callgo.mn", "password": "nope"}, http.StatusUnauthorized},
		{"unknown email", map[string]string{"email": "who@callgo.mn", "password": adminPassword}, http.StatusUnauthorized},
		{"missing fields", map[string]string{"email": ""}, http.StatusBadRequest},
		{"malformed", "{", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := e.do(http.MethodPost, "/api/auth/login", "", tc.body)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			if tc.status != http.StatusOK {
				assert.NotEmpty(t, decode[errBody](t, w).Error.Code)
				return
			}
			res := decode[struct {
				Token string              `json:"token"`
				User  domain.User         `json:"user"`
				Org   domain.Organization `json:"org"`
			}](t, w)
			assert.NotEmpty(t, res.Token)
			assert.Equal(t, e.admin.ID, res.User.ID)
			assert.Equal(t, e.org.ID, res.Org.ID)
			assert.NotContains(t, w.Body.String(), "passwordHash")
			assert.NotContains(t, w.Body.String(), e.admin.PasswordHash)

			me := e.do(http.MethodGet, "/api/auth/me", res.Token, nil)
			require.Equal(t, http.StatusOK, me.Code)
			assert.Contains(t, me.Body.String(), `"email":"admin@callgo.mn"`)
		})
	}
}

func TestAuthRequired(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/api/auth/me", "/api/calls", "/api/stats", "/api/lexicon", "/api/llm-configs", "/api/ws"} {
		requireErr(t, e.do(http.MethodGet, path, "", nil), http.StatusUnauthorized, "unauthorized")
		requireErr(t, e.do(http.MethodGet, path, "garbage", nil), http.StatusUnauthorized, "unauthorized")
	}
	// Token signed with another secret.
	other := newEnv(t, func(_ *Deps, c *Config) { c.JWTSecret = []byte("different") })
	requireErr(t, other.do(http.MethodGet, "/api/calls", e.adminTok, nil), http.StatusUnauthorized, "unauthorized")
}

func TestRegister(t *testing.T) {
	e := newEnv(t)
	body := map[string]string{"orgName": "Acme ХХК", "email": "boss@acme.mn", "password": "supersecret", "name": "Boss"}
	requireErr(t, e.do(http.MethodPost, "/api/auth/register", "", body), http.StatusForbidden, "forbidden")

	e = newEnv(t, func(_ *Deps, c *Config) { c.AllowSignup = true })
	w := e.do(http.MethodPost, "/api/auth/register", "", body)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	res := decode[struct {
		Token string              `json:"token"`
		User  domain.User         `json:"user"`
		Org   domain.Organization `json:"org"`
	}](t, w)
	assert.Equal(t, domain.RoleOwner, res.User.Role)
	assert.Equal(t, "acme", res.Org.Slug)
	assert.Equal(t, res.Org.ID, res.User.OrgID)
	assert.Equal(t, http.StatusOK, e.do(http.MethodGet, "/api/auth/me", res.Token, nil).Code)

	requireErr(t, e.do(http.MethodPost, "/api/auth/register", "", body), http.StatusConflict, "conflict")
	bad := map[string]string{"orgName": "X", "email": "not-an-email", "password": "supersecret", "name": "B"}
	requireErr(t, e.do(http.MethodPost, "/api/auth/register", "", bad), http.StatusBadRequest, "invalid")
	short := map[string]string{"orgName": "X", "email": "a@b.mn", "password": "short", "name": "B"}
	requireErr(t, e.do(http.MethodPost, "/api/auth/register", "", short), http.StatusBadRequest, "invalid")
}

func TestRoles(t *testing.T) {
	e := newEnv(t)
	body := map[string]any{"number": "+97677001234", "label": "Main", "allowInbound": true}
	requireErr(t, e.do(http.MethodPost, "/api/sip-numbers", e.opTok, body), http.StatusForbidden, "forbidden")
	requireErr(t, e.do(http.MethodPost, "/api/llm-configs", e.opTok, map[string]any{}), http.StatusForbidden, "forbidden")
	requireErr(t, e.do(http.MethodPost, "/api/agent-profiles", e.opTok, map[string]any{}), http.StatusForbidden, "forbidden")
	assert.Equal(t, http.StatusOK, e.do(http.MethodGet, "/api/sip-numbers", e.opTok, nil).Code)
	// Operators may correct lexicon / contacts.
	assert.Equal(t, http.StatusCreated, e.do(http.MethodPost, "/api/lexicon", e.opTok, map[string]string{"wrong": "a", "correct": "b"}).Code)
}

func TestOrgIsolation(t *testing.T) {
	e := newEnv(t)
	foreign := e.seedCall(e.org2.ID, domain.StatusActive)
	mine := e.seedCall(e.org.ID, domain.StatusCompleted)
	turn := domain.TranscriptTurn{CallID: foreign.ID, Seq: 1, Speaker: domain.SpeakerCustomer, Text: "hi"}
	require.NoError(t, e.db.AddTurn(context.Background(), &turn))
	fNum := e.seedNumber(e.org2.ID, "+97677009999", nil)
	fContact := e.seedContact(e.org2.ID, "+97699000001", "F")
	fLLM := e.seedLLM(e.org2.ID, "theirs", "sk-theirs-123456", true, nil)
	fProfile := e.seedProfile(e.org2.ID, nil)

	cid := foreign.ID.String()
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/calls/" + cid},
		{http.MethodPost, "/api/calls/" + cid + "/hangup"},
		{http.MethodGet, "/api/calls/" + cid + "/recording"},
		{http.MethodGet, "/api/contacts/" + fContact.ID.String()},
		{http.MethodDelete, "/api/contacts/" + fContact.ID.String()},
		{http.MethodDelete, "/api/sip-numbers/" + fNum.ID.String()},
		{http.MethodPost, "/api/sip-numbers/" + fNum.ID.String() + "/provision"},
		{http.MethodDelete, "/api/llm-configs/" + fLLM.ID.String()},
		{http.MethodPost, "/api/llm-configs/" + fLLM.ID.String() + "/test"},
		{http.MethodDelete, "/api/agent-profiles/" + fProfile.ID.String()},
	} {
		requireErr(t, e.do(tc.method, tc.path, e.adminTok, nil), http.StatusNotFound, "not_found")
	}
	requireErr(t, e.do(http.MethodPatch, "/api/turns/"+turn.ID.String(), e.adminTok, map[string]string{"text": "x"}), http.StatusNotFound, "not_found")

	list := decode[list[domain.Call]](t, e.do(http.MethodGet, "/api/calls", e.adminTok, nil))
	require.Len(t, list.Items, 1)
	assert.Equal(t, mine.ID, list.Items[0].ID)
	assert.Empty(t, e.tel.hangups)

	// Referencing another org's number when dialing is a validation error.
	w := e.do(http.MethodPost, "/api/calls/dial", e.adminTok, map[string]string{"toNumber": "+97699112233", "sipNumberId": fNum.ID.String()})
	requireErr(t, w, http.StatusBadRequest, "invalid")
}

func TestErrorMapping(t *testing.T) {
	e := newEnv(t)
	w := e.do(http.MethodGet, "/api/calls/not-a-uuid", e.adminTok, nil)
	requireErr(t, w, http.StatusBadRequest, "invalid")
	w = e.do(http.MethodGet, "/api/calls/"+uuid.NewString(), e.adminTok, nil)
	requireErr(t, w, http.StatusNotFound, "not_found")
}
