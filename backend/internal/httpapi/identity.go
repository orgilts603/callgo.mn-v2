package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/identity"
	cgmw "github.com/orgilts603/callgo.mn-v2/backend/internal/middleware"
)

// IdentityDeps wires the identity endpoints (docs/API.md "Identity").
type IdentityDeps struct {
	// Svc is the identity service (required). It also resolves API keys on
	// the authenticated identity routes.
	Svc *identity.Service
	// HasFeature gates the API-key endpoints on plan feature "api" (403
	// feature_unavailable). Nil allows them.
	HasFeature func(ctx context.Context, orgID uuid.UUID, feature string) (bool, error)
	// AuthRateLimit replaces the default per-IP limiter (5 requests/min/IP,
	// one bucket set per route) on signup, login, resend-verification and
	// forgot-password. Mainly for tests.
	AuthRateLimit func(http.Handler) http.Handler
}

// identityRateRPS is 5 requests per minute.
const identityRateRPS = 5.0 / 60.0

type identityAPI struct {
	d   IdentityDeps
	cfg Config
	log zerolog.Logger
}

// mountIdentity mounts the identity routes on r, which must be the "/api"
// subrouter (paths below are relative to /api) WITHOUT auth middleware:
// public routes are mounted as-is and the others get their own
// auth.RequireAuth (JWT; API keys are recognised and refused with 403).
// It also serves POST /auth/login (with refreshToken + subscription),
// replacing the legacy server.login.
func mountIdentity(r chi.Router, d IdentityDeps, cfg Config, log zerolog.Logger) {
	a := &identityAPI{d: d, cfg: cfg, log: log.With().Str("component", "identity-api").Logger()}
	if d.Svc == nil {
		a.log.Error().Msg("identity service not configured; identity routes not mounted")
		return
	}
	limit := func() func(http.Handler) http.Handler {
		if d.AuthRateLimit != nil {
			return d.AuthRateLimit
		}
		return cgmw.RateLimit(identityRateRPS, 5)
	}

	// Public.
	r.With(limit()).Post("/auth/signup", a.signup)
	r.With(limit()).Post("/auth/login", a.loginHandler)
	r.With(limit()).Post("/auth/forgot-password", a.forgotPassword)
	r.With(limit()).Post("/auth/resend-verification", a.resendVerification)
	r.Post("/auth/refresh", a.refresh)
	r.Post("/auth/logout", a.logout)
	r.Post("/auth/verify-email", a.verifyEmail)
	r.Post("/auth/reset-password", a.resetPassword)
	r.Post("/auth/accept-invitation", a.acceptInvitation)

	// Authenticated users (not API keys).
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(cfg.JWTSecret, auth.WithAPIKeys(d.Svc)), auth.RequireUser)
		r.Post("/auth/change-password", a.changePassword)
		r.Get("/auth/sessions", a.listSessions)
		r.Delete("/auth/sessions/{id}", a.revokeSession)
		r.Get("/org", a.getOrg)

		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(domain.RoleOwner, domain.RoleAdmin))
			r.Put("/org", a.updateOrg)
			r.Get("/org/members", a.listMembers)
			r.Put("/org/members/{userId}", a.updateMember)
			r.Delete("/org/members/{userId}", a.removeMember)
			r.Post("/org/invitations", a.createInvitation)
			r.Delete("/org/invitations/{id}", a.deleteInvitation)
			r.With(a.requireFeature(identity.FeatureAPI)).Get("/org/api-keys", a.listAPIKeys)
			r.With(a.requireFeature(identity.FeatureAPI)).Post("/org/api-keys", a.createAPIKey)
			// Revocation stays possible after a downgrade.
			r.Delete("/org/api-keys/{id}", a.revokeAPIKey)
			r.Get("/org/audit", a.listAudit)
		})
	})
}

// ---- helpers ----

func identityClientIP(r *http.Request) string {
	if ip := chimw.GetClientIP(r.Context()); ip != "" {
		return ip
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func identityClient(r *http.Request) identity.Client {
	return identity.Client{IP: identityClientIP(r), UserAgent: r.UserAgent()}
}

func identityActor(r *http.Request) identity.Actor {
	return identity.ActorFromClaims(claimsOf(r))
}

// fail maps service errors onto the API error envelope.
func (a *identityAPI) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	var qe *identity.QuotaError
	msg, public := identity.PublicMessage(err)
	pick := func(def string) string {
		if public {
			return msg
		}
		return def
	}
	switch {
	case errors.As(err, &ae):
		auth.WriteError(w, ae.status, ae.code, ae.message)
	case errors.As(err, &qe):
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": map[string]any{
			"code": "quota_exceeded", "message": qe.Error(),
			"details": map[string]any{"resource": qe.Resource, "limit": qe.Limit, "used": qe.Used},
		}})
	case errors.Is(err, domain.ErrInvalid):
		auth.WriteError(w, http.StatusBadRequest, "invalid", pick("invalid input"))
	case errors.Is(err, domain.ErrNotFound):
		auth.WriteError(w, http.StatusNotFound, "not_found", pick("not found"))
	case errors.Is(err, domain.ErrConflict):
		auth.WriteError(w, http.StatusConflict, "conflict", pick("conflict"))
	case errors.Is(err, domain.ErrUnauthorized):
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", pick("unauthorized"))
	case errors.Is(err, auth.ErrFeatureUnavailable):
		auth.WriteError(w, http.StatusForbidden, "feature_unavailable", "your plan does not include this feature")
	case errors.Is(err, domain.ErrForbidden):
		auth.WriteError(w, http.StatusForbidden, "forbidden", pick("forbidden"))
	default:
		a.log.Error().Err(err).Str("method", r.Method).Str("path", r.URL.Path).
			Str("reqId", chimw.GetReqID(r.Context())).Msg("request failed")
		auth.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

// audit records a mutation by the request's actor; failures are logged only.
func (a *identityAPI) audit(r *http.Request, action, targetType, targetID string, meta map[string]any) {
	c := claimsOf(r)
	_ = a.d.Svc.Audit(r.Context(), c.OrgID, identity.ActorFromClaims(c), action, targetType, targetID, meta, identityClientIP(r))
}

func (a *identityAPI) requireFeature(feature string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if a.d.HasFeature != nil {
				ok, err := a.d.HasFeature(r.Context(), claimsOf(r).OrgID, feature)
				if err != nil {
					a.fail(w, r, err)
					return
				}
				if !ok {
					auth.WriteError(w, http.StatusForbidden, "feature_unavailable", "your plan does not include the "+feature+" feature")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---- public auth ----

func (a *identityAPI) signup(w http.ResponseWriter, r *http.Request) {
	var in identity.SignupInput
	if err := decodeJSON(w, r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	res, err := a.d.Svc.Signup(r.Context(), in, identityClient(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

// loginHandler serves POST /api/auth/login → {token, refreshToken, user, org, subscription}.
func (a *identityAPI) loginHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	res, err := a.d.Svc.Login(r.Context(), req.Email, req.Password, identityClient(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type identityRefreshBody struct {
	RefreshToken string `json:"refreshToken"`
}

func (a *identityAPI) refresh(w http.ResponseWriter, r *http.Request) {
	var req identityRefreshBody
	if err := decodeJSON(w, r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	if strings.TrimSpace(req.RefreshToken) == "" {
		a.fail(w, r, errInvalid("refreshToken is required"))
		return
	}
	res, err := a.d.Svc.Refresh(r.Context(), req.RefreshToken, identityClient(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *identityAPI) logout(w http.ResponseWriter, r *http.Request) {
	var req identityRefreshBody
	if err := decodeOptionalJSON(w, r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.d.Svc.Logout(r.Context(), req.RefreshToken); err != nil {
		a.fail(w, r, err)
		return
	}
	noContent(w)
}

func (a *identityAPI) verifyEmail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	u, err := a.d.Svc.VerifyEmail(r.Context(), req.Token)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	_ = a.d.Svc.Audit(r.Context(), u.OrgID, identity.Actor{UserID: u.ID, Email: u.Email}, "user.verify_email", "user", u.ID.String(), nil, identityClientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

// resendVerification works for a signed-in user (Bearer JWT) or by email;
// it always answers 204.
func (a *identityAPI) resendVerification(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := decodeOptionalJSON(w, r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	var err error
	if tok := auth.TokenFromRequest(r); tok != "" && !strings.HasPrefix(tok, auth.APIKeyPrefix) {
		if c, perr := auth.ParseToken(a.cfg.JWTSecret, tok); perr == nil {
			err = a.d.Svc.ResendVerificationFor(r.Context(), c.UserID)
		} else if req.Email != "" {
			err = a.d.Svc.ResendVerification(r.Context(), req.Email)
		}
	} else if req.Email != "" {
		err = a.d.Svc.ResendVerification(r.Context(), req.Email)
	}
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		a.log.Error().Err(err).Msg("resend verification")
	}
	noContent(w)
}

func (a *identityAPI) forgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := decodeOptionalJSON(w, r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	// Detach from the request so a client disconnect cannot reveal timing.
	a.d.Svc.ForgotPassword(context.WithoutCancel(r.Context()), req.Email)
	noContent(w)
}

func (a *identityAPI) resetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	u, err := a.d.Svc.ResetPassword(r.Context(), req.Token, req.Password)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	_ = a.d.Svc.Audit(r.Context(), u.OrgID, identity.Actor{UserID: u.ID, Email: u.Email}, "auth.password_reset", "user", u.ID.String(), nil, identityClientIP(r))
	noContent(w)
}

func (a *identityAPI) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	var in identity.AcceptInvitationInput
	if err := decodeJSON(w, r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	res, err := a.d.Svc.AcceptInvitation(r.Context(), in, identityClient(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---- signed-in user ----

func (a *identityAPI) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
		// RefreshToken (optional) keeps the caller's own session alive.
		RefreshToken string `json:"refreshToken"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	c := claimsOf(r)
	if err := a.d.Svc.ChangePassword(r.Context(), c.UserID, req.CurrentPassword, req.NewPassword, req.RefreshToken); err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "auth.password_change", "user", c.UserID.String(), nil)
	noContent(w)
}

func (a *identityAPI) listSessions(w http.ResponseWriter, r *http.Request) {
	items, err := a.d.Svc.ListSessions(r.Context(), claimsOf(r).UserID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newList(items, len(items)))
}

func (a *identityAPI) revokeSession(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.d.Svc.RevokeSession(r.Context(), claimsOf(r).UserID, id); err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "session.revoke", "session", id.String(), nil)
	noContent(w)
}

// ---- organisation ----

func (a *identityAPI) getOrg(w http.ResponseWriter, r *http.Request) {
	ov, err := a.d.Svc.GetOrg(r.Context(), claimsOf(r).OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ov)
}

func (a *identityAPI) updateOrg(w http.ResponseWriter, r *http.Request) {
	var upd identity.OrgUpdate
	if err := decodeJSON(w, r, &upd); err != nil {
		a.fail(w, r, err)
		return
	}
	org, err := a.d.Svc.UpdateOrg(r.Context(), identityActor(r), upd)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	meta := map[string]any{}
	if upd.Name != nil {
		meta["name"] = *upd.Name
	}
	if upd.Timezone != nil {
		meta["timezone"] = *upd.Timezone
	}
	if len(upd.Settings) > 0 {
		keys := make([]string, 0, len(upd.Settings))
		for k := range upd.Settings {
			keys = append(keys, k)
		}
		meta["settings"] = keys
	}
	a.audit(r, "org.update", "organization", org.ID.String(), meta)
	writeJSON(w, http.StatusOK, map[string]any{"org": org})
}

func (a *identityAPI) listMembers(w http.ResponseWriter, r *http.Request) {
	users, invs, err := a.d.Svc.ListMembers(r.Context(), claimsOf(r).OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if users == nil {
		users = []domain.User{}
	}
	if invs == nil {
		invs = []domain.Invitation{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": users, "total": len(users), "invitations": invs})
}

func (a *identityAPI) updateMember(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "userId")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var upd identity.MemberUpdate
	if err := decodeJSON(w, r, &upd); err != nil {
		a.fail(w, r, err)
		return
	}
	if upd.Role == nil && upd.Status == nil {
		a.fail(w, r, errInvalid("role or status is required"))
		return
	}
	u, err := a.d.Svc.UpdateMember(r.Context(), identityActor(r), id, upd)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	meta := map[string]any{"email": u.Email}
	if upd.Role != nil {
		meta["role"] = string(*upd.Role)
	}
	if upd.Status != nil {
		meta["status"] = string(*upd.Status)
	}
	a.audit(r, "member.update", "user", u.ID.String(), meta)
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

func (a *identityAPI) removeMember(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "userId")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	u, err := a.d.Svc.RemoveMember(r.Context(), identityActor(r), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "member.remove", "user", u.ID.String(), map[string]any{"email": u.Email, "role": string(u.Role)})
	noContent(w)
}

func (a *identityAPI) createInvitation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string      `json:"email"`
		Role  domain.Role `json:"role"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	inv, err := a.d.Svc.Invite(r.Context(), identityActor(r), req.Email, req.Role)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "invitation.create", "invitation", inv.ID.String(), map[string]any{"email": inv.Email, "role": string(inv.Role)})
	writeJSON(w, http.StatusCreated, map[string]any{"invitation": inv})
}

func (a *identityAPI) deleteInvitation(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	inv, err := a.d.Svc.CancelInvitation(r.Context(), claimsOf(r).OrgID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "invitation.delete", "invitation", inv.ID.String(), map[string]any{"email": inv.Email})
	noContent(w)
}

// ---- API keys ----

func (a *identityAPI) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := a.d.Svc.ListAPIKeys(r.Context(), claimsOf(r).OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newList(keys, len(keys)))
}

func (a *identityAPI) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	k, plain, err := a.d.Svc.CreateAPIKey(r.Context(), identityActor(r), req.Name, req.Scopes)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "api_key.create", "api_key", k.ID.String(), map[string]any{"name": k.Name, "prefix": k.Prefix, "scopes": k.Scopes})
	writeJSON(w, http.StatusCreated, map[string]any{"apiKey": k, "plaintext": plain})
}

func (a *identityAPI) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	k, err := a.d.Svc.RevokeAPIKey(r.Context(), claimsOf(r).OrgID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "api_key.revoke", "api_key", k.ID.String(), map[string]any{"name": k.Name, "prefix": k.Prefix})
	noContent(w)
}

// ---- audit ----

func (a *identityAPI) listAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var f domain.AuditFilter
	var err error
	if f.ActorID, err = parseOptUUID(q.Get("actorId"), "actorId"); err != nil {
		a.fail(w, r, err)
		return
	}
	f.Action = strings.TrimSpace(q.Get("action"))
	for _, p := range []struct {
		name string
		dst  **time.Time
	}{{"from", &f.From}, {"to", &f.To}} {
		v := strings.TrimSpace(q.Get(p.name))
		if v == "" {
			continue
		}
		t, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			a.fail(w, r, errInvalid("%s must be an RFC 3339 timestamp", p.name))
			return
		}
		*p.dst = &t
	}
	if f.Limit, err = queryInt(r, "limit", 50, 1, 500); err != nil {
		a.fail(w, r, err)
		return
	}
	if f.Offset, err = queryInt(r, "offset", 0, 0, 1_000_000); err != nil {
		a.fail(w, r, err)
		return
	}
	items, total, err := a.d.Svc.ListAudit(r.Context(), claimsOf(r).OrgID, f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newList(items, total))
}
