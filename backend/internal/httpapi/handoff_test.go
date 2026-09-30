package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/livekit/protocol/auth"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	cgauth "github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// handoffCalls is a CallRepository fake; only GetCall/UpdateCall are used.
type handoffCalls struct {
	domain.CallRepository
	mu      sync.Mutex
	calls   map[uuid.UUID]*domain.Call
	updates int
}

func (f *handoffCalls) GetCall(_ context.Context, id uuid.UUID) (*domain.Call, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.calls[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (f *handoffCalls) UpdateCall(_ context.Context, c *domain.Call) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *c
	f.calls[c.ID] = &cp
	f.updates++
	return nil
}

type handoffBus struct {
	mu     sync.Mutex
	events []domain.Event
}

func (b *handoffBus) Publish(_ context.Context, ev domain.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, ev)
}

type handoffSet struct {
	id    uuid.UUID
	state domain.HandoffState
	op    *uuid.UUID
}

type handoffEnv struct {
	h        http.Handler
	calls    *handoffCalls
	bus      *handoffBus
	sets     []handoffSet
	org      uuid.UUID
	active   uuid.UUID
	ended    uuid.UUID
	feature  bool
	operator domain.User
}

const handoffLKKey, handoffLKSecret = "devkey", "devsecret-callgo-local-only-change-me-0123456789"

func newHandoffEnv(t *testing.T, mutate ...func(*HandoffDeps)) *handoffEnv {
	t.Helper()
	e := &handoffEnv{org: uuid.New(), active: uuid.New(), ended: uuid.New(), feature: true, bus: &handoffBus{}}
	e.operator = domain.User{ID: uuid.New(), OrgID: e.org, Name: "Болд", Role: domain.RoleOperator}
	e.calls = &handoffCalls{calls: map[uuid.UUID]*domain.Call{
		e.active: {ID: e.active, OrgID: e.org, Status: domain.StatusActive, RoomName: "call-active"},
		e.ended:  {ID: e.ended, OrgID: e.org, Status: domain.StatusCompleted, RoomName: "call-ended"},
	}}
	d := HandoffDeps{
		Calls:      e.calls,
		LiveKitURL: "wss://lk.example.mn",
		Bus:        e.bus,
		SetHandoff: func(_ context.Context, id uuid.UUID, st domain.HandoffState, op *uuid.UUID) error {
			e.sets = append(e.sets, handoffSet{id, st, op})
			return nil
		},
		HasFeature: func(_ context.Context, org uuid.UUID, feature string) (bool, error) {
			require.Equal(t, "handoff", feature)
			return e.feature, nil
		},
		UserName: func(_ context.Context, id uuid.UUID) (string, error) {
			if id == e.operator.ID {
				return e.operator.Name, nil
			}
			return "", domain.ErrNotFound
		},
	}
	for _, m := range mutate {
		m(&d)
	}
	r := chi.NewRouter()
	mountHandoff(r, d, Config{JWTSecret: recJWT, LiveKitAPIKey: handoffLKKey, LiveKitAPISecret: handoffLKSecret}, zerolog.Nop())
	e.h = r
	return e
}

func (e *handoffEnv) tok(t *testing.T, u domain.User) string {
	t.Helper()
	tok, err := cgauth.IssueToken(recJWT, cgauth.Claims{UserID: u.ID, OrgID: u.OrgID, Role: u.Role}, time.Hour)
	require.NoError(t, err)
	return tok
}

func TestHandoffStart(t *testing.T) {
	e := newHandoffEnv(t)
	w := recDo(e.h, http.MethodPost, "/api/calls/"+e.active.String()+"/handoff", e.tok(t, e.operator))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body struct {
		Token, URL, RoomName, Identity string
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "wss://lk.example.mn", body.URL)
	require.Equal(t, "call-active", body.RoomName)
	require.Equal(t, "op-"+e.operator.ID.String(), body.Identity)

	v, err := auth.ParseAPIToken(body.Token)
	require.NoError(t, err)
	claims, grants, err := v.Verify(handoffLKSecret)
	require.NoError(t, err)
	require.Equal(t, body.Identity, v.Identity())
	require.Equal(t, "Болд", grants.Name)
	require.Equal(t, "operator", grants.Attributes["callgo.role"])
	require.Equal(t, e.operator.ID.String(), grants.Attributes["callgo.userId"])
	require.Equal(t, "call-active", grants.Video.Room)
	require.True(t, grants.Video.RoomJoin)
	require.WithinDuration(t, time.Now().Add(time.Hour), claims.ExpiresAt.Time, 5*time.Second)

	require.Len(t, e.sets, 1)
	require.Equal(t, domain.HandoffRequested, e.sets[0].state)
	require.Equal(t, e.operator.ID, *e.sets[0].op)

	require.Len(t, e.bus.events, 1)
	ev := e.bus.events[0]
	require.Equal(t, domain.EventCallUpdated, ev.Type)
	require.Equal(t, e.org, ev.OrgID)
	p := ev.Payload.(map[string]any)
	require.Equal(t, "requested", p["handoff"])
	require.Equal(t, e.operator.ID.String(), p["operatorId"])
	require.Equal(t, domain.HandoffRequested, p["call"].(*domain.Call).Handoff)
}

func TestHandoffStartRejections(t *testing.T) {
	e := newHandoffEnv(t)
	tok := e.tok(t, e.operator)

	w := recDo(e.h, http.MethodPost, "/api/calls/"+e.ended.String()+"/handoff", tok)
	require.Equal(t, http.StatusConflict, w.Code)

	w = recDo(e.h, http.MethodPost, "/api/calls/"+uuid.NewString()+"/handoff", tok)
	require.Equal(t, http.StatusNotFound, w.Code)

	stranger := domain.User{ID: uuid.New(), OrgID: uuid.New(), Role: domain.RoleOwner}
	w = recDo(e.h, http.MethodPost, "/api/calls/"+e.active.String()+"/handoff", e.tok(t, stranger))
	require.Equal(t, http.StatusNotFound, w.Code, "other org")

	w = recDo(e.h, http.MethodPost, "/api/calls/"+e.active.String()+"/handoff", "")
	require.Equal(t, http.StatusUnauthorized, w.Code)

	e.feature = false
	w = recDo(e.h, http.MethodPost, "/api/calls/"+e.active.String()+"/handoff", tok)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Equal(t, "feature_unavailable", recErrCode(t, w))
	require.Empty(t, e.sets)
	require.Empty(t, e.bus.events)
}

func TestHandoffEnd(t *testing.T) {
	e := newHandoffEnv(t)
	opTok := e.tok(t, e.operator)
	path := "/api/calls/" + e.active.String() + "/handoff/end"

	w := recDo(e.h, http.MethodPost, path, opTok)
	require.Equal(t, http.StatusConflict, w.Code, "no handoff yet")

	w = recDo(e.h, http.MethodPost, "/api/calls/"+e.active.String()+"/handoff", opTok)
	require.Equal(t, http.StatusOK, w.Code)
	// Persisted via SetHandoff; mirror it into the fake repo.
	e.calls.calls[e.active].Handoff = domain.HandoffRequested
	e.calls.calls[e.active].OperatorID = &e.operator.ID

	other := domain.User{ID: uuid.New(), OrgID: e.org, Role: domain.RoleOperator}
	w = recDo(e.h, http.MethodPost, path, e.tok(t, other))
	require.Equal(t, http.StatusForbidden, w.Code, "another operator")

	w = recDo(e.h, http.MethodPost, path, opTok)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.Equal(t, domain.HandoffEnded, e.sets[len(e.sets)-1].state)
	last := e.bus.events[len(e.bus.events)-1].Payload.(map[string]any)
	require.Equal(t, "ended", last["handoff"])

	e.calls.calls[e.active].Handoff = domain.HandoffEnded
	w = recDo(e.h, http.MethodPost, path, opTok)
	require.Equal(t, http.StatusNoContent, w.Code, "idempotent")

	// Admins may end someone else's handoff.
	e.calls.calls[e.active].Handoff = domain.HandoffActive
	admin := domain.User{ID: uuid.New(), OrgID: e.org, Role: domain.RoleAdmin}
	w = recDo(e.h, http.MethodPost, path, e.tok(t, admin))
	require.Equal(t, http.StatusNoContent, w.Code)
}

func TestHandoffFallbackUpdateCallAndConfig(t *testing.T) {
	e := newHandoffEnv(t, func(d *HandoffDeps) { d.SetHandoff = nil; d.UserName = nil })
	w := recDo(e.h, http.MethodPost, "/api/calls/"+e.active.String()+"/handoff", e.tok(t, e.operator))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, e.calls.updates)
	stored := e.calls.calls[e.active]
	require.Equal(t, domain.HandoffRequested, stored.Handoff)
	require.Equal(t, e.operator.ID, *stored.OperatorID)

	w = recDo(e.h, http.MethodGet, "/api/livekit/config", e.tok(t, e.operator))
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"url":"wss://lk.example.mn"}`, w.Body.String())

	e2 := newHandoffEnv(t, func(d *HandoffDeps) { d.LiveKitURL = "" })
	w = recDo(e2.h, http.MethodGet, "/api/livekit/config", e2.tok(t, e2.operator))
	require.Equal(t, http.StatusInternalServerError, w.Code)
	w = recDo(e2.h, http.MethodPost, "/api/calls/"+e2.active.String()+"/handoff", e2.tok(t, e2.operator))
	require.Equal(t, http.StatusInternalServerError, w.Code)
}
