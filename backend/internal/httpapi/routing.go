package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/routing"
)

// routingAuditor appends audit entries (satisfied by the integrator's
// Deps.Audit writer). A nil auditor disables auditing.
type routingAuditor interface {
	Append(ctx context.Context, e *domain.AuditEntry) error
}

// RoutingDeps are the collaborators of the inbound-routing endpoints.
type RoutingDeps struct {
	Numbers  domain.SIPNumberRepository
	Profiles domain.AgentProfileRepository
	// Orgs supplies the organisation time zone for business hours.
	Orgs domain.OrgRepository
	// Audit is optional.
	Audit routingAuditor
	// Now is the clock (default time.Now); tests override it.
	Now func() time.Time
}

func (d *RoutingDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now().UTC()
}

// mountRouting registers, relative to the authenticated "/api" group:
//
//	POST /sip-numbers/{id}/routing/resolve?at=<rfc3339>  → {route}
//	PUT  /sip-numbers/{id}/routing (owner/admin)          → {sipNumber}
//
// Full paths are registered on r so they coexist with the existing
// r.Route("/sip-numbers", …) subrouter. The caller applies auth.RequireAuth.
func mountRouting(r chi.Router, d *RoutingDeps, _ Config, log zerolog.Logger) {
	h := &routingHandler{d: d, log: log.With().Str("component", "httpapi.routing").Logger()}
	admin := auth.RequireRole(domain.RoleOwner, domain.RoleAdmin)
	r.Post("/sip-numbers/{id}/routing/resolve", h.resolve)
	r.With(admin).Put("/sip-numbers/{id}/routing", h.put)
}

type routingHandler struct {
	d   *RoutingDeps
	log zerolog.Logger
}

func (h *routingHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	writeFeatureErr(h.log, w, r, err)
}

// ValidateRoutingBody validates a RoutingConfig received on a SIP number
// create/update request: rules of routing.Validate with the organisation's
// agent profiles as the existence check. It returns the config with defaults
// applied (menu timeout 8s, repeat 1) to store on SIPNumber.Routing. The
// error is a 400 "invalid" API error, so handlers can pass it to writeErr.
//
// Integration: add `Routing *domain.RoutingConfig `json:"routing"“ to
// sipNumberBody and, when non-nil, set n.Routing to the returned config.
func ValidateRoutingBody(ctx context.Context, profiles domain.AgentProfileRepository, orgID uuid.UUID, cfg domain.RoutingConfig) (domain.RoutingConfig, error) {
	cfg = routing.Normalize(cfg)
	exists, err := routing.ProfileSet(ctx, profiles, orgID)
	if err != nil {
		return cfg, err
	}
	if err := routing.Validate(cfg, exists); err != nil {
		return cfg, errInvalid("%s", featureMessage(err))
	}
	return cfg, nil
}

func (h *routingHandler) loadNumber(ctx context.Context, orgID, id uuid.UUID) (*domain.SIPNumber, error) {
	n, err := h.d.Numbers.GetSIPNumber(ctx, id)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && (n == nil || n.OrgID != orgID)) {
		return nil, errNotFound("SIP number")
	}
	if err != nil {
		return nil, fmt.Errorf("get sip number: %w", err)
	}
	return n, nil
}

func (h *routingHandler) resolve(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	at := h.d.now()
	if v := strings.TrimSpace(r.URL.Query().Get("at")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			h.fail(w, r, errInvalid("at must be an RFC 3339 timestamp"))
			return
		}
		at = t
	}
	ctx := r.Context()
	orgID := claimsOf(r).OrgID
	n, err := h.loadNumber(ctx, orgID, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	tzName := ""
	if h.d.Orgs != nil {
		org, err := h.d.Orgs.GetOrg(ctx, orgID)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			h.fail(w, r, fmt.Errorf("get org: %w", err))
			return
		}
		if org != nil {
			tzName = org.Timezone
		}
	}
	route := routing.Resolve(*n, at, routing.Location(tzName))
	writeJSON(w, http.StatusOK, map[string]any{"route": route})
}

// put replaces only SIPNumber.Routing (body: a RoutingConfig; `{}` clears it).
func (h *routingHandler) put(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var cfg domain.RoutingConfig
	if err := decodeJSON(w, r, &cfg); err != nil {
		h.fail(w, r, err)
		return
	}
	ctx := r.Context()
	claims := claimsOf(r)
	n, err := h.loadNumber(ctx, claims.OrgID, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	cfg, err = ValidateRoutingBody(ctx, h.d.Profiles, claims.OrgID, cfg)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	n.Routing = cfg
	n.UpdatedAt = h.d.now()
	if err := h.d.Numbers.UpdateSIPNumber(ctx, n); err != nil {
		h.fail(w, r, fmt.Errorf("update sip number routing: %w", err))
		return
	}
	featureAudit(ctx, h.log, h.d.Audit, r, claims, "sip_number.routing.update", "sip_number", n.ID.String(), map[string]any{
		"menuOptions": len(cfg.Menu), "hasBusinessHours": !cfg.BusinessHours.IsZero(),
	})
	writeJSON(w, http.StatusOK, map[string]any{"sipNumber": n})
}

// featureMessage is the public text of a domain.ErrInvalid error without the
// trailing sentinel text.
func featureMessage(err error) string {
	return strings.TrimSuffix(err.Error(), ": "+domain.ErrInvalid.Error())
}

// writeFeatureErr is the error mapping of the routing and callback handlers
// (same envelope as server.writeErr, without needing the server).
func writeFeatureErr(log zerolog.Logger, w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
		auth.WriteError(w, ae.status, ae.code, ae.message)
	case errors.Is(err, domain.ErrNotFound):
		auth.WriteError(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, domain.ErrConflict):
		auth.WriteError(w, http.StatusConflict, "conflict", "conflict")
	case errors.Is(err, domain.ErrInvalid):
		auth.WriteError(w, http.StatusBadRequest, "invalid", featureMessage(err))
	case errors.Is(err, domain.ErrForbidden):
		auth.WriteError(w, http.StatusForbidden, "forbidden", "forbidden")
	default:
		log.Error().Err(err).Str("method", r.Method).Str("path", r.URL.Path).
			Str("reqId", middleware.GetReqID(r.Context())).Msg("request failed")
		auth.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

// featureAudit appends an audit entry; failures are logged, never returned.
func featureAudit(ctx context.Context, log zerolog.Logger, a routingAuditor, r *http.Request, c auth.Claims, action, targetType, targetID string, meta map[string]any) {
	if a == nil {
		return
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	actor := c.UserID
	e := &domain.AuditEntry{
		ID: uuid.New(), OrgID: c.OrgID, ActorID: &actor, Action: action,
		TargetType: targetType, TargetID: targetID, Meta: meta, IP: ip, At: time.Now().UTC(),
	}
	if err := a.Append(ctx, e); err != nil {
		log.Warn().Err(err).Str("action", action).Msg("append audit entry")
	}
}
