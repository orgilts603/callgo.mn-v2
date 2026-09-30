package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/callbacks"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

const maxCallbackNote = 2000

// CallbackDeps are the collaborators of the /api/callbacks endpoints.
type CallbackDeps struct {
	// Sched validates and stores new callbacks (and publishes
	// callback.scheduled).
	Sched *callbacks.Scheduler
	Repo  domain.IntegrationsRepository
	// Audit is optional.
	Audit routingAuditor
	// Now is the clock (default time.Now); tests override it.
	Now func() time.Time
}

func (d *CallbackDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now().UTC()
}

// callbackDeleter is implemented by repositories that can hard-delete a
// callback; others get a soft delete (status "canceled").
type callbackDeleter interface {
	DeleteCallback(ctx context.Context, id uuid.UUID) error
}

// mountCallbacks registers, relative to the authenticated "/api" group:
//
//	GET    /callbacks?status=&limit=&offset=  → {items, total}
//	POST   /callbacks                         → 201 {callback}
//	PUT    /callbacks/{id}                    → {callback}  (dueAt?, note?, status?: "canceled"; pending only)
//	DELETE /callbacks/{id}                    → 204 (pending: canceled or deleted)
//
// The caller applies auth.RequireAuth.
func mountCallbacks(r chi.Router, d *CallbackDeps, _ Config, log zerolog.Logger) {
	h := &callbackHandler{d: d, log: log.With().Str("component", "httpapi.callbacks").Logger()}
	r.Route("/callbacks", func(r chi.Router) {
		r.Get("/", h.list)
		r.Post("/", h.create)
		r.Put("/{id}", h.update)
		r.Delete("/{id}", h.delete)
	})
}

type callbackHandler struct {
	d   *CallbackDeps
	log zerolog.Logger
}

func (h *callbackHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	writeFeatureErr(h.log, w, r, err)
}

func (h *callbackHandler) ready() error {
	if h.d == nil || h.d.Repo == nil || h.d.Sched == nil {
		return errNotConfigured("callbacks")
	}
	return nil
}

func (h *callbackHandler) load(ctx context.Context, orgID, id uuid.UUID) (*domain.CallbackRequest, error) {
	c, err := h.d.Repo.GetCallback(ctx, id)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && (c == nil || c.OrgID != orgID)) {
		return nil, errNotFound("callback")
	}
	if err != nil {
		return nil, fmt.Errorf("get callback: %w", err)
	}
	return c, nil
}

func (h *callbackHandler) list(w http.ResponseWriter, r *http.Request) {
	if err := h.ready(); err != nil {
		h.fail(w, r, err)
		return
	}
	status := domain.CallbackStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	switch status {
	case "", domain.CallbackPending, domain.CallbackDialed, domain.CallbackDone, domain.CallbackCanceled, domain.CallbackFailed:
	default:
		h.fail(w, r, errInvalid("status must be one of pending, dialed, done, canceled, failed"))
		return
	}
	limit, err := queryInt(r, "limit", 50, 1, 200)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	offset, err := queryInt(r, "offset", 0, 0, 1<<30)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items, total, err := h.d.Repo.ListCallbacks(r.Context(), claimsOf(r).OrgID, status, limit, offset)
	if err != nil {
		h.fail(w, r, fmt.Errorf("list callbacks: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, newList(items, total))
}

func (h *callbackHandler) create(w http.ResponseWriter, r *http.Request) {
	if err := h.ready(); err != nil {
		h.fail(w, r, err)
		return
	}
	var body struct {
		Phone          string     `json:"phone"`
		Name           string     `json:"name"`
		Note           string     `json:"note"`
		DueAt          *time.Time `json:"dueAt"`
		SIPNumberID    string     `json:"sipNumberId"`
		AgentProfileID string     `json:"agentProfileId"`
		ContactID      string     `json:"contactId"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		h.fail(w, r, err)
		return
	}
	if body.DueAt == nil || body.DueAt.IsZero() {
		h.fail(w, r, errInvalid("dueAt is required (RFC 3339)"))
		return
	}
	sipID, err := parseOptUUID(body.SIPNumberID, "sipNumberId")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	profID, err := parseOptUUID(body.AgentProfileID, "agentProfileId")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	contactID, err := parseOptUUID(body.ContactID, "contactId")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	claims := claimsOf(r)
	createdBy := claims.UserID
	c := &domain.CallbackRequest{
		OrgID: claims.OrgID, Phone: body.Phone, Name: body.Name, Note: body.Note, DueAt: body.DueAt.UTC(),
		SIPNumberID: sipID, AgentProfileID: profID, ContactID: contactID, CreatedBy: &createdBy,
	}
	if err := h.d.Sched.Create(r.Context(), c); err != nil {
		h.fail(w, r, err)
		return
	}
	featureAudit(r.Context(), h.log, h.d.Audit, r, claims, "callback.create", "callback", c.ID.String(), map[string]any{"phone": c.Phone, "dueAt": c.DueAt})
	writeJSON(w, http.StatusCreated, map[string]any{"callback": c})
}

func (h *callbackHandler) update(w http.ResponseWriter, r *http.Request) {
	if err := h.ready(); err != nil {
		h.fail(w, r, err)
		return
	}
	id, err := urlID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var body struct {
		DueAt  *time.Time `json:"dueAt"`
		Note   *string    `json:"note"`
		Status *string    `json:"status"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		h.fail(w, r, err)
		return
	}
	ctx := r.Context()
	claims := claimsOf(r)
	c, err := h.load(ctx, claims.OrgID, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if c.Status != domain.CallbackPending {
		h.fail(w, r, errConflict("callback is %s and can no longer be changed", c.Status))
		return
	}
	now := h.d.now()
	if body.DueAt != nil {
		if !body.DueAt.After(now) {
			h.fail(w, r, errInvalid("dueAt must be in the future"))
			return
		}
		c.DueAt = body.DueAt.UTC()
	}
	if body.Note != nil {
		note := strings.TrimSpace(*body.Note)
		if len(note) > maxCallbackNote {
			h.fail(w, r, errInvalid("note must be at most %d characters", maxCallbackNote))
			return
		}
		c.Note = note
	}
	if body.Status != nil {
		if *body.Status != string(domain.CallbackCanceled) {
			h.fail(w, r, errInvalid(`status may only be set to "canceled"`))
			return
		}
		c.Status = domain.CallbackCanceled
	}
	c.UpdatedAt = now
	if err := h.d.Repo.UpdateCallback(ctx, c); err != nil {
		h.fail(w, r, fmt.Errorf("update callback: %w", err))
		return
	}
	featureAudit(ctx, h.log, h.d.Audit, r, claims, "callback.update", "callback", c.ID.String(), map[string]any{"status": string(c.Status)})
	writeJSON(w, http.StatusOK, map[string]any{"callback": c})
}

func (h *callbackHandler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.ready(); err != nil {
		h.fail(w, r, err)
		return
	}
	id, err := urlID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ctx := r.Context()
	claims := claimsOf(r)
	c, err := h.load(ctx, claims.OrgID, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	switch c.Status {
	case domain.CallbackDialed:
		h.fail(w, r, errConflict("callback is being dialed"))
		return
	case domain.CallbackPending, domain.CallbackDone, domain.CallbackFailed, domain.CallbackCanceled:
	}
	if del, ok := h.d.Repo.(callbackDeleter); ok {
		if err := del.DeleteCallback(ctx, c.ID); err != nil {
			h.fail(w, r, fmt.Errorf("delete callback: %w", err))
			return
		}
	} else if c.Status == domain.CallbackPending {
		c.Status = domain.CallbackCanceled
		c.UpdatedAt = h.d.now()
		if err := h.d.Repo.UpdateCallback(ctx, c); err != nil {
			h.fail(w, r, fmt.Errorf("cancel callback: %w", err))
			return
		}
	}
	featureAudit(ctx, h.log, h.d.Audit, r, claims, "callback.delete", "callback", c.ID.String(), nil)
	noContent(w)
}
