package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/callbacks"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

type cbWorld struct {
	*rtWorld
	repo *rtRepo
	bus  *rtBus
	h    http.Handler
}

func newCBWorld(t *testing.T, hard bool) *cbWorld {
	t.Helper()
	w := &cbWorld{rtWorld: newRTWorld(), repo: newRTRepo(), bus: &rtBus{}}
	var repo domain.IntegrationsRepository = w.repo
	if hard {
		repo = &rtHardRepo{rtRepo: w.repo}
	}
	sched := callbacks.New(repo, nil, rtContacts{}, w.numbers, w.profiles, nil, nil, w.bus,
		callbacks.Config{Now: func() time.Time { return rtNow }}, rtLog())
	d := &CallbackDeps{Sched: sched, Repo: repo, Audit: w.audit, Now: func() time.Time { return rtNow }}
	w.h = w.rtRouter(func(r chi.Router) { mountCallbacks(r, d, Config{}, rtLog()) })
	return w
}

func (w *cbWorld) seed(org uuid.UUID, status domain.CallbackStatus) domain.CallbackRequest {
	c := domain.CallbackRequest{ID: uuid.New(), OrgID: org, Phone: "+97699112233", Note: "n", DueAt: rtNow.Add(time.Hour), Status: status}
	_ = w.repo.CreateCallback(nil, &c) //nolint:staticcheck // fake ignores ctx
	return c
}

func dueIn(d time.Duration) string { return rtNow.Add(d).Format(time.RFC3339) }

func TestCallbacksCreate(t *testing.T) {
	w := newCBWorld(t, false)
	res := rtDo(t, w.h, "operator", http.MethodPost, "/api/callbacks", map[string]any{
		"phone": "9911 2233", "name": "Bat", "note": "asked for price", "dueAt": dueIn(2 * time.Hour),
		"sipNumberId": w.number.ID.String(), "agentProfileId": w.profile.ID.String(),
	})
	require.Equal(t, http.StatusCreated, res.Code, string(res.Raw))
	var out struct {
		Callback domain.CallbackRequest `json:"callback"`
	}
	res.decode(t, &out)
	c := out.Callback
	assert.Equal(t, "+97699112233", c.Phone)
	assert.Equal(t, domain.CallbackPending, c.Status)
	assert.Equal(t, w.org.ID, c.OrgID)
	require.NotNil(t, c.CreatedBy)
	assert.Equal(t, w.operator.UserID, *c.CreatedBy)
	assert.True(t, c.DueAt.Equal(rtNow.Add(2*time.Hour)))
	assert.Len(t, w.repo.items, 1)
	assert.Equal(t, []string{"callback.create"}, w.audit.actions())
	require.Len(t, w.bus.events, 1)
	assert.Equal(t, callbacks.EventCallbackScheduled, w.bus.events[0].Type)
}

func TestCallbacksCreateRejects(t *testing.T) {
	w := newCBWorld(t, false)
	tests := []struct {
		name string
		body any
	}{
		{"bad phone", map[string]any{"phone": "xx", "dueAt": dueIn(time.Hour)}},
		{"no dueAt", map[string]any{"phone": "+97699112233"}},
		{"past dueAt", map[string]any{"phone": "+97699112233", "dueAt": dueIn(-time.Minute)}},
		{"bad dueAt format", map[string]any{"phone": "+97699112233", "dueAt": "tomorrow"}},
		{"bad sipNumberId", map[string]any{"phone": "+97699112233", "dueAt": dueIn(time.Hour), "sipNumberId": "nope"}},
		{"unknown sipNumberId", map[string]any{"phone": "+97699112233", "dueAt": dueIn(time.Hour), "sipNumberId": uuid.NewString()}},
		{"foreign profile", map[string]any{"phone": "+97699112233", "dueAt": dueIn(time.Hour), "agentProfileId": w.profile2.ID.String()}},
		{"unknown contact", map[string]any{"phone": "+97699112233", "dueAt": dueIn(time.Hour), "contactId": uuid.NewString()}},
		{"empty body", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := rtDo(t, w.h, "admin", http.MethodPost, "/api/callbacks", tc.body)
			assert.Equal(t, http.StatusBadRequest, res.Code, string(res.Raw))
			assert.Equal(t, "invalid", res.errCode(t))
		})
	}
	assert.Empty(t, w.repo.items)
	assert.Equal(t, http.StatusUnauthorized, rtDo(t, w.h, "", http.MethodPost, "/api/callbacks", map[string]any{}).Code)
}

func TestCallbacksList(t *testing.T) {
	w := newCBWorld(t, false)
	w.seed(w.org.ID, domain.CallbackPending)
	w.seed(w.org.ID, domain.CallbackPending)
	w.seed(w.org.ID, domain.CallbackDone)
	w.seed(w.org2.ID, domain.CallbackPending)

	var out struct {
		Items []domain.CallbackRequest `json:"items"`
		Total int                      `json:"total"`
	}
	res := rtDo(t, w.h, "operator", http.MethodGet, "/api/callbacks", nil)
	require.Equal(t, http.StatusOK, res.Code)
	res.decode(t, &out)
	assert.Equal(t, 3, out.Total, "only the caller's org")
	assert.Len(t, out.Items, 3)

	res = rtDo(t, w.h, "operator", http.MethodGet, "/api/callbacks?status=pending&limit=1", nil)
	out.Items = nil
	res.decode(t, &out)
	assert.Equal(t, 2, out.Total)
	assert.Len(t, out.Items, 1)

	res = rtDo(t, w.h, "operator", http.MethodGet, "/api/callbacks?status=done", nil)
	out.Items = nil
	res.decode(t, &out)
	assert.Equal(t, 1, out.Total)

	res = rtDo(t, w.h, "foreign", http.MethodGet, "/api/callbacks?status=done", nil)
	res.decode(t, &out)
	assert.Zero(t, out.Total)
	assert.Equal(t, `{"items":[],"total":0}`+"\n", string(res.Raw), "empty list is [] not null")

	assert.Equal(t, http.StatusBadRequest, rtDo(t, w.h, "admin", http.MethodGet, "/api/callbacks?status=weird", nil).Code)
	assert.Equal(t, http.StatusBadRequest, rtDo(t, w.h, "admin", http.MethodGet, "/api/callbacks?limit=0", nil).Code)
}

func TestCallbacksUpdate(t *testing.T) {
	w := newCBWorld(t, false)
	c := w.seed(w.org.ID, domain.CallbackPending)
	path := "/api/callbacks/" + c.ID.String()

	res := rtDo(t, w.h, "operator", http.MethodPut, path, map[string]any{"dueAt": dueIn(5 * time.Hour), "note": " new note "})
	require.Equal(t, http.StatusOK, res.Code, string(res.Raw))
	stored, _ := w.repo.GetCallback(nil, c.ID) //nolint:staticcheck // fake ignores ctx
	assert.True(t, stored.DueAt.Equal(rtNow.Add(5*time.Hour)))
	assert.Equal(t, "new note", stored.Note)
	assert.Equal(t, domain.CallbackPending, stored.Status)

	res = rtDo(t, w.h, "operator", http.MethodPut, path, map[string]any{"dueAt": dueIn(-time.Hour)})
	assert.Equal(t, http.StatusBadRequest, res.Code)
	res = rtDo(t, w.h, "operator", http.MethodPut, path, map[string]any{"status": "done"})
	assert.Equal(t, http.StatusBadRequest, res.Code, "only canceled may be set")

	res = rtDo(t, w.h, "operator", http.MethodPut, path, map[string]any{"status": "canceled"})
	require.Equal(t, http.StatusOK, res.Code)
	stored, _ = w.repo.GetCallback(nil, c.ID) //nolint:staticcheck // fake ignores ctx
	assert.Equal(t, domain.CallbackCanceled, stored.Status)

	res = rtDo(t, w.h, "operator", http.MethodPut, path, map[string]any{"note": "again"})
	assert.Equal(t, http.StatusConflict, res.Code, "no longer pending")
	assert.Equal(t, "conflict", res.errCode(t))

	assert.Equal(t, http.StatusNotFound, rtDo(t, w.h, "foreign", http.MethodPut, path, map[string]any{"note": "x"}).Code)
	assert.Equal(t, http.StatusNotFound, rtDo(t, w.h, "admin", http.MethodPut, "/api/callbacks/"+uuid.NewString(), map[string]any{"note": "x"}).Code)
	assert.Equal(t, http.StatusBadRequest, rtDo(t, w.h, "admin", http.MethodPut, "/api/callbacks/nope", map[string]any{"note": "x"}).Code)
	assert.Equal(t, []string{"callback.update", "callback.update"}, w.audit.actions())
}

func TestCallbacksDeleteCancelsWithoutHardDelete(t *testing.T) {
	w := newCBWorld(t, false)
	pending := w.seed(w.org.ID, domain.CallbackPending)
	done := w.seed(w.org.ID, domain.CallbackDone)
	dialed := w.seed(w.org.ID, domain.CallbackDialed)

	assert.Equal(t, http.StatusNoContent, rtDo(t, w.h, "operator", http.MethodDelete, "/api/callbacks/"+pending.ID.String(), nil).Code)
	got, _ := w.repo.GetCallback(nil, pending.ID) //nolint:staticcheck // fake ignores ctx
	assert.Equal(t, domain.CallbackCanceled, got.Status)

	assert.Equal(t, http.StatusNoContent, rtDo(t, w.h, "operator", http.MethodDelete, "/api/callbacks/"+done.ID.String(), nil).Code)
	got, _ = w.repo.GetCallback(nil, done.ID) //nolint:staticcheck // fake ignores ctx
	assert.Equal(t, domain.CallbackDone, got.Status, "history is kept")

	res := rtDo(t, w.h, "operator", http.MethodDelete, "/api/callbacks/"+dialed.ID.String(), nil)
	assert.Equal(t, http.StatusConflict, res.Code)

	assert.Equal(t, http.StatusNotFound, rtDo(t, w.h, "foreign", http.MethodDelete, "/api/callbacks/"+pending.ID.String(), nil).Code)
	assert.Equal(t, http.StatusNotFound, rtDo(t, w.h, "admin", http.MethodDelete, "/api/callbacks/"+uuid.NewString(), nil).Code)
	assert.Equal(t, http.StatusUnauthorized, rtDo(t, w.h, "", http.MethodDelete, "/api/callbacks/"+pending.ID.String(), nil).Code)
}

func TestCallbacksDeleteHardWhenRepositorySupportsIt(t *testing.T) {
	w := newCBWorld(t, true)
	c := w.seed(w.org.ID, domain.CallbackPending)
	assert.Equal(t, http.StatusNoContent, rtDo(t, w.h, "admin", http.MethodDelete, "/api/callbacks/"+c.ID.String(), nil).Code)
	_, err := w.repo.GetCallback(nil, c.ID) //nolint:staticcheck // fake ignores ctx
	assert.ErrorIs(t, err, domain.ErrNotFound)
	assert.Equal(t, []string{"callback.delete"}, w.audit.actions())
}

func TestCallbacksNotConfigured(t *testing.T) {
	w := newRTWorld()
	h := w.rtRouter(func(r chi.Router) { mountCallbacks(r, &CallbackDeps{}, Config{}, rtLog()) })
	res := rtDo(t, h, "admin", http.MethodGet, "/api/callbacks", nil)
	assert.Equal(t, http.StatusInternalServerError, res.Code)
	assert.Equal(t, "internal", res.errCode(t))
}
