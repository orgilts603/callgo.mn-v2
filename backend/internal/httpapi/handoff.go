package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	lkclient "github.com/orgilts603/callgo.mn-v2/backend/internal/livekit"
)

// Participant attributes of an operator (read by the agent worker).
const (
	handoffRoleAttr   = "callgo.role"
	handoffUserAttr   = "callgo.userId"
	handoffRoleValue  = "operator"
	handoffTokenTTL   = time.Hour
	handoffFeatureKey = "handoff"
)

// HandoffDeps are the collaborators of the operator-handoff API
// (docs/API.md "Operator handoff").
type HandoffDeps struct {
	// Calls loads calls (required). It is also the fallback persistence
	// (UpdateCall) when SetHandoff is nil.
	Calls domain.CallRepository
	// SetHandoff persists call.handoff and call.operatorId.
	SetHandoff func(ctx context.Context, callID uuid.UUID, state domain.HandoffState, operatorID *uuid.UUID) error
	// Token issues a LiveKit access token. Default: livekit.OperatorToken
	// with Config.LiveKitAPIKey/Secret and a 1 h TTL.
	Token func(roomName, identity, name string, attrs map[string]string) (string, error)
	// LiveKitURL is the public ws(s):// URL browsers connect to.
	LiveKitURL string
	// HasFeature gates POST /handoff on the plan feature "handoff". nil = allowed.
	HasFeature func(ctx context.Context, orgID uuid.UUID, feature string) (bool, error)
	Bus        domain.EventBus
	// UserName resolves the operator's display name (optional; default
	// "Оператор").
	UserName func(ctx context.Context, userID uuid.UUID) (string, error)
	// Audit records handoffs; nil skips auditing.
	Audit interface {
		Append(ctx context.Context, e *domain.AuditEntry) error
	}
	// Now is the clock (default time.Now).
	Now func() time.Time
}

type handoffAPI struct {
	d   HandoffDeps
	cfg Config
	log zerolog.Logger
	srv *server // error mapping only
}

// mountHandoff mounts (all authenticated)
//
//	POST /api/calls/{id}/handoff       {token, url, roomName, identity}
//	POST /api/calls/{id}/handoff/end   204
//	GET  /api/livekit/config           {url}
//
// on the root router.
func mountHandoff(r chi.Router, d HandoffDeps, cfg Config, log zerolog.Logger) {
	a := &handoffAPI{d: d, cfg: cfg, log: log, srv: &server{log: log}}
	if a.d.Token == nil && cfg.LiveKitAPIKey != "" && cfg.LiveKitAPISecret != "" {
		key, secret := cfg.LiveKitAPIKey, cfg.LiveKitAPISecret
		a.d.Token = func(room, identity, name string, attrs map[string]string) (string, error) {
			return lkclient.OperatorToken(key, secret, room, identity, name, attrs, handoffTokenTTL)
		}
	}
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(cfg.JWTSecret))
		r.Post("/api/calls/{id}/handoff", a.start)
		r.Post("/api/calls/{id}/handoff/end", a.end)
		r.Get("/api/livekit/config", a.config)
	})
}

func (a *handoffAPI) now() time.Time {
	if a.d.Now != nil {
		return a.d.Now().UTC()
	}
	return time.Now().UTC()
}

func (a *handoffAPI) loadCall(w http.ResponseWriter, r *http.Request) (*domain.Call, bool) {
	if a.d.Calls == nil {
		a.srv.writeErr(w, r, errNotConfigured("handoff"))
		return nil, false
	}
	id, err := urlID(r, "id")
	if err != nil {
		a.srv.writeErr(w, r, err)
		return nil, false
	}
	c, err := a.d.Calls.GetCall(r.Context(), id)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && (c == nil || c.OrgID != claimsOf(r).OrgID)) {
		a.srv.writeErr(w, r, errNotFound("call"))
		return nil, false
	}
	if err != nil {
		a.srv.writeErr(w, r, err)
		return nil, false
	}
	return c, true
}

func (a *handoffAPI) start(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cl := claimsOf(r)
	call, ok := a.loadCall(w, r)
	if !ok {
		return
	}
	if a.d.HasFeature != nil {
		allowed, err := a.d.HasFeature(ctx, cl.OrgID, handoffFeatureKey)
		if err != nil {
			a.srv.writeErr(w, r, err)
			return
		}
		if !allowed {
			auth.WriteError(w, http.StatusForbidden, "feature_unavailable", "your plan does not include handoff")
			return
		}
	}
	if call.Status != domain.StatusActive || call.RoomName == "" {
		a.srv.writeErr(w, r, errConflict("call is not active"))
		return
	}
	if a.d.Token == nil || strings.TrimSpace(a.d.LiveKitURL) == "" {
		a.srv.writeErr(w, r, errNotConfigured("livekit"))
		return
	}
	identity := "op-" + cl.UserID.String()
	name := "Оператор"
	if a.d.UserName != nil {
		if n, err := a.d.UserName(ctx, cl.UserID); err == nil && strings.TrimSpace(n) != "" {
			name = n
		}
	}
	token, err := a.d.Token(call.RoomName, identity, name, map[string]string{
		handoffRoleAttr: handoffRoleValue,
		handoffUserAttr: cl.UserID.String(),
	})
	if err != nil {
		a.srv.writeErr(w, r, err)
		return
	}
	uid := cl.UserID
	if err := a.persist(ctx, call, domain.HandoffRequested, &uid); err != nil {
		a.srv.writeErr(w, r, err)
		return
	}
	a.publish(ctx, call)
	a.audit(r, "call.handoff", call.ID, map[string]any{"identity": identity})
	writeJSON(w, http.StatusOK, map[string]any{
		"token": token, "url": a.d.LiveKitURL, "roomName": call.RoomName, "identity": identity,
	})
}

func (a *handoffAPI) end(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cl := claimsOf(r)
	call, ok := a.loadCall(w, r)
	if !ok {
		return
	}
	switch call.Handoff {
	case domain.HandoffNone:
		a.srv.writeErr(w, r, errConflict("no operator handoff on this call"))
		return
	case domain.HandoffEnded:
		noContent(w)
		return
	}
	isAdmin := cl.Role == domain.RoleOwner || cl.Role == domain.RoleAdmin
	if call.OperatorID != nil && *call.OperatorID != cl.UserID && !isAdmin {
		auth.WriteError(w, http.StatusForbidden, "forbidden", "only the operator or an admin can end the handoff")
		return
	}
	if err := a.persist(ctx, call, domain.HandoffEnded, call.OperatorID); err != nil {
		a.srv.writeErr(w, r, err)
		return
	}
	a.publish(ctx, call)
	a.audit(r, "call.handoff_end", call.ID, nil)
	noContent(w)
}

func (a *handoffAPI) config(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(a.d.LiveKitURL) == "" {
		a.srv.writeErr(w, r, errNotConfigured("livekit"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": a.d.LiveKitURL})
}

// persist stores the handoff state and updates call in place.
func (a *handoffAPI) persist(ctx context.Context, call *domain.Call, state domain.HandoffState, operatorID *uuid.UUID) error {
	call.Handoff = state
	call.OperatorID = operatorID
	call.UpdatedAt = a.now()
	if a.d.SetHandoff != nil {
		return a.d.SetHandoff(ctx, call.ID, state, operatorID)
	}
	return a.d.Calls.UpdateCall(ctx, call)
}

func (a *handoffAPI) publish(ctx context.Context, call *domain.Call) {
	if a.d.Bus == nil {
		return
	}
	snap := *call
	id := call.ID
	payload := map[string]any{"call": &snap, "handoff": string(call.Handoff)}
	if call.OperatorID != nil {
		payload["operatorId"] = call.OperatorID.String()
	}
	a.d.Bus.Publish(ctx, domain.Event{
		ID: uuid.NewString(), Type: domain.EventCallUpdated, OrgID: call.OrgID, CallID: &id, At: a.now(), Payload: payload,
	})
}

func (a *handoffAPI) audit(r *http.Request, action string, callID uuid.UUID, meta map[string]any) {
	if a.d.Audit == nil {
		return
	}
	c := claimsOf(r)
	e := &domain.AuditEntry{
		ID: uuid.New(), OrgID: c.OrgID, Action: action, TargetType: "call", TargetID: callID.String(),
		Meta: meta, IP: recClientIP(r), At: a.now(),
	}
	if c.UserID != uuid.Nil {
		uid := c.UserID
		e.ActorID = &uid
	}
	if err := a.d.Audit.Append(r.Context(), e); err != nil {
		a.log.Warn().Err(err).Str("action", action).Msg("handoff: audit append failed")
	}
}
