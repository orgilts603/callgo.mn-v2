// Package postcall runs an agent profile's post-call actions (SMS, webhook,
// callback) when a call ends.
package postcall

import (
	"bytes"
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// MetaDone is the call.Metadata key set once the actions ran.
const MetaDone = "postCallDone"

// MetaCallbacks is the call.Metadata key holding LLM-requested callbacks:
// [{"dueAt": "<rfc3339>", "note": "..."}].
const MetaCallbacks = "callbacks"

// MetaVars is the call.Metadata key holding template variables ({{vars.X}}).
const MetaVars = "vars"

// SMSSender sends a text on behalf of an org (implemented by *sms.Service).
type SMSSender interface {
	Send(ctx context.Context, orgID uuid.UUID, callID *uuid.UUID, to, body string) (*domain.SMSMessage, error)
}

// WebhookEnqueuer queues an event for one webhook (implemented by
// *webhooks.Dispatcher).
type WebhookEnqueuer interface {
	EnqueueFor(ctx context.Context, webhookID uuid.UUID, ev domain.Event) error
}

// CallbackCreator schedules a callback (implemented by the callbacks service).
type CallbackCreator interface {
	CreateCallback(ctx context.Context, c *domain.CallbackRequest) error
}

// Runner executes post-call actions.
type Runner struct {
	profiles  domain.AgentProfileRepository
	contacts  domain.ContactRepository
	campaigns domain.CampaignRepository
	sms       SMSSender
	webhooks  WebhookEnqueuer
	callbacks CallbackCreator
	log       zerolog.Logger

	markDone func(ctx context.Context, callID uuid.UUID) error
	now      func() time.Time
	seen     *lru
}

// Option customises a Runner.
type Option func(*Runner)

// WithMarkDone persists "postCallDone" on the call (sets
// call.Metadata["postCallDone"]=true) after the actions ran, making the
// runner idempotent across restarts.
func WithMarkDone(f func(ctx context.Context, callID uuid.UUID) error) Option {
	return func(r *Runner) { r.markDone = f }
}

// WithClock overrides time.Now (tests).
func WithClock(now func() time.Time) Option { return func(r *Runner) { r.now = now } }

// WithCacheSize sets how many processed call ids are remembered in memory
// (default 4096).
func WithCacheSize(n int) Option { return func(r *Runner) { r.seen = newLRU(n) } }

// New builds a Runner. sms, webhooks and callbacks may be nil; the matching
// actions are then skipped with a warning. campaigns may be nil.
func New(profiles domain.AgentProfileRepository, contacts domain.ContactRepository, campaigns domain.CampaignRepository,
	sms SMSSender, webhooks WebhookEnqueuer, callbacks CallbackCreator, log zerolog.Logger, opts ...Option) *Runner {
	r := &Runner{
		profiles: profiles, contacts: contacts, campaigns: campaigns, sms: sms, webhooks: webhooks, callbacks: callbacks,
		log: log.With().Str("component", "postcall").Logger(), now: time.Now, seen: newLRU(4096),
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// OnCallEnded runs the matching actions of the call's agent profile. It is
// at-most-once per call: a call already marked done (metadata or in-memory
// cache) is skipped, and a failing action is reported in the returned error
// but never retried. Errors of several actions are joined.
func (r *Runner) OnCallEnded(ctx context.Context, call *domain.Call) error {
	if call == nil || call.AgentProfileID == nil {
		return nil
	}
	if done, _ := call.Metadata[MetaDone].(bool); done {
		return nil
	}
	profile, err := r.profiles.GetAgentProfile(ctx, *call.AgentProfileID)
	if err != nil {
		return fmt.Errorf("load agent profile %s: %w", *call.AgentProfileID, err)
	}
	actions := matching(profile.PostCallActions, call.Outcome)
	if len(actions) == 0 {
		return nil
	}
	if !r.seen.add(call.ID) {
		return nil // already processed (or in flight) in this process
	}

	env := r.buildEnv(ctx, call)
	var errs []error
	for i, a := range actions {
		var err error
		switch a.Type {
		case domain.ActionSMS:
			err = r.runSMS(ctx, call, env, a)
		case domain.ActionWebhook:
			err = r.runWebhook(ctx, call, env, a)
		case domain.ActionCallback:
			err = r.runCallback(ctx, call, env, a)
		default:
			err = fmt.Errorf("unknown action type %q", a.Type)
		}
		if err != nil {
			r.log.Error().Err(err).Str("call", call.ID.String()).Str("action", string(a.Type)).Int("index", i).Msg("post-call action failed")
			errs = append(errs, fmt.Errorf("action %d (%s): %w", i, a.Type, err))
		}
	}
	if r.markDone != nil {
		if err := r.markDone(ctx, call.ID); err != nil {
			errs = append(errs, fmt.Errorf("mark call done: %w", err))
		}
	}
	return errors.Join(errs...)
}

func matching(actions []domain.PostCallAction, outcome string) []domain.PostCallAction {
	var out []domain.PostCallAction
	for _, a := range actions {
		if len(a.Outcomes) == 0 || slices.Contains(a.Outcomes, outcome) {
			out = append(out, a)
		}
	}
	return out
}

// env is everything the actions need to know about the call.
type env struct {
	phone    string
	contact  *domain.Contact
	campaign *domain.Campaign
	name     string
	vars     map[string]string // template data (flat: "name", "vars.X", ...)
}

// CustomerNumber is the customer's phone: the callee of an outbound call, the
// caller of an inbound one.
func CustomerNumber(c *domain.Call) string {
	if c.Direction == domain.DirectionOutbound {
		return c.ToNumber
	}
	return c.FromNumber
}

func (r *Runner) buildEnv(ctx context.Context, call *domain.Call) *env {
	e := &env{phone: CustomerNumber(call)}
	if r.contacts != nil {
		if call.ContactID != nil {
			if c, err := r.contacts.GetContact(ctx, *call.ContactID); err == nil {
				e.contact = c
			} else if !errors.Is(err, domain.ErrNotFound) {
				r.log.Warn().Err(err).Str("call", call.ID.String()).Msg("load contact")
			}
		}
		if e.contact == nil && e.phone != "" {
			if c, err := r.contacts.GetContactByPhone(ctx, call.OrgID, e.phone); err == nil {
				e.contact = c
			} else if !errors.Is(err, domain.ErrNotFound) {
				r.log.Warn().Err(err).Str("call", call.ID.String()).Msg("lookup contact by phone")
			}
		}
	}
	if r.campaigns != nil && call.CampaignID != nil {
		if c, err := r.campaigns.GetCampaign(ctx, *call.CampaignID); err == nil {
			e.campaign = c
		} else if !errors.Is(err, domain.ErrNotFound) {
			r.log.Warn().Err(err).Str("call", call.ID.String()).Msg("load campaign")
		}
	}

	v := map[string]string{
		"phone":       e.phone,
		"summary":     call.Summary,
		"outcome":     call.Outcome,
		"outcomeNote": call.OutcomeNote,
		"intent":      call.Intent,
		"sentiment":   string(call.Sentiment),
		"duration":    strconv.Itoa(call.DurationSec),
	}
	if e.contact != nil {
		e.name = e.contact.Name
		for k, val := range e.contact.Meta {
			v["vars."+k] = val
		}
	}
	v["name"] = e.name
	if e.campaign != nil {
		v["campaign"] = e.campaign.Name
		for _, o := range e.campaign.Outcomes {
			if o.Code == call.Outcome && o.Label != "" {
				v["outcomeCode"] = call.Outcome
				v["outcome"] = o.Label
			}
		}
	}
	if _, ok := v["outcomeCode"]; !ok {
		v["outcomeCode"] = call.Outcome
	}
	if vars, ok := call.Metadata[MetaVars].(map[string]any); ok {
		for k, val := range vars {
			v["vars."+k] = fmt.Sprint(val)
		}
	} else if vars, ok := call.Metadata[MetaVars].(map[string]string); ok {
		for k, val := range vars {
			v["vars."+k] = val
		}
	}
	e.vars = v
	return e
}

// --- SMS -------------------------------------------------------------------

func (r *Runner) runSMS(ctx context.Context, call *domain.Call, e *env, a domain.PostCallAction) error {
	if r.sms == nil {
		return errors.New("sms is not configured")
	}
	if e.phone == "" {
		return errors.New("call has no customer number")
	}
	body, err := RenderTemplate(a.Template, e.vars)
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("rendered sms body is empty")
	}
	id := call.ID
	if _, err := r.sms.Send(ctx, call.OrgID, &id, e.phone, body); err != nil {
		return fmt.Errorf("send sms: %w", err)
	}
	return nil
}

// Placeholders: {{name}}, {{vars.some-key}}. Go syntax ({{.name}}) also works.
var (
	varRe    = regexp.MustCompile(`\{\{\s*vars\.([^\s{}]+)\s*\}\}`)
	simpleRe = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)
)

// RenderTemplate renders an SMS template against flat variables. {{name}},
// {{phone}}, {{summary}}, {{outcome}}, {{campaign}} and {{vars.X}} are
// supported; unknown placeholders render as empty text.
func RenderTemplate(tpl string, vars map[string]string) (string, error) {
	src := varRe.ReplaceAllStringFunc(tpl, func(m string) string {
		key := varRe.FindStringSubmatch(m)[1]
		return "{{index . " + strconv.Quote("vars."+key) + "}}"
	})
	src = simpleRe.ReplaceAllStringFunc(src, func(m string) string {
		key := simpleRe.FindStringSubmatch(m)[1]
		return "{{index . " + strconv.Quote(key) + "}}"
	})
	t, err := template.New("sms").Option("missingkey=zero").Parse(src)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var b bytes.Buffer
	if err := t.Execute(&b, vars); err != nil {
		return "", fmt.Errorf("render template: %w", err)
	}
	return strings.TrimSpace(b.String()), nil
}

// --- webhook ---------------------------------------------------------------

func (r *Runner) runWebhook(ctx context.Context, call *domain.Call, e *env, a domain.PostCallAction) error {
	if r.webhooks == nil {
		return errors.New("webhooks are not configured")
	}
	if a.WebhookID == nil {
		return errors.New("webhook action has no webhookId")
	}
	payload := map[string]any{
		"call":        call,
		"summary":     call.Summary,
		"sentiment":   call.Sentiment,
		"intent":      call.Intent,
		"durationSec": call.DurationSec,
		"outcome":     call.Outcome,
		"outcomeNote": call.OutcomeNote,
		"endReason":   call.EndReason,
		"postCall":    true,
	}
	if call.LLMModelUsed != "" {
		payload["llmModelUsed"] = call.LLMModelUsed
	}
	if e.campaign != nil {
		payload["campaign"] = map[string]any{"id": e.campaign.ID, "name": e.campaign.Name}
	}
	id := call.ID
	ev := domain.Event{
		ID: uuid.NewString(), Type: domain.EventCallEnded, OrgID: call.OrgID, CallID: &id,
		At: r.now().UTC(), Payload: payload,
	}
	if err := r.webhooks.EnqueueFor(ctx, *a.WebhookID, ev); err != nil {
		return fmt.Errorf("enqueue webhook %s: %w", *a.WebhookID, err)
	}
	return nil
}

// --- callback --------------------------------------------------------------

type requestedCallback struct {
	DueAt time.Time `json:"dueAt"`
	Note  string    `json:"note"`
}

func (r *Runner) runCallback(ctx context.Context, call *domain.Call, e *env, a domain.PostCallAction) error {
	if r.callbacks == nil {
		return errors.New("callbacks are not configured")
	}
	if e.phone == "" {
		return errors.New("call has no customer number")
	}
	now := r.now().UTC()
	var due []requestedCallback
	switch {
	case a.DelayMin > 0:
		due = []requestedCallback{{DueAt: now.Add(time.Duration(a.DelayMin) * time.Minute)}}
	default:
		reqs, err := requestedCallbacks(call.Metadata[MetaCallbacks])
		if err != nil {
			return fmt.Errorf("parse metadata.callbacks: %w", err)
		}
		for _, q := range reqs {
			if q.DueAt.IsZero() {
				continue
			}
			if q.DueAt.Before(now) {
				q.DueAt = now
			}
			due = append(due, q)
		}
	}
	var errs []error
	for _, q := range due {
		note := q.Note
		if note == "" {
			note = call.Summary
		}
		src := call.ID
		cb := &domain.CallbackRequest{
			ID: uuid.New(), OrgID: call.OrgID, SourceCallID: &src, ContactID: call.ContactID,
			Phone: e.phone, Name: e.name, Note: note, DueAt: q.DueAt.UTC(),
			SIPNumberID: call.SIPNumberID, AgentProfileID: call.AgentProfileID,
			Status: domain.CallbackPending, CreatedAt: now, UpdatedAt: now,
		}
		if cb.ContactID == nil && e.contact != nil {
			id := e.contact.ID
			cb.ContactID = &id
		}
		if err := r.callbacks.CreateCallback(ctx, cb); err != nil {
			errs = append(errs, fmt.Errorf("create callback: %w", err))
		}
	}
	return errors.Join(errs...)
}

// requestedCallbacks parses metadata.callbacks whatever its concrete type
// (JSON-decoded []any, typed slices, ...).
func requestedCallbacks(v any) ([]requestedCallback, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out []requestedCallback
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// --- in-memory LRU of processed call ids -------------------------------------

type lru struct {
	mu    sync.Mutex
	cap   int
	order *list.List
	items map[uuid.UUID]*list.Element
}

func newLRU(n int) *lru {
	if n <= 0 {
		n = 4096
	}
	return &lru{cap: n, order: list.New(), items: map[uuid.UUID]*list.Element{}}
}

// add records id and reports whether it was new.
func (l *lru) add(id uuid.UUID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.items[id]; ok {
		l.order.MoveToFront(el)
		return false
	}
	l.items[id] = l.order.PushFront(id)
	for l.order.Len() > l.cap {
		last := l.order.Back()
		l.order.Remove(last)
		delete(l.items, last.Value.(uuid.UUID))
	}
	return true
}
