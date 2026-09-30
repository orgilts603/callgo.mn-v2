package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/objstore"
)

var recJWT = []byte("recordings-test-jwt-secret-0123456789")

func recToken(t *testing.T, org uuid.UUID, role domain.Role) string {
	t.Helper()
	tok, err := auth.IssueToken(recJWT, auth.Claims{UserID: uuid.New(), OrgID: org, Role: role}, time.Hour)
	require.NoError(t, err)
	return tok
}

func recDo(h http.Handler, method, path, tok string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func recErrCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	return body.Error.Code
}

type recFakeSvc struct {
	mu      sync.Mutex
	calls   map[uuid.UUID]*domain.Call
	url     string
	exp     time.Time
	deleted []uuid.UUID
}

func (f *recFakeSvc) Call(_ context.Context, id uuid.UUID) (*domain.Call, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.calls[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (f *recFakeSvc) SignedURL(_ context.Context, id uuid.UUID) (string, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls[id]
	if c == nil || c.Recording == nil || c.Recording.Status != "ready" {
		return "", time.Time{}, domain.ErrNotFound
	}
	return f.url, f.exp, nil
}

func (f *recFakeSvc) Delete(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls[id]
	if c == nil || c.Recording == nil || c.Recording.Status == "deleted" {
		return domain.ErrNotFound
	}
	c.Recording.Status = "deleted"
	f.deleted = append(f.deleted, id)
	return nil
}

type recFakeAudit struct {
	mu      sync.Mutex
	entries []domain.AuditEntry
}

func (a *recFakeAudit) Append(_ context.Context, e *domain.AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, *e)
	return nil
}

type recEnv struct {
	h       http.Handler
	svc     *recFakeSvc
	audit   *recFakeAudit
	org     uuid.UUID
	ready   uuid.UUID
	pending uuid.UUID
	feature bool
}

func newRecEnv(t *testing.T, mutate ...func(*RecordingDeps)) *recEnv {
	t.Helper()
	e := &recEnv{org: uuid.New(), ready: uuid.New(), pending: uuid.New(), feature: true, audit: &recFakeAudit{}}
	e.svc = &recFakeSvc{
		url: "https://s3.example.mn/b/key.ogg?X-Amz-Signature=abc",
		exp: time.Date(2026, 9, 30, 12, 10, 0, 0, time.UTC),
		calls: map[uuid.UUID]*domain.Call{
			e.ready:   {ID: e.ready, OrgID: e.org, Recording: &domain.RecordingInfo{Status: "ready", ObjectKey: "k.ogg"}},
			e.pending: {ID: e.pending, OrgID: e.org, Recording: &domain.RecordingInfo{Status: "recording"}},
		},
	}
	d := RecordingDeps{
		Svc: e.svc, Audit: e.audit,
		HasFeature: func(_ context.Context, org uuid.UUID, feature string) (bool, error) {
			require.Equal(t, "recordings", feature)
			return e.feature && org == e.org, nil
		},
	}
	for _, m := range mutate {
		m(&d)
	}
	r := chi.NewRouter()
	mountRecordings(r, d, Config{JWTSecret: recJWT}, zerolog.Nop())
	e.h = r
	return e
}

func TestRecordingRedirectAndURL(t *testing.T) {
	e := newRecEnv(t)
	tok := recToken(t, e.org, domain.RoleOperator)

	w := recDo(e.h, http.MethodGet, "/api/calls/"+e.ready.String()+"/recording", tok)
	require.Equal(t, http.StatusFound, w.Code)
	require.Equal(t, e.svc.url, w.Header().Get("Location"))

	w = recDo(e.h, http.MethodGet, "/api/calls/"+e.ready.String()+"/recording/url", tok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body struct {
		URL       string    `json:"url"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, e.svc.url, body.URL)
	require.True(t, e.svc.exp.Equal(body.ExpiresAt))
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

func TestRecordingNotFoundCases(t *testing.T) {
	e := newRecEnv(t)
	tok := recToken(t, e.org, domain.RoleOperator)
	for _, p := range []string{
		"/api/calls/" + e.pending.String() + "/recording",     // still recording
		"/api/calls/" + e.pending.String() + "/recording/url", // still recording
		"/api/calls/" + uuid.NewString() + "/recording",       // unknown call
	} {
		w := recDo(e.h, http.MethodGet, p, tok)
		require.Equal(t, http.StatusNotFound, w.Code, p)
		require.Equal(t, "not_found", recErrCode(t, w))
	}
	// Other org's call is hidden.
	other := recToken(t, uuid.New(), domain.RoleOwner)
	w := recDo(e.h, http.MethodGet, "/api/calls/"+e.ready.String()+"/recording", other)
	require.Equal(t, http.StatusNotFound, w.Code)

	w = recDo(e.h, http.MethodGet, "/api/calls/not-a-uuid/recording", tok)
	require.Equal(t, http.StatusBadRequest, w.Code)

	w = recDo(e.h, http.MethodGet, "/api/calls/"+e.ready.String()+"/recording", "")
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestRecordingFeatureGate(t *testing.T) {
	e := newRecEnv(t)
	e.feature = false
	tok := recToken(t, e.org, domain.RoleOwner)
	w := recDo(e.h, http.MethodGet, "/api/calls/"+e.ready.String()+"/recording/url", tok)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Equal(t, "feature_unavailable", recErrCode(t, w))

	// Deleting is still possible after a downgrade.
	w = recDo(e.h, http.MethodDelete, "/api/calls/"+e.ready.String()+"/recording", tok)
	require.Equal(t, http.StatusNoContent, w.Code)
}

func TestRecordingDelete(t *testing.T) {
	e := newRecEnv(t)
	path := "/api/calls/" + e.ready.String() + "/recording"

	w := recDo(e.h, http.MethodDelete, path, recToken(t, e.org, domain.RoleOperator))
	require.Equal(t, http.StatusForbidden, w.Code, "operators cannot delete")

	w = recDo(e.h, http.MethodDelete, path, recToken(t, e.org, domain.RoleAdmin))
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.Equal(t, []uuid.UUID{e.ready}, e.svc.deleted)
	require.Len(t, e.audit.entries, 1)
	require.Equal(t, "recording.delete", e.audit.entries[0].Action)
	require.Equal(t, e.ready.String(), e.audit.entries[0].TargetID)
	require.Equal(t, e.org, e.audit.entries[0].OrgID)

	w = recDo(e.h, http.MethodDelete, path, recToken(t, e.org, domain.RoleOwner))
	require.Equal(t, http.StatusNotFound, w.Code, "already deleted")
}

func TestRecordingNotConfigured(t *testing.T) {
	r := chi.NewRouter()
	mountRecordings(r, RecordingDeps{}, Config{JWTSecret: recJWT}, zerolog.Nop())
	w := recDo(r, http.MethodGet, "/api/calls/"+uuid.NewString()+"/recording", recToken(t, uuid.New(), domain.RoleOwner))
	require.Equal(t, http.StatusInternalServerError, w.Code)
	w = recDo(r, http.MethodGet, "/api/recordings/file/a.ogg?exp=1&sig=x", "")
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestRecordingLocalFileRoute(t *testing.T) {
	dir := t.TempDir()
	secret := []byte("local-files-secret-0123456789abcdef")
	store, err := objstore.NewLocal(dir, objstore.LocalSigner(secret, ""))
	require.NoError(t, err)
	key := "recordings/org/2026/09/call.ogg"
	require.NoError(t, store.Put(context.Background(), key, "audio/ogg", []byte("OggS-audio")))

	e := newRecEnv(t, func(d *RecordingDeps) {
		d.LocalFiles = objstore.ServeLocal(dir)
		d.LocalSecret = secret
	})
	signed, err := store.SignedURL(context.Background(), key, time.Minute)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(signed, "/api/recordings/file/"))

	w := recDo(e.h, http.MethodGet, signed, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "OggS-audio", w.Body.String())
	require.Equal(t, "audio/ogg", w.Header().Get("Content-Type"))

	w = recDo(e.h, http.MethodHead, signed, "")
	require.Equal(t, http.StatusOK, w.Code)

	for _, bad := range []string{
		strings.Replace(signed, "sig=", "sig=00", 1),
		strings.Replace(signed, "call.ogg", "other.ogg", 1),
		"/api/recordings/file/" + key,
		"/api/recordings/file/" + key + "?exp=1&sig=" + objstore.SignLocal(secret, key, 1), // expired
	} {
		w = recDo(e.h, http.MethodGet, bad, "")
		require.Equal(t, http.StatusForbidden, w.Code, bad)
	}

	// JWT secret is the default signing key.
	e2 := newRecEnv(t, func(d *RecordingDeps) { d.LocalFiles = objstore.ServeLocal(dir) })
	u := objstore.LocalSigner(recJWT, "")(key, time.Now().Add(time.Minute))
	w = recDo(e2.h, http.MethodGet, u, "")
	require.Equal(t, http.StatusOK, w.Code)
}

// TestRecordingRoutesCoexistWithAPISubrouter checks that the root-level
// routes win over an /api sub-router mounted like server.go does it.
func TestRecordingRoutesCoexistWithAPISubrouter(t *testing.T) {
	e := newRecEnv(t)
	r := chi.NewRouter()
	mountRecordings(r, RecordingDeps{Svc: e.svc}, Config{JWTSecret: recJWT}, zerolog.Nop())
	mountHandoff(r, HandoffDeps{LiveKitURL: "wss://x"}, Config{JWTSecret: recJWT}, zerolog.Nop())
	r.Route("/api", func(r chi.Router) {
		r.Get("/calls/{id}", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("legacy-call")) })
		r.Get("/calls/{id}/recording", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("legacy-rec")) })
	})
	tok := recToken(t, e.org, domain.RoleOperator)
	w := recDo(r, http.MethodGet, "/api/calls/"+e.ready.String()+"/recording", tok)
	require.Equal(t, http.StatusFound, w.Code)
	w = recDo(r, http.MethodGet, "/api/calls/"+e.ready.String(), tok)
	require.Equal(t, "legacy-call", w.Body.String())
	w = recDo(r, http.MethodGet, "/api/livekit/config", tok)
	require.Equal(t, http.StatusOK, w.Code)
}
