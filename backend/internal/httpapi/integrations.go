package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/phone"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/sms"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/webhooks"
)

// IntegrationsAuditWriter records audit entries (identical in shape to the
// other features' audit writers, so the integrator can pass the same value).
type IntegrationsAuditWriter interface {
	Append(ctx context.Context, e *domain.AuditEntry) error
}

// IntegrationsOrgStore reads and writes organisations; SMS settings live in
// org.settings["sms"]. *crm / identity repositories satisfy it when they
// implement GetOrg and UpdateOrg.
type IntegrationsOrgStore interface {
	GetOrg(ctx context.Context, id uuid.UUID) (*domain.Organization, error)
	UpdateOrg(ctx context.Context, o *domain.Organization) error
}

// IntegrationsDeps are the collaborators of the integrations routes (webhooks
// and SMS). Callback routes are owned by the callbacks feature.
type IntegrationsDeps struct {
	Repo     domain.IntegrationsRepository
	Webhooks *webhooks.Dispatcher
	SMS      *sms.Service
	// Orgs loads/stores org.settings for the SMS config endpoints.
	Orgs IntegrationsOrgStore
	// Calls is optional; when set, POST /api/sms/send verifies that callId
	// belongs to the caller's org.
	Calls domain.CallRepository
	// HasFeature answers plan-feature checks (Entitlements.HasFeature). A nil
	// func allows every feature (development).
	HasFeature func(ctx context.Context, orgID uuid.UUID, feature string) (bool, error)
	// Audit is optional.
	Audit IntegrationsAuditWriter
}

type integrationsAPI struct {
	d   IntegrationsDeps
	log zerolog.Logger
	// srv provides the shared error mapping (writeErr).
	srv *server
}

// mountIntegrations registers the webhook and SMS routes of docs/API.md
// "Integrations" on r. r must be the authenticated /api group: paths are
// relative to /api and the claims of auth.RequireAuth are required. Routes:
//
//	GET|POST        /webhooks                       (owner/admin, feature webhooks)
//	PUT|DELETE      /webhooks/{id}
//	POST            /webhooks/{id}/test
//	POST            /webhooks/{id}/rotate-secret
//	GET             /webhooks/{id}/deliveries
//	POST            /webhook-deliveries/{id}/retry
//	GET|PUT         /sms/config                     (owner/admin, feature sms)
//	POST            /sms/send                       (feature sms)
//	GET             /sms                            (feature sms)
func mountIntegrations(r chi.Router, d IntegrationsDeps, cfg Config, log zerolog.Logger) {
	a := &integrationsAPI{d: d, log: log, srv: &server{cfg: cfg, log: log}}
	admin := auth.RequireRole(domain.RoleOwner, domain.RoleAdmin)

	r.Group(func(r chi.Router) {
		r.Use(admin, a.requireFeature("webhooks"))
		r.Get("/webhooks", a.listWebhooks)
		r.Post("/webhooks", a.createWebhook)
		r.Put("/webhooks/{id}", a.updateWebhook)
		r.Delete("/webhooks/{id}", a.deleteWebhook)
		r.Post("/webhooks/{id}/test", a.testWebhook)
		r.Post("/webhooks/{id}/rotate-secret", a.rotateWebhookSecret)
		r.Get("/webhooks/{id}/deliveries", a.listWebhookDeliveries)
		r.Post("/webhook-deliveries/{id}/retry", a.retryWebhookDelivery)
	})
	r.Group(func(r chi.Router) {
		r.Use(a.requireFeature("sms"))
		r.With(admin).Get("/sms/config", a.getSMSConfig)
		r.With(admin).Put("/sms/config", a.putSMSConfig)
		r.Post("/sms/send", a.sendSMS)
		r.Get("/sms", a.listSMS)
	})
}

func (a *integrationsAPI) fail(w http.ResponseWriter, r *http.Request, err error) {
	a.srv.writeErr(w, r, err)
}

// requireFeature answers 403 feature_unavailable when the org's plan lacks
// the feature.
func (a *integrationsAPI) requireFeature(feature string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if a.d.HasFeature != nil {
				ok, err := a.d.HasFeature(r.Context(), claimsOf(r).OrgID, feature)
				if err != nil {
					a.fail(w, r, err)
					return
				}
				if !ok {
					auth.WriteError(w, http.StatusForbidden, "feature_unavailable", "your plan does not include "+feature)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (a *integrationsAPI) audit(r *http.Request, action, targetType, targetID string, meta map[string]any) {
	if a.d.Audit == nil {
		return
	}
	c := claimsOf(r)
	actor := c.UserID
	ip := middleware.GetClientIP(r.Context())
	if ip == "" {
		ip = r.RemoteAddr
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			ip = host
		}
	}
	e := &domain.AuditEntry{
		ID: uuid.New(), OrgID: c.OrgID, ActorID: &actor, Action: action,
		TargetType: targetType, TargetID: targetID, Meta: meta, IP: ip,
	}
	// The request may already be cancelled by the time the client hangs up;
	// audit anyway.
	if err := a.d.Audit.Append(context.WithoutCancel(r.Context()), e); err != nil {
		a.log.Error().Err(err).Str("action", action).Msg("append audit entry")
	}
}

// ---------------------------------------------------------------------------
// webhooks
// ---------------------------------------------------------------------------

const (
	integMaxURLLen      = 2048
	integMaxDescription = 500
)

func (a *integrationsAPI) repo() (domain.IntegrationsRepository, error) {
	if a.d.Repo == nil {
		return nil, errNotConfigured("integrations")
	}
	return a.d.Repo, nil
}

// ownedWebhook loads the webhook of the URL id and hides other orgs' rows.
func (a *integrationsAPI) ownedWebhook(r *http.Request) (*domain.Webhook, error) {
	repo, err := a.repo()
	if err != nil {
		return nil, err
	}
	id, err := urlID(r, "id")
	if err != nil {
		return nil, err
	}
	w, err := repo.GetWebhook(r.Context(), id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, errNotFound("webhook")
		}
		return nil, err
	}
	if w.OrgID != claimsOf(r).OrgID {
		return nil, errNotFound("webhook")
	}
	return w, nil
}

func (a *integrationsAPI) listWebhooks(w http.ResponseWriter, r *http.Request) {
	repo, err := a.repo()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	items, err := repo.ListWebhooks(r.Context(), claimsOf(r).OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if items == nil {
		items = []domain.Webhook{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type integWebhookCreate struct {
	URL         string   `json:"url"`
	Events      []string `json:"events"`
	Description string   `json:"description"`
}

func (a *integrationsAPI) createWebhook(w http.ResponseWriter, r *http.Request) {
	repo, err := a.repo()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in integWebhookCreate
	if err := decodeJSON(w, r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	u, err := integValidateURL(in.URL)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	events, err := integValidateEvents(in.Events)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	desc, err := integValidateDescription(in.Description)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	secret, hint, err := webhooks.NewSecret()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	c := claimsOf(r)
	hook := &domain.Webhook{
		ID: uuid.New(), OrgID: c.OrgID, URL: u, Secret: secret, SecretHint: hint,
		Events: events, Active: true, Description: desc,
	}
	if err := repo.CreateWebhook(r.Context(), hook); err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "webhook.create", "webhook", hook.ID.String(), map[string]any{"url": u, "events": events})
	writeJSON(w, http.StatusCreated, map[string]any{"webhook": hook, "secret": secret})
}

type integWebhookUpdate struct {
	URL         *string   `json:"url"`
	Events      *[]string `json:"events"`
	Active      *bool     `json:"active"`
	Description *string   `json:"description"`
}

func (a *integrationsAPI) updateWebhook(w http.ResponseWriter, r *http.Request) {
	hook, err := a.ownedWebhook(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in integWebhookUpdate
	if err := decodeJSON(w, r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	changed := map[string]any{}
	if in.URL != nil {
		u, err := integValidateURL(*in.URL)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		hook.URL = u
		changed["url"] = u
	}
	if in.Events != nil {
		events, err := integValidateEvents(*in.Events)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		hook.Events = events
		changed["events"] = events
	}
	if in.Description != nil {
		desc, err := integValidateDescription(*in.Description)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		hook.Description = desc
		changed["description"] = desc
	}
	if in.Active != nil {
		if *in.Active && !hook.Active {
			hook.FailureCount = 0 // re-enabling starts with a clean slate
		}
		hook.Active = *in.Active
		changed["active"] = *in.Active
	}
	repo, _ := a.repo()
	if err := repo.UpdateWebhook(r.Context(), hook); err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "webhook.update", "webhook", hook.ID.String(), changed)
	writeJSON(w, http.StatusOK, map[string]any{"webhook": hook})
}

func (a *integrationsAPI) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	hook, err := a.ownedWebhook(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	repo, _ := a.repo()
	if err := repo.DeleteWebhook(r.Context(), hook.ID); err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "webhook.delete", "webhook", hook.ID.String(), map[string]any{"url": hook.URL})
	noContent(w)
}

func (a *integrationsAPI) testWebhook(w http.ResponseWriter, r *http.Request) {
	hook, err := a.ownedWebhook(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if a.d.Webhooks == nil {
		a.fail(w, r, errNotConfigured("webhook dispatcher"))
		return
	}
	dl, err := a.d.Webhooks.Test(r.Context(), hook.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "webhook.test", "webhook", hook.ID.String(), map[string]any{"status": dl.Status})
	writeJSON(w, http.StatusOK, map[string]any{"delivery": dl})
}

func (a *integrationsAPI) rotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	hook, err := a.ownedWebhook(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	secret, hint, err := webhooks.NewSecret()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	hook.Secret, hook.SecretHint = secret, hint
	repo, _ := a.repo()
	if err := repo.UpdateWebhook(r.Context(), hook); err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "webhook.rotate_secret", "webhook", hook.ID.String(), nil)
	writeJSON(w, http.StatusOK, map[string]any{"secret": secret})
}

func (a *integrationsAPI) listWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	hook, err := a.ownedWebhook(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	limit, err := queryInt(r, "limit", 50, 1, 200)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	offset, err := queryInt(r, "offset", 0, 0, 1_000_000)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	repo, _ := a.repo()
	items, total, err := repo.ListDeliveries(r.Context(), hook.ID, limit, offset)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newList(items, total))
}

func (a *integrationsAPI) retryWebhookDelivery(w http.ResponseWriter, r *http.Request) {
	if a.d.Webhooks == nil {
		a.fail(w, r, errNotConfigured("webhook dispatcher"))
		return
	}
	id, err := urlID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	dl, err := a.d.Webhooks.Retry(r.Context(), claimsOf(r).OrgID, id)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		a.fail(w, r, errNotFound("delivery"))
		return
	case errors.Is(err, webhooks.ErrWebhookInactive):
		a.fail(w, r, errConflict("webhook is disabled; re-enable it first"))
		return
	case err != nil:
		a.fail(w, r, err)
		return
	}
	a.audit(r, "webhook.delivery.retry", "webhook_delivery", dl.ID.String(), map[string]any{"webhookId": dl.WebhookID})
	writeJSON(w, http.StatusOK, map[string]any{"delivery": dl})
}

// integValidateURL requires an https URL (http only for loopback hosts) with
// no credentials and no literal private / link-local IP.
func integValidateURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errInvalid("url is required")
	}
	if len(raw) > integMaxURLLen {
		return "", errInvalid("url is longer than %d characters", integMaxURLLen)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "", errInvalid("url is not a valid URL")
	}
	if u.User != nil {
		return "", errInvalid("url must not contain credentials")
	}
	host := u.Hostname()
	loopback := host == "localhost" || strings.HasSuffix(host, ".localhost")
	if ip, err := netip.ParseAddr(host); err == nil {
		loopback = ip.IsLoopback()
		if !loopback && (ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast()) {
			return "", errInvalid("url must not point to a private address")
		}
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !loopback {
			return "", errInvalid("url must use https")
		}
	default:
		return "", errInvalid("url must use https")
	}
	return u.String(), nil
}

func integValidateEvents(events []string) ([]string, error) {
	out := make([]string, 0, len(events))
	seen := map[string]bool{}
	for _, e := range events {
		e = strings.TrimSpace(e)
		if seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	if err := webhooks.ValidateEvents(out); err != nil {
		return nil, errInvalid("%s", strings.TrimPrefix(err.Error(), domain.ErrInvalid.Error()+": "))
	}
	return out, nil
}

func integValidateDescription(s string) (string, error) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > integMaxDescription {
		return "", errInvalid("description is longer than %d characters", integMaxDescription)
	}
	return s, nil
}

// ---------------------------------------------------------------------------
// SMS
// ---------------------------------------------------------------------------

type integSMSConfigIn struct {
	Provider     string  `json:"provider"`
	URL          string  `json:"url"`
	APIKey       *string `json:"apiKey"`
	From         string  `json:"from"`
	BodyTemplate string  `json:"bodyTemplate"`
	Method       string  `json:"method"`
	AuthHeader   string  `json:"authHeader"`
}

func (a *integrationsAPI) smsDeps() error {
	if a.d.SMS == nil {
		return errNotConfigured("sms service")
	}
	if a.d.Orgs == nil {
		return errNotConfigured("organisation store")
	}
	return nil
}

func (a *integrationsAPI) getSMSConfig(w http.ResponseWriter, r *http.Request) {
	if err := a.smsDeps(); err != nil {
		a.fail(w, r, err)
		return
	}
	org, err := a.d.Orgs.GetOrg(r.Context(), claimsOf(r).OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, a.d.SMS.Config(org.Settings))
}

func (a *integrationsAPI) putSMSConfig(w http.ResponseWriter, r *http.Request) {
	if err := a.smsDeps(); err != nil {
		a.fail(w, r, err)
		return
	}
	var in integSMSConfigIn
	if err := decodeJSON(w, r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	org, err := a.d.Orgs.GetOrg(r.Context(), claimsOf(r).OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	frag, err := a.d.SMS.SetConfig(org.Settings, sms.ConfigInput{
		Provider: in.Provider, URL: in.URL, APIKey: in.APIKey, From: in.From,
		Method: in.Method, BodyTemplate: in.BodyTemplate, AuthHeader: in.AuthHeader,
	})
	if err != nil {
		if errors.Is(err, domain.ErrInvalid) {
			a.fail(w, r, errInvalid("%s", strings.TrimPrefix(err.Error(), domain.ErrInvalid.Error()+": ")))
			return
		}
		a.fail(w, r, err)
		return
	}
	settings := make(map[string]any, len(org.Settings)+1)
	for k, v := range org.Settings {
		settings[k] = v
	}
	for k, v := range frag {
		settings[k] = v
	}
	org.Settings = settings
	if err := a.d.Orgs.UpdateOrg(r.Context(), org); err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "sms.config.update", "org", org.ID.String(), map[string]any{"provider": in.Provider, "apiKeyChanged": in.APIKey != nil})
	writeJSON(w, http.StatusOK, a.d.SMS.Config(org.Settings))
}

type integSMSSendIn struct {
	To     string `json:"to"`
	Body   string `json:"body"`
	CallID string `json:"callId"`
}

func (a *integrationsAPI) sendSMS(w http.ResponseWriter, r *http.Request) {
	if a.d.SMS == nil {
		a.fail(w, r, errNotConfigured("sms service"))
		return
	}
	var in integSMSSendIn
	if err := decodeJSON(w, r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	callID, err := parseOptUUID(in.CallID, "callId")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	orgID := claimsOf(r).OrgID
	if callID != nil && a.d.Calls != nil {
		c, err := a.d.Calls.GetCall(r.Context(), *callID)
		if err != nil || c.OrgID != orgID {
			if err == nil || errors.Is(err, domain.ErrNotFound) {
				a.fail(w, r, errInvalid("callId does not exist"))
			} else {
				a.fail(w, r, err)
			}
			return
		}
	}
	msg, err := a.d.SMS.Send(r.Context(), orgID, callID, in.To, in.Body)
	switch {
	case errors.Is(err, sms.ErrFeatureUnavailable):
		auth.WriteError(w, http.StatusForbidden, "feature_unavailable", "your plan does not include sms")
		return
	case errors.Is(err, sms.ErrNotConfigured):
		a.fail(w, r, errConflict("sms gateway is not configured"))
		return
	case errors.Is(err, sms.ErrSendFailed):
		a.log.Warn().Err(err).Str("org", orgID.String()).Msg("sms gateway failure")
		a.fail(w, r, &apiError{http.StatusBadGateway, "bad_gateway", "the sms gateway rejected the message"})
		return
	case errors.Is(err, domain.ErrInvalid):
		a.fail(w, r, errInvalid("%s", strings.TrimPrefix(err.Error(), domain.ErrInvalid.Error()+": ")))
		return
	case err != nil:
		a.fail(w, r, err)
		return
	}
	a.audit(r, "sms.send", "sms", msg.ID.String(), map[string]any{"to": phone.Mask(msg.To)})
	writeJSON(w, http.StatusCreated, map[string]any{"message": msg})
}

func (a *integrationsAPI) listSMS(w http.ResponseWriter, r *http.Request) {
	repo, err := a.repo()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	limit, err := queryInt(r, "limit", 50, 1, 200)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	offset, err := queryInt(r, "offset", 0, 0, 1_000_000)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	items, total, err := repo.ListSMS(r.Context(), claimsOf(r).OrgID, limit, offset)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newList(items, total))
}
