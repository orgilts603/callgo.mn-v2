package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/objstore"
)

// RecordingService is the recording backend of the API (*recording.Service).
type RecordingService interface {
	// Call loads a call (for org scoping).
	Call(ctx context.Context, id uuid.UUID) (*domain.Call, error)
	// SignedURL returns a short-lived URL of a ready recording, or an
	// error wrapping domain.ErrNotFound.
	SignedURL(ctx context.Context, callID uuid.UUID) (string, time.Time, error)
	// Delete removes the recording (domain.ErrNotFound when there is none).
	Delete(ctx context.Context, callID uuid.UUID) error
}

// RecordingDeps are the collaborators of the recordings API
// (docs/API.md "Recordings").
type RecordingDeps struct {
	// Svc is required; nil answers "not configured".
	Svc RecordingService
	// LocalFiles serves files of the local object store
	// (objstore.ServeLocal(dir)); only needed with the local driver.
	// GET /api/recordings/file/* verifies the signature before delegating.
	LocalFiles http.Handler
	// LocalSecret is the HMAC key the local store signs URLs with
	// (objstore.LocalSigner). Default: Config.JWTSecret.
	LocalSecret []byte
	// HasFeature gates playback on the plan feature "recordings"
	// (403 feature_unavailable). nil = allowed. DELETE is never gated.
	HasFeature func(ctx context.Context, orgID uuid.UUID, feature string) (bool, error)
	// Audit records recording deletions; nil skips auditing.
	Audit interface {
		Append(ctx context.Context, e *domain.AuditEntry) error
	}
}

type recordingsAPI struct {
	d   RecordingDeps
	cfg Config
	log zerolog.Logger
	srv *server // error mapping only
}

// mountRecordings mounts
//
//	GET    /api/calls/{id}/recording       302 to a signed URL (auth)
//	GET    /api/calls/{id}/recording/url   {url, expiresAt} (auth)
//	DELETE /api/calls/{id}/recording       204 (owner/admin)
//	GET    /api/recordings/file/*          local driver files (signed URL, no auth)
//
// on the root router. The integrator must drop the legacy
// `r.Get("/{id}/recording", s.recording)` route from server.go.
func mountRecordings(r chi.Router, d RecordingDeps, cfg Config, log zerolog.Logger) {
	a := &recordingsAPI{d: d, cfg: cfg, log: log, srv: &server{log: log}}
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(cfg.JWTSecret))
		r.Get("/api/calls/{id}/recording", a.redirect)
		r.Get("/api/calls/{id}/recording/url", a.signedURL)
		r.With(auth.RequireRole(domain.RoleOwner, domain.RoleAdmin)).Delete("/api/calls/{id}/recording", a.delete)
	})
	r.Get(strings.TrimSuffix(objstore.LocalFilePrefix, "/")+"/*", a.localFile)
	r.Head(strings.TrimSuffix(objstore.LocalFilePrefix, "/")+"/*", a.localFile)
}

// load resolves the call of the request and checks org + feature.
func (a *recordingsAPI) load(w http.ResponseWriter, r *http.Request, gate bool) (uuid.UUID, bool) {
	if a.d.Svc == nil {
		a.srv.writeErr(w, r, errNotConfigured("recordings"))
		return uuid.Nil, false
	}
	id, err := urlID(r, "id")
	if err != nil {
		a.srv.writeErr(w, r, err)
		return uuid.Nil, false
	}
	orgID := claimsOf(r).OrgID
	c, err := a.d.Svc.Call(r.Context(), id)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && (c == nil || c.OrgID != orgID)) {
		a.srv.writeErr(w, r, errNotFound("call"))
		return uuid.Nil, false
	}
	if err != nil {
		a.srv.writeErr(w, r, err)
		return uuid.Nil, false
	}
	if gate && a.d.HasFeature != nil {
		ok, err := a.d.HasFeature(r.Context(), orgID, "recordings")
		if err != nil {
			a.srv.writeErr(w, r, err)
			return uuid.Nil, false
		}
		if !ok {
			auth.WriteError(w, http.StatusForbidden, "feature_unavailable", "your plan does not include recordings")
			return uuid.Nil, false
		}
	}
	return id, true
}

func (a *recordingsAPI) sign(w http.ResponseWriter, r *http.Request) (string, time.Time, bool) {
	id, ok := a.load(w, r, true)
	if !ok {
		return "", time.Time{}, false
	}
	u, exp, err := a.d.Svc.SignedURL(r.Context(), id)
	if errors.Is(err, domain.ErrNotFound) {
		a.srv.writeErr(w, r, errNotFound("recording"))
		return "", time.Time{}, false
	}
	if err != nil {
		a.srv.writeErr(w, r, err)
		return "", time.Time{}, false
	}
	return u, exp, true
}

func (a *recordingsAPI) redirect(w http.ResponseWriter, r *http.Request) {
	u, _, ok := a.sign(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, u, http.StatusFound)
}

func (a *recordingsAPI) signedURL(w http.ResponseWriter, r *http.Request) {
	u, exp, ok := a.sign(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"url": u, "expiresAt": exp.UTC()})
}

func (a *recordingsAPI) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := a.load(w, r, false)
	if !ok {
		return
	}
	if err := a.d.Svc.Delete(r.Context(), id); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			a.srv.writeErr(w, r, errNotFound("recording"))
			return
		}
		a.srv.writeErr(w, r, err)
		return
	}
	a.audit(r, "recording.delete", id)
	noContent(w)
}

func (a *recordingsAPI) audit(r *http.Request, action string, callID uuid.UUID) {
	if a.d.Audit == nil {
		return
	}
	c := claimsOf(r)
	e := &domain.AuditEntry{
		ID: uuid.New(), OrgID: c.OrgID, Action: action, TargetType: "call", TargetID: callID.String(),
		IP: recClientIP(r), At: time.Now().UTC(),
	}
	if c.UserID != uuid.Nil {
		uid := c.UserID
		e.ActorID = &uid
	}
	if err := a.d.Audit.Append(r.Context(), e); err != nil {
		a.log.Warn().Err(err).Str("action", action).Msg("recordings: audit append failed")
	}
}

// localFile serves a local-driver recording after verifying its signature.
func (a *recordingsAPI) localFile(w http.ResponseWriter, r *http.Request) {
	if a.d.LocalFiles == nil {
		auth.WriteError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	key := strings.TrimPrefix(r.URL.Path, objstore.LocalFilePrefix)
	secret := a.d.LocalSecret
	if len(secret) == 0 {
		secret = a.cfg.JWTSecret
	}
	q := r.URL.Query()
	if key == "" || key == r.URL.Path || !objstore.VerifyLocalSignature(secret, key, q.Get("exp"), q.Get("sig")) {
		auth.WriteError(w, http.StatusForbidden, "forbidden", "invalid or expired link")
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL = &url.URL{Path: "/" + key}
	r2.RequestURI = r2.URL.RequestURI()
	a.d.LocalFiles.ServeHTTP(w, r2)
}

func recClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
