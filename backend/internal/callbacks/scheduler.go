// Package callbacks schedules and dials "call me back" requests: customer
// promises made during a call, post-call callback actions and callbacks
// created through the API. The Scheduler polls due callbacks, checks quota
// and business hours, places them as outbound AI calls and settles them when
// the call ends.
package callbacks

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/phone"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/routing"
)

// EventCallbackScheduled is published when a callback is created
// (payload {"callback": CallbackRequest}, docs/EVENTS.md).
const EventCallbackScheduled domain.EventType = "callback.scheduled"

// MetadataCallbackID is the Call.Metadata key linking a call to its callback.
const MetadataCallbackID = "callbackId"

const (
	maxNoteLen = 2000
	maxNameLen = 200
	// errorBackoff delays a callback whose dial could not even be prepared
	// because of a transient storage error.
	errorBackoff = time.Minute
	dialTimeout  = 90 * time.Second
)

// CallbackCreator is implemented by Scheduler; the post-call action runner
// depends on this narrow interface.
type CallbackCreator interface {
	CreateCallback(ctx context.Context, c *domain.CallbackRequest) error
}

var _ CallbackCreator = (*Scheduler)(nil)

// Config tunes the Scheduler. Zero values take the defaults noted.
type Config struct {
	PollInterval time.Duration // default 15s
	// RetryAfter delays the retry of an unanswered callback (default 30m).
	RetryAfter time.Duration
	// MaxAttempts is the number of dials before a callback fails (default 2).
	MaxAttempts int
	// DeferAfter delays a callback the org may not place yet because of
	// quota (default 15m).
	DeferAfter time.Duration
	// BatchSize is the most callbacks claimed per poll (default 10).
	BatchSize int
	// RingTimeout is passed to telephony (default 45s).
	RingTimeout time.Duration
	// Now is the clock (default time.Now, UTC).
	Now func() time.Time
}

func (c *Config) applyDefaults() {
	if c.PollInterval <= 0 {
		c.PollInterval = 15 * time.Second
	}
	if c.RetryAfter <= 0 {
		c.RetryAfter = 30 * time.Minute
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 2
	}
	if c.DeferAfter <= 0 {
		c.DeferAfter = 15 * time.Minute
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 10
	}
	if c.RingTimeout <= 0 {
		c.RingTimeout = 45 * time.Second
	}
	if c.Now == nil {
		c.Now = func() time.Time { return time.Now().UTC() }
	}
}

// Scheduler dials due callbacks. Create it with New and run it with Run.
type Scheduler struct {
	repo     domain.IntegrationsRepository
	calls    domain.CallRepository
	contacts domain.ContactRepository
	numbers  domain.SIPNumberRepository
	profiles domain.AgentProfileRepository
	tel      domain.Telephony
	ent      domain.Entitlements
	bus      domain.EventBus
	orgs     domain.OrgRepository // optional, for the org time zone
	cfg      Config
	log      zerolog.Logger
}

// New builds a Scheduler. bus and ent may be nil (no events / no quota check).
func New(repo domain.IntegrationsRepository, calls domain.CallRepository, contacts domain.ContactRepository,
	numbers domain.SIPNumberRepository, profiles domain.AgentProfileRepository, tel domain.Telephony,
	ent domain.Entitlements, bus domain.EventBus, cfg Config, log zerolog.Logger) *Scheduler {
	cfg.applyDefaults()
	return &Scheduler{
		repo: repo, calls: calls, contacts: contacts, numbers: numbers, profiles: profiles,
		tel: tel, ent: ent, bus: bus, cfg: cfg,
		log: log.With().Str("component", "callbacks").Logger(),
	}
}

// WithOrgs makes business hours without their own time zone use the
// organisation's zone (default Asia/Ulaanbaatar otherwise). It returns s.
func (s *Scheduler) WithOrgs(orgs domain.OrgRepository) *Scheduler {
	s.orgs = orgs
	return s
}

func (s *Scheduler) now() time.Time { return s.cfg.Now() }

func invalid(format string, args ...any) error {
	return fmt.Errorf("callback: %s: %w", fmt.Sprintf(format, args...), domain.ErrInvalid)
}

// CreateCallback implements CallbackCreator; it is Create.
func (s *Scheduler) CreateCallback(ctx context.Context, c *domain.CallbackRequest) error {
	return s.Create(ctx, c)
}

// Create validates and stores a new pending callback and publishes
// callback.scheduled. c needs OrgID, Phone and a future DueAt; ID, Status,
// timestamps and (when the phone matches one) ContactID are filled in. The
// phone is normalised to E.164. Referenced SIP number, profile and contact
// must belong to the organisation. Errors wrap domain.ErrInvalid.
func (s *Scheduler) Create(ctx context.Context, c *domain.CallbackRequest) error {
	if c == nil || c.OrgID == uuid.Nil {
		return invalid("orgId is required")
	}
	p, err := phone.Normalize(c.Phone)
	if err != nil {
		return invalid("phone: %v", err)
	}
	c.Phone = p
	now := s.now()
	if !c.DueAt.After(now) {
		return invalid("dueAt must be in the future")
	}
	c.Name, c.Note = strings.TrimSpace(c.Name), strings.TrimSpace(c.Note)
	if len(c.Name) > maxNameLen {
		return invalid("name must be at most %d characters", maxNameLen)
	}
	if len(c.Note) > maxNoteLen {
		return invalid("note must be at most %d characters", maxNoteLen)
	}
	if c.SIPNumberID != nil {
		n, err := s.numbers.GetSIPNumber(ctx, *c.SIPNumberID)
		if errors.Is(err, domain.ErrNotFound) || (err == nil && (n == nil || n.OrgID != c.OrgID)) {
			return invalid("sipNumberId does not exist")
		}
		if err != nil {
			return fmt.Errorf("get sip number: %w", err)
		}
	}
	if c.AgentProfileID != nil {
		pr, err := s.profiles.GetAgentProfile(ctx, *c.AgentProfileID)
		if errors.Is(err, domain.ErrNotFound) || (err == nil && (pr == nil || pr.OrgID != c.OrgID)) {
			return invalid("agentProfileId does not exist")
		}
		if err != nil {
			return fmt.Errorf("get agent profile: %w", err)
		}
	}
	if c.ContactID != nil {
		ct, err := s.contacts.GetContact(ctx, *c.ContactID)
		if errors.Is(err, domain.ErrNotFound) || (err == nil && (ct == nil || ct.OrgID != c.OrgID)) {
			return invalid("contactId does not exist")
		}
		if err != nil {
			return fmt.Errorf("get contact: %w", err)
		}
	} else if ct, err := s.contacts.GetContactByPhone(ctx, c.OrgID, c.Phone); err == nil && ct != nil {
		c.ContactID = &ct.ID
		if c.Name == "" {
			c.Name = ct.Name
		}
	}

	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	c.Status = domain.CallbackPending
	c.Attempts = 0
	c.ResultCallID = nil
	c.CreatedAt, c.UpdatedAt = now, now
	if err := s.repo.CreateCallback(ctx, c); err != nil {
		return fmt.Errorf("create callback: %w", err)
	}
	s.publish(ctx, c.OrgID, c.SourceCallID, EventCallbackScheduled, map[string]any{"callback": *c})
	return nil
}

// Run polls for due callbacks every PollInterval until ctx is cancelled
// (a clean shutdown returns nil).
func (s *Scheduler) Run(ctx context.Context) error {
	s.log.Info().Dur("pollInterval", s.cfg.PollInterval).Msg("callback scheduler started")
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()
	for {
		s.Poll(ctx)
		select {
		case <-ctx.Done():
			s.log.Info().Msg("callback scheduler stopping")
			return nil
		case <-ticker.C:
		}
	}
}

// Poll claims the due callbacks and processes each; it returns how many were
// claimed. Run calls it on every tick.
func (s *Scheduler) Poll(ctx context.Context) int {
	if ctx.Err() != nil {
		return 0
	}
	due, err := s.repo.ClaimDueCallbacks(ctx, s.cfg.BatchSize)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error().Err(err).Msg("claim due callbacks")
		}
		return 0
	}
	for i := range due {
		s.process(ctx, &due[i])
	}
	return len(due)
}

// process handles one claimed callback (status already "dialed").
func (s *Scheduler) process(ctx context.Context, c *domain.CallbackRequest) {
	// Storage writes after the claim must survive a shutdown.
	wctx := context.WithoutCancel(ctx)
	log := s.log.With().Str("callbackId", c.ID.String()).Str("orgId", c.OrgID.String()).Logger()

	if s.ent != nil {
		ok, reason, err := s.ent.CanStartCall(ctx, c.OrgID)
		if err != nil {
			log.Error().Err(err).Msg("check entitlements; deferring callback")
			s.deferUndialed(wctx, c, s.now().Add(s.cfg.DeferAfter))
			return
		}
		if !ok {
			log.Info().Str("reason", reason).Msg("org may not start calls; deferring callback")
			s.deferUndialed(wctx, c, s.now().Add(s.cfg.DeferAfter))
			return
		}
	}

	num, err := s.pickNumber(ctx, c)
	if err != nil {
		log.Warn().Err(err).Msg("no SIP number for callback")
		s.fail(wctx, c, err.Error())
		return
	}
	hours := num.Routing.BusinessHours
	if tz := s.location(ctx, c.OrgID); !routing.IsOpen(hours, s.now(), tz) {
		next, ok := routing.NextOpen(hours, s.now(), tz)
		if !ok {
			log.Warn().Str("number", num.Number).Msg("business hours never open")
			s.fail(wctx, c, "business hours never open")
			return
		}
		log.Info().Time("next", next).Str("number", num.Number).Msg("outside business hours; rescheduling callback")
		s.deferUndialed(wctx, c, next)
		return
	}

	profile, err := s.pickProfile(ctx, c, num)
	if err != nil {
		log.Warn().Err(err).Msg("no agent profile for callback")
		s.fail(wctx, c, err.Error())
		return
	}

	call, err := s.startCall(ctx, c, num, profile)
	if err != nil {
		if ctx.Err() != nil {
			s.deferUndialed(wctx, c, s.now())
			return
		}
		log.Error().Err(err).Msg("prepare callback call")
		s.deferUndialed(wctx, c, s.now().Add(errorBackoff))
		return
	}
	s.dial(ctx, wctx, c, call, num, profile)
}

func (s *Scheduler) location(ctx context.Context, orgID uuid.UUID) *time.Location {
	if s.orgs == nil {
		return routing.Location("")
	}
	org, err := s.orgs.GetOrg(ctx, orgID)
	if err != nil || org == nil {
		return routing.Location("")
	}
	return routing.Location(org.Timezone)
}

// pickNumber returns the callback's SIP number, or the org's first active
// outbound-capable one when none is given or the given one is unusable.
func (s *Scheduler) pickNumber(ctx context.Context, c *domain.CallbackRequest) (*domain.SIPNumber, error) {
	usable := func(n *domain.SIPNumber) bool {
		return n != nil && n.OrgID == c.OrgID && n.Active && n.AllowOutbound
	}
	if c.SIPNumberID != nil {
		n, err := s.numbers.GetSIPNumber(ctx, *c.SIPNumberID)
		switch {
		case err == nil && usable(n):
			return n, nil
		case err != nil && !errors.Is(err, domain.ErrNotFound):
			return nil, fmt.Errorf("get sip number: %w", err)
		}
		s.log.Warn().Str("callbackId", c.ID.String()).Str("sipNumberId", c.SIPNumberID.String()).
			Msg("callback SIP number unusable; falling back to the org's first outbound number")
	}
	all, err := s.numbers.ListSIPNumbers(ctx, c.OrgID)
	if err != nil {
		return nil, fmt.Errorf("list sip numbers: %w", err)
	}
	for i := range all {
		if usable(&all[i]) {
			return &all[i], nil
		}
	}
	return nil, errors.New("no active outbound SIP number")
}

// pickProfile returns the callback's profile, else the number's.
func (s *Scheduler) pickProfile(ctx context.Context, c *domain.CallbackRequest, num *domain.SIPNumber) (*domain.AgentProfile, error) {
	for _, id := range []*uuid.UUID{c.AgentProfileID, num.AgentProfileID} {
		if id == nil {
			continue
		}
		p, err := s.profiles.GetAgentProfile(ctx, *id)
		switch {
		case err == nil && p != nil && p.OrgID == c.OrgID:
			return p, nil
		case err != nil && !errors.Is(err, domain.ErrNotFound):
			return nil, fmt.Errorf("get agent profile: %w", err)
		}
	}
	return nil, errors.New("no agent profile")
}

// startCall creates the outbound Call row, links it to the callback (counting
// the attempt) and publishes call.started.
func (s *Scheduler) startCall(ctx context.Context, c *domain.CallbackRequest, num *domain.SIPNumber, profile *domain.AgentProfile) (*domain.Call, error) {
	contactID := c.ContactID
	if contactID == nil {
		if ct, err := s.contacts.GetContactByPhone(ctx, c.OrgID, c.Phone); err == nil && ct != nil {
			contactID = &ct.ID
		}
	}
	now := s.now()
	id := uuid.New()
	numID, profID := num.ID, profile.ID
	call := &domain.Call{
		ID:             id,
		OrgID:          c.OrgID,
		ContactID:      contactID,
		SIPNumberID:    &numID,
		AgentProfileID: &profID,
		Direction:      domain.DirectionOutbound,
		Status:         domain.StatusRinging,
		FromNumber:     num.Number,
		ToNumber:       c.Phone,
		RoomName:       "call-" + id.String(),
		StartedAt:      now,
		Metadata:       map[string]any{MetadataCallbackID: c.ID.String(), "note": c.Note},
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.calls.CreateCall(ctx, call); err != nil {
		return nil, fmt.Errorf("create call: %w", err)
	}

	// Attempts was already incremented by IntegrationsRepository.ClaimDueCallbacks.
	c.ResultCallID = &call.ID
	c.SIPNumberID = &numID
	c.AgentProfileID = &profID
	c.ContactID = contactID
	c.Status = domain.CallbackDialed
	c.UpdatedAt = now
	if err := s.repo.UpdateCallback(context.WithoutCancel(ctx), c); err != nil {
		c.Attempts--
		c.ResultCallID = nil
		s.closeCall(context.WithoutCancel(ctx), call, domain.StatusFailed, "failed", "link callback: "+err.Error())
		return nil, fmt.Errorf("link callback to call: %w", err)
	}
	s.publishCall(ctx, domain.EventCallStarted, call)
	return call, nil
}

// dial places the call and records the result. A failed dial ends the call
// and settles the callback like a call that ended unanswered.
func (s *Scheduler) dial(ctx, wctx context.Context, c *domain.CallbackRequest, call *domain.Call, num *domain.SIPNumber, profile *domain.AgentProfile) {
	log := s.log.With().Str("callbackId", c.ID.String()).Str("callId", call.ID.String()).Logger()
	req := domain.OutboundCallRequest{
		CallID:       call.ID,
		RoomName:     call.RoomName,
		FromNumber:   *num,
		ToNumber:     c.Phone,
		AgentProfile: *profile,
		Metadata: map[string]any{
			"callId":           call.ID.String(),
			"direction":        string(domain.DirectionOutbound),
			"sipNumberId":      num.ID.String(),
			"agentProfileId":   profile.ID.String(),
			MetadataCallbackID: c.ID.String(),
			"note":             c.Note,
		},
		WaitUntilAnswered: false,
		RingTimeout:       s.cfg.RingTimeout,
	}
	if call.ContactID != nil {
		req.Metadata["contactId"] = call.ContactID.String()
	}
	if c.Name != "" {
		req.Metadata["contactName"] = c.Name
	}
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	res, dialErr := s.tel.Dial(dctx, req)
	cancel()
	if dialErr != nil || res.Error != "" {
		msg := res.Error
		if dialErr != nil {
			msg = dialErr.Error()
		}
		log.Warn().Str("error", msg).Msg("callback dial failed")
		s.closeCall(wctx, call, domain.StatusFailed, "failed", msg)
		s.settle(wctx, c, domain.StatusFailed)
		return
	}
	call.ParticipantID, call.SIPCallID = res.ParticipantID, res.SIPCallID
	typ := domain.EventCallUpdated
	if res.Answered {
		now := s.now()
		call.Status, call.AnsweredAt = domain.StatusActive, &now
		typ = domain.EventCallAnswered
	}
	call.UpdatedAt = s.now()
	if err := s.calls.UpdateCall(wctx, call); err != nil {
		log.Error().Err(err).Msg("update dialed call")
		return
	}
	s.publishCall(wctx, typ, call)
}

// closeCall ends a call the scheduler itself failed to place.
func (s *Scheduler) closeCall(ctx context.Context, call *domain.Call, status domain.CallStatus, reason, detail string) {
	now := s.now()
	md := maps.Clone(call.Metadata)
	if md == nil {
		md = map[string]any{}
	}
	md["dialError"] = detail
	call.Metadata = md
	call.Status, call.EndReason, call.EndedAt, call.UpdatedAt = status, reason, &now, now
	if err := s.calls.UpdateCall(ctx, call); err != nil {
		s.log.Error().Err(err).Str("callId", call.ID.String()).Msg("close failed callback call")
		return
	}
	snap := *call
	id := call.ID
	s.publish(ctx, call.OrgID, &id, domain.EventCallEnded, map[string]any{
		"call": snap, "endReason": reason, "durationSec": 0,
	})
}

// OnCallEnded settles the callback a finished call belongs to (found through
// Call.Metadata["callbackId"]); other calls are ignored. Duplicate
// notifications are harmless: only callbacks still "dialed" are settled.
func (s *Scheduler) OnCallEnded(ctx context.Context, call *domain.Call) {
	if call == nil {
		return
	}
	raw, ok := call.Metadata[MetadataCallbackID].(string)
	if !ok || raw == "" {
		return
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return
	}
	if !call.Status.IsTerminal() {
		return
	}
	c, err := s.repo.GetCallback(ctx, id)
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			s.log.Error().Err(err).Str("callbackId", raw).Msg("load callback of ended call")
		}
		return
	}
	if c.Status != domain.CallbackDialed {
		return
	}
	if c.ResultCallID != nil && *c.ResultCallID != call.ID {
		return // a stale call of an earlier attempt
	}
	s.settle(ctx, c, call.Status)
}

// settle records the result of the dialed call: done for a conversation,
// else pending again after RetryAfter while attempts remain, else failed.
func (s *Scheduler) settle(ctx context.Context, c *domain.CallbackRequest, result domain.CallStatus) {
	now := s.now()
	switch {
	case result == domain.StatusCompleted || result == domain.StatusVoicemail:
		c.Status = domain.CallbackDone
	case c.Attempts < s.cfg.MaxAttempts:
		c.Status = domain.CallbackPending
		c.DueAt = now.Add(s.cfg.RetryAfter)
	default:
		c.Status = domain.CallbackFailed
	}
	c.UpdatedAt = now
	if err := s.repo.UpdateCallback(ctx, c); err != nil {
		s.log.Error().Err(err).Str("callbackId", c.ID.String()).Msg("settle callback")
	}
}

// deferUndialed puts back a claimed callback that was not dialed at all
// (quota, business hours, preparation error). The claim counted an attempt
// (ClaimDueCallbacks increments Attempts); that is undone so only real dials
// count towards MaxAttempts.
func (s *Scheduler) deferUndialed(ctx context.Context, c *domain.CallbackRequest, due time.Time) {
	if c.Attempts > 0 {
		c.Attempts--
	}
	s.reschedule(ctx, c, due)
}

// reschedule puts a claimed callback back to pending at due.
func (s *Scheduler) reschedule(ctx context.Context, c *domain.CallbackRequest, due time.Time) {
	c.Status = domain.CallbackPending
	c.DueAt = due
	c.UpdatedAt = s.now()
	if err := s.repo.UpdateCallback(ctx, c); err != nil {
		s.log.Error().Err(err).Str("callbackId", c.ID.String()).Msg("reschedule callback")
	}
}

// fail marks a claimed callback that cannot be dialed at all as failed.
func (s *Scheduler) fail(ctx context.Context, c *domain.CallbackRequest, reason string) {
	s.log.Warn().Str("callbackId", c.ID.String()).Str("reason", reason).Msg("callback failed")
	c.Status = domain.CallbackFailed
	c.UpdatedAt = s.now()
	if err := s.repo.UpdateCallback(ctx, c); err != nil {
		s.log.Error().Err(err).Str("callbackId", c.ID.String()).Msg("fail callback")
	}
}

func (s *Scheduler) publishCall(ctx context.Context, typ domain.EventType, call *domain.Call) {
	snap := *call
	id := call.ID
	s.publish(ctx, call.OrgID, &id, typ, map[string]any{"call": snap})
}

func (s *Scheduler) publish(ctx context.Context, orgID uuid.UUID, callID *uuid.UUID, typ domain.EventType, payload any) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(ctx, domain.Event{
		ID: uuid.NewString(), Type: typ, OrgID: orgID, CallID: callID, At: s.now(), Payload: payload,
	})
}
