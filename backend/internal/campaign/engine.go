// Package campaign implements the outbound campaign dialer engine.
//
// The engine polls running campaigns, claims due targets from the
// CampaignRepository, places calls through the Telephony port and turns call
// outcomes into target / campaign state (retries with backoff, MaxAttempts,
// counters, auto-completion). Answered calls keep their target in "calling"
// until the HTTP layer reports the end of the call through OnCallEnded.
//
// Attempt accounting: the engine owns CampaignTarget.Attempts. It increments
// the counter when it creates a Call for a claimed target, so
// CampaignRepository.ClaimTargets must only flip status pending → calling.
//
// Campaign v2: before claiming, the engine honours the campaign's calling
// window (Schedule, see InWindow), its pace (Schedule.PacePerMinute, a token
// bucket per campaign) and a dry-run limit (Start with dryRunLimit > 0 dials
// that many targets and then pauses the campaign). Claimed targets whose
// number is on the organisation's do-not-call list are marked "skipped"
// instead of being dialled. Structured call outcomes (Campaign.Outcomes) are
// copied onto the target; a non-terminal outcome such as "callback" requeues
// the target while attempts remain.
package campaign

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Default option values.
const (
	DefaultPollInterval         = 2 * time.Second
	DefaultRetryBackoff         = 5 * time.Minute
	DefaultRingTimeout          = 30 * time.Second
	DefaultMaxGlobalConcurrency = 20
	DefaultMaxCallDuration      = 30 * time.Minute

	// dialGrace is added to RingTimeout as a hard deadline for Telephony.Dial
	// so a misbehaving adapter can never pin a dialer slot forever.
	dialGrace = 30 * time.Second
	// targetPageSize is the page size used when scanning campaign targets.
	targetPageSize = 500
	// minSyncInterval bounds how often an idle campaign is re-scanned to
	// recompute counters and detect completion.
	minSyncInterval = 30 * time.Second
	// windowLogEvery rate-limits the "outside calling window" debug log.
	windowLogEvery = time.Minute

	// lastErrorDoNotCall is CampaignTarget.LastError of a target skipped
	// because its number is on the do-not-call list.
	lastErrorDoNotCall = "do_not_call"
	// outcomeNoContact is the outcome code given to targets whose call was
	// never answered, when the campaign defines it.
	outcomeNoContact = "no_contact"
)

// Options tunes the engine. Zero values are replaced by defaults.
type Options struct {
	// PollInterval is how often running campaigns are polled for due targets.
	PollInterval time.Duration
	// RetryBackoff is the delay before a failed / unanswered target is retried.
	RetryBackoff time.Duration
	// RingTimeout is passed to Telephony.Dial.
	RingTimeout time.Duration
	// MaxGlobalConcurrency caps simultaneous campaign calls handled by this
	// engine across all campaigns (dialing + answered).
	MaxGlobalConcurrency int
	// MaxCallDuration is the longest an answered call is expected to last.
	// A target stuck in "calling" for more than 2×MaxCallDuration is
	// considered orphaned and is requeued.
	MaxCallDuration time.Duration
	// Clock returns the current time (tests inject a fake clock).
	Clock func() time.Time
}

func (o Options) withDefaults() Options {
	if o.PollInterval <= 0 {
		o.PollInterval = DefaultPollInterval
	}
	if o.RetryBackoff <= 0 {
		o.RetryBackoff = DefaultRetryBackoff
	}
	if o.RingTimeout <= 0 {
		o.RingTimeout = DefaultRingTimeout
	}
	if o.MaxGlobalConcurrency <= 0 {
		o.MaxGlobalConcurrency = DefaultMaxGlobalConcurrency
	}
	if o.MaxCallDuration <= 0 {
		o.MaxCallDuration = DefaultMaxCallDuration
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	return o
}

// slot is one campaign call owned by this engine (claimed target → settled).
// The set of slots is the global concurrency semaphore.
type slot struct {
	campaignID uuid.UUID
	callID     uuid.UUID // zero until the Call row exists
	target     domain.CampaignTarget
	since      time.Time
	dialing    bool // a dial goroutine is still running for it
}

// Engine is the outbound dialer. Create it with NewEngine and run it with Run.
type Engine struct {
	campaigns domain.CampaignRepository
	calls     domain.CallRepository
	contacts  domain.ContactRepository
	numbers   domain.SIPNumberRepository
	profiles  domain.AgentProfileRepository
	dnc       domain.DoNotCallRepository
	tel       domain.Telephony
	bus       domain.EventBus
	opts      Options
	log       zerolog.Logger

	// stateMu serialises target / campaign state transitions (settling a
	// target, counters, Start, Pause, reconciliation) inside this process.
	stateMu sync.Mutex

	mu       sync.Mutex // guards active, lastSync and the scheduling state below
	active   map[uuid.UUID]*slot
	lastSync map[uuid.UUID]time.Time

	buckets   map[uuid.UUID]*tokenBucket // pacing per campaign
	windowLog map[uuid.UUID]time.Time    // last "outside window" log per campaign
	locations map[string]*time.Location  // resolved schedule time zones

	wg sync.WaitGroup // dial goroutines
}

// NewEngine builds a dialer engine. dnc may be nil, in which case no
// do-not-call check happens at claim time.
func NewEngine(
	campaigns domain.CampaignRepository,
	calls domain.CallRepository,
	contacts domain.ContactRepository,
	numbers domain.SIPNumberRepository,
	profiles domain.AgentProfileRepository,
	dnc domain.DoNotCallRepository,
	tel domain.Telephony,
	bus domain.EventBus,
	opts Options,
	log zerolog.Logger,
) *Engine {
	return &Engine{
		campaigns: campaigns,
		calls:     calls,
		contacts:  contacts,
		numbers:   numbers,
		profiles:  profiles,
		dnc:       dnc,
		tel:       tel,
		bus:       bus,
		opts:      opts.withDefaults(),
		log:       log.With().Str("component", "campaign").Logger(),
		active:    make(map[uuid.UUID]*slot),
		lastSync:  make(map[uuid.UUID]time.Time),
		buckets:   make(map[uuid.UUID]*tokenBucket),
		windowLog: make(map[uuid.UUID]time.Time),
		locations: make(map[string]*time.Location),
	}
}

func (e *Engine) now() time.Time { return e.opts.Clock().UTC() }

func (e *Engine) staleAfter() time.Duration { return 2 * e.opts.MaxCallDuration }

func (e *Engine) syncInterval() time.Duration {
	return max(minSyncInterval, 15*e.opts.PollInterval)
}

// Run reconciles orphaned targets and then polls running campaigns every
// PollInterval until ctx is cancelled. It waits for in-flight dial goroutines
// before returning. A cancelled context is a clean shutdown (nil error).
func (e *Engine) Run(ctx context.Context) error {
	e.log.Info().
		Dur("pollInterval", e.opts.PollInterval).
		Int("maxGlobalConcurrency", e.opts.MaxGlobalConcurrency).
		Msg("campaign dialer started")
	defer e.wg.Wait()

	if err := e.reconcile(ctx); err != nil && ctx.Err() == nil {
		e.log.Error().Err(err).Msg("reconcile stuck targets")
	}

	ticker := time.NewTicker(e.opts.PollInterval)
	defer ticker.Stop()
	for {
		e.pollOnce(ctx)
		select {
		case <-ctx.Done():
			e.log.Info().Msg("campaign dialer stopping")
			return nil
		case <-ticker.C:
		}
	}
}

// pollOnce runs one dialer iteration over all running campaigns.
func (e *Engine) pollOnce(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	e.sweepStale(ctx)

	running, err := e.campaigns.ListRunningCampaigns(ctx)
	if err != nil {
		if ctx.Err() == nil {
			e.log.Error().Err(err).Msg("list running campaigns")
		}
		return
	}
	e.pruneSchedulingState(running)
	for i := range running {
		if ctx.Err() != nil {
			return
		}
		if err := e.pollCampaign(ctx, running[i]); err != nil && ctx.Err() == nil {
			e.log.Error().Err(err).Str("campaignId", running[i].ID.String()).Msg("poll campaign")
		}
	}
}

func (e *Engine) globalFree() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.opts.MaxGlobalConcurrency - len(e.active)
}

func (e *Engine) pollCampaign(ctx context.Context, c domain.Campaign) error {
	now := e.now()
	dryRemaining := -1
	if c.DryRunLimit > 0 {
		dryRemaining = c.DryRunLimit - c.DryRunDialed
		if dryRemaining <= 0 {
			// Limit reached but the pause was not persisted (crash): pause now.
			e.pauseAfterDryRun(ctx, c.ID, 0)
			return nil
		}
	}
	if !e.inWindow(&c, now) {
		return nil
	}

	activeCount, err := e.campaigns.CountActiveTargets(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("count active targets: %w", err)
	}
	concurrency := max(c.Concurrency, 1)
	slots := min(concurrency-activeCount, e.globalFree())
	if dryRemaining >= 0 {
		slots = min(slots, dryRemaining)
	}
	if slots <= 0 {
		return nil
	}
	if pace := c.Schedule.PacePerMinute; pace > 0 {
		slots = min(slots, e.paceAvailable(c.ID, pace, now))
		if slots <= 0 {
			return nil
		}
	} else {
		e.dropBucket(c.ID)
	}

	sip, profile, err := e.dialResources(ctx, &c)
	if err != nil {
		return err
	}

	claimed, err := e.campaigns.ClaimTargets(ctx, c.ID, slots)
	if err != nil {
		return fmt.Errorf("claim targets: %w", err)
	}
	claimed, err = e.skipDoNotCall(ctx, &c, claimed)
	if err != nil {
		return err
	}
	if len(claimed) == 0 {
		if activeCount == 0 {
			e.maybeSync(ctx, c.ID)
		}
		return nil
	}

	e.paceTake(c.ID, len(claimed))
	if c.DryRunLimit > 0 {
		e.pauseAfterDryRun(ctx, c.ID, len(claimed))
	}
	for i := range claimed {
		t := claimed[i]
		e.mu.Lock()
		e.active[t.ID] = &slot{campaignID: c.ID, target: t, since: now, dialing: true}
		e.mu.Unlock()
		e.wg.Add(1)
		go e.runTarget(ctx, c, *sip, *profile, t)
	}
	return nil
}

// dialResources resolves the caller-ID number and agent profile of a campaign.
func (e *Engine) dialResources(ctx context.Context, c *domain.Campaign) (*domain.SIPNumber, *domain.AgentProfile, error) {
	if c.SIPNumberID == nil {
		return nil, nil, fmt.Errorf("campaign %s has no sip number: %w", c.ID, domain.ErrInvalid)
	}
	sip, err := e.numbers.GetSIPNumber(ctx, *c.SIPNumberID)
	if err != nil {
		return nil, nil, fmt.Errorf("get sip number: %w", err)
	}
	if !sip.AllowOutbound {
		return nil, nil, fmt.Errorf("sip number %s does not allow outbound calls: %w", sip.Number, domain.ErrInvalid)
	}
	profileID := c.AgentProfileID
	if profileID == nil {
		profileID = sip.AgentProfileID
	}
	if profileID == nil {
		return nil, nil, fmt.Errorf("campaign %s has no agent profile: %w", c.ID, domain.ErrInvalid)
	}
	profile, err := e.profiles.GetAgentProfile(ctx, *profileID)
	if err != nil {
		return nil, nil, fmt.Errorf("get agent profile: %w", err)
	}
	return sip, profile, nil
}

// Start moves a draft or paused campaign to running.
//
// dryRunLimit > 0 starts a dry run: the limit is stored on the campaign
// (DryRunLimit, DryRunDialed = 0), at most that many targets are dialled and
// the campaign is then paused automatically (campaign.progress is published).
// dryRunLimit == 0 is a full run: any stored dry-run limit is cleared, so
// starting a campaign paused by a dry run continues with the remaining
// pending targets. Starting a running campaign is a no-op, except that
// dryRunLimit == 0 turns a running dry run into a full run.
//
// Errors wrap domain.ErrNotFound, domain.ErrInvalid (negative dryRunLimit,
// no outbound SIP number / profile / targets) or domain.ErrConflict
// (completed).
func (e *Engine) Start(ctx context.Context, campaignID uuid.UUID, dryRunLimit int) error {
	if dryRunLimit < 0 {
		return fmt.Errorf("dry-run limit must be >= 0: %w", domain.ErrInvalid)
	}
	c, err := e.campaigns.GetCampaign(ctx, campaignID)
	if err != nil {
		return fmt.Errorf("get campaign: %w", err)
	}
	switch c.Status {
	case domain.CampaignRunning:
		if dryRunLimit == 0 && c.DryRunLimit > 0 {
			return e.clearRunningDryRun(ctx, campaignID)
		}
		return nil
	case domain.CampaignDraft, domain.CampaignPaused:
	default:
		return fmt.Errorf("cannot start campaign in status %q: %w", c.Status, domain.ErrConflict)
	}
	if _, _, err := e.dialResources(ctx, c); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("campaign sip number or agent profile missing: %w", domain.ErrInvalid)
		}
		return err
	}
	_, total, err := e.campaigns.ListTargets(ctx, campaignID, 1, 0)
	if err != nil {
		return fmt.Errorf("list targets: %w", err)
	}
	if total == 0 {
		return fmt.Errorf("campaign has no targets: %w", domain.ErrInvalid)
	}

	if c.Status == domain.CampaignPaused {
		if err := e.reconcileCampaign(ctx, campaignID); err != nil {
			e.log.Warn().Err(err).Str("campaignId", campaignID.String()).Msg("reconcile on start")
		}
	}

	e.stateMu.Lock()
	c, err = e.campaigns.GetCampaign(ctx, campaignID)
	if err != nil {
		e.stateMu.Unlock()
		return fmt.Errorf("get campaign: %w", err)
	}
	if c.Status != domain.CampaignDraft && c.Status != domain.CampaignPaused {
		e.stateMu.Unlock()
		if c.Status == domain.CampaignRunning {
			return nil
		}
		return fmt.Errorf("cannot start campaign in status %q: %w", c.Status, domain.ErrConflict)
	}
	c.Status = domain.CampaignRunning
	c.DryRunLimit = dryRunLimit
	c.DryRunDialed = 0
	c.UpdatedAt = e.now()
	err = e.campaigns.UpdateCampaign(ctx, c)
	e.stateMu.Unlock()
	if err != nil {
		return fmt.Errorf("update campaign: %w", err)
	}

	e.mu.Lock()
	delete(e.lastSync, campaignID)
	e.mu.Unlock()
	e.emit(ctx, e.progressEvent(c, nil))
	e.log.Info().Str("campaignId", campaignID.String()).Int("dryRunLimit", dryRunLimit).Msg("campaign started")
	return nil
}

// clearRunningDryRun turns a running dry run into a full run.
func (e *Engine) clearRunningDryRun(ctx context.Context, campaignID uuid.UUID) error {
	e.stateMu.Lock()
	c, err := e.campaigns.GetCampaign(ctx, campaignID)
	if err != nil {
		e.stateMu.Unlock()
		return fmt.Errorf("get campaign: %w", err)
	}
	if c.Status != domain.CampaignRunning || c.DryRunLimit == 0 {
		e.stateMu.Unlock()
		return nil
	}
	c.DryRunLimit = 0
	c.DryRunDialed = 0
	c.UpdatedAt = e.now()
	err = e.campaigns.UpdateCampaign(ctx, c)
	e.stateMu.Unlock()
	if err != nil {
		return fmt.Errorf("update campaign: %w", err)
	}
	e.emit(ctx, e.progressEvent(c, nil))
	e.log.Info().Str("campaignId", campaignID.String()).Msg("dry run converted to full run")
	return nil
}

// Pause moves a running campaign to paused. In-flight calls continue and are
// settled normally; no new targets are claimed. Pausing a paused campaign is
// a no-op; any other status returns an error wrapping domain.ErrConflict.
func (e *Engine) Pause(ctx context.Context, campaignID uuid.UUID) error {
	e.stateMu.Lock()
	c, err := e.campaigns.GetCampaign(ctx, campaignID)
	if err != nil {
		e.stateMu.Unlock()
		return fmt.Errorf("get campaign: %w", err)
	}
	switch c.Status {
	case domain.CampaignPaused:
		e.stateMu.Unlock()
		return nil
	case domain.CampaignRunning:
	default:
		e.stateMu.Unlock()
		return fmt.Errorf("cannot pause campaign in status %q: %w", c.Status, domain.ErrConflict)
	}
	c.Status = domain.CampaignPaused
	c.UpdatedAt = e.now()
	err = e.campaigns.UpdateCampaign(ctx, c)
	e.stateMu.Unlock()
	if err != nil {
		return fmt.Errorf("update campaign: %w", err)
	}
	e.emit(ctx, e.progressEvent(c, nil))
	e.log.Info().Str("campaignId", campaignID.String()).Msg("campaign paused")
	return nil
}
