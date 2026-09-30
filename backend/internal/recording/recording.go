// Package recording records calls with LiveKit egress into an object store
// and manages the recordings' lifecycle (docs/API.md "Recordings"):
//
//   - OnCallAnswered starts an audio-only room-composite egress when the org
//     has the "recordings" feature and org.settings.recordCalls != false;
//   - OnEgressEnded (fed by the LiveKit egress_ended webhook through
//     HandleEgressWebhook) marks the recording ready;
//   - SignedURL / Delete serve the API; RunRetention deletes recordings older
//     than org.settings.recordingRetentionDays (default 90).
//
// Every state change is persisted with SetCallRecording and broadcast as
// call.updated {"call": Call, "recording": RecordingInfo}.
package recording

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/livekit"
)

// Recording statuses (domain.RecordingInfo.Status).
const (
	StatusRecording = "recording"
	StatusReady     = "ready"
	StatusFailed    = "failed"
	StatusDeleted   = "deleted"
)

// Feature is the plan feature that enables recordings.
const Feature = "recordings"

// Org settings keys.
const (
	SettingRecordCalls   = "recordCalls"
	SettingRetentionDays = "recordingRetentionDays"
)

// Defaults of Config.
const (
	DefaultRetentionDays = 90
	DefaultSignedTTL     = 10 * time.Minute
	DefaultObjectPrefix  = "recordings/"
	// retentionPage is the page size of RunRetention.
	retentionPage = 200
	// maxRetentionPages bounds one retention run.
	maxRetentionPages = 1000
	// hookTimeout bounds work started from the event bus.
	hookTimeout = 30 * time.Second
)

// CallRepo is the persistence the service needs. *crm call repositories
// implement it; GetCallByRoom is part of domain.CallRepository.
type CallRepo interface {
	GetCall(ctx context.Context, id uuid.UUID) (*domain.Call, error)
	GetCallByRoom(ctx context.Context, roomName string) (*domain.Call, error)
	// SetCallRecording stores rec as the call's recording (nil clears it)
	// and sets calls.recording_url to URLPath(callID) when rec.Status is
	// "ready", or "" otherwise.
	SetCallRecording(ctx context.Context, callID uuid.UUID, rec *domain.RecordingInfo) error
	// ListCallsForRetention returns calls with a stored recording object
	// (recording.status "ready" or "failed" with an objectKey) whose
	// started_at < before, newest first (ORDER BY started_at DESC), at most
	// limit rows. RunRetention pages with before = last row's StartedAt.
	ListCallsForRetention(ctx context.Context, before time.Time, limit int) ([]domain.Call, error)
}

// OrgRepo loads organisations (settings).
type OrgRepo interface {
	GetOrg(ctx context.Context, id uuid.UUID) (*domain.Organization, error)
}

// Egress starts and stops LiveKit egresses (*livekit.Client, *livekit.Mock).
type Egress interface {
	StartRecording(ctx context.Context, roomName, key string, s3 *livekit.S3Target) (string, error)
	StopRecording(ctx context.Context, egressID string) error
}

// Config configures the Service.
type Config struct {
	// Enabled turns automatic recording on (CALLGO_RECORDINGS).
	Enabled bool
	// S3 is where egress uploads; nil = egress writes to its local volume
	// (livekit.LocalRecordingDir), read by an objstore.Local.
	S3 *livekit.S3Target
	// RetentionDays applies when an org has no recordingRetentionDays
	// setting (default 90).
	RetentionDays int
	// SignedTTL is the validity of signed URLs (default 10 min).
	SignedTTL time.Duration
	// ObjectPrefix prefixes object keys (default "recordings/"). Keys are
	// <prefix><orgID>/<yyyy>/<mm>/<callID>.ogg.
	ObjectPrefix string
}

func (c Config) withDefaults() Config {
	if c.RetentionDays <= 0 {
		c.RetentionDays = DefaultRetentionDays
	}
	if c.SignedTTL <= 0 {
		c.SignedTTL = DefaultSignedTTL
	}
	if c.ObjectPrefix == "" {
		c.ObjectPrefix = DefaultObjectPrefix
	}
	if !strings.HasSuffix(c.ObjectPrefix, "/") {
		c.ObjectPrefix += "/"
	}
	c.ObjectPrefix = strings.TrimLeft(c.ObjectPrefix, "/")
	return c
}

// Service manages call recordings.
type Service struct {
	calls  CallRepo
	orgs   OrgRepo
	egress Egress
	store  domain.ObjectStore
	ent    domain.Entitlements
	bus    domain.EventBus
	cfg    Config
	log    zerolog.Logger
	now    func() time.Time

	bg sync.WaitGroup

	mu       sync.Mutex
	starting map[uuid.UUID]struct{} // calls with an OnCallAnswered in flight
}

// New builds the Service. ent == nil allows the feature for every org;
// bus == nil disables broadcasts; egress == nil disables starting.
func New(calls CallRepo, orgs OrgRepo, egress Egress, store domain.ObjectStore, ent domain.Entitlements,
	bus domain.EventBus, cfg Config, log zerolog.Logger) *Service {
	return &Service{
		calls: calls, orgs: orgs, egress: egress, store: store, ent: ent, bus: bus,
		cfg: cfg.withDefaults(), log: log.With().Str("component", "recording").Logger(),
		now:      func() time.Time { return time.Now().UTC() },
		starting: map[uuid.UUID]struct{}{},
	}
}

// Config returns the effective configuration.
func (s *Service) Config() Config { return s.cfg }

// URLPath is the API path that redirects to a call's recording.
func URLPath(callID uuid.UUID) string { return "/api/calls/" + callID.String() + "/recording" }

// ObjectKey returns the object key of a call's recording.
func (s *Service) ObjectKey(c *domain.Call) string {
	t := c.StartedAt
	if t.IsZero() {
		t = s.now()
	}
	t = t.UTC()
	return fmt.Sprintf("%s%s/%04d/%02d/%s.ogg", s.cfg.ObjectPrefix, c.OrgID, t.Year(), int(t.Month()), c.ID)
}

// Call loads a call (used by the HTTP layer for org scoping).
func (s *Service) Call(ctx context.Context, id uuid.UUID) (*domain.Call, error) {
	c, err := s.calls.GetCall(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("recording: get call %s: %w", id, err)
	}
	if c == nil {
		return nil, fmt.Errorf("recording: call %s: %w", id, domain.ErrNotFound)
	}
	return c, nil
}

// ShouldRecord reports whether calls of orgID are recorded now.
func (s *Service) ShouldRecord(ctx context.Context, orgID uuid.UUID) (bool, error) {
	if !s.cfg.Enabled || s.egress == nil {
		return false, nil
	}
	if s.ent != nil {
		ok, err := s.ent.HasFeature(ctx, orgID, Feature)
		if err != nil {
			return false, fmt.Errorf("recording: check feature: %w", err)
		}
		if !ok {
			return false, nil
		}
	}
	if s.orgs != nil {
		org, err := s.orgs.GetOrg(ctx, orgID)
		if err != nil {
			return false, fmt.Errorf("recording: get org %s: %w", orgID, err)
		}
		if org != nil && !settingBool(org.Settings, SettingRecordCalls, true) {
			return false, nil
		}
	}
	return true, nil
}

// OnCallAnswered starts recording call when the org allows it. It is
// idempotent (a call already recording or recorded is left alone). On
// success call.Recording is updated in place.
func (s *Service) OnCallAnswered(ctx context.Context, call *domain.Call) error {
	if call == nil || call.RoomName == "" {
		return nil
	}
	if r := call.Recording; r != nil && (r.Status == StatusRecording || r.Status == StatusReady) {
		return nil
	}
	if !s.claim(call.ID) {
		return nil // a concurrent call.answered is already starting it
	}
	defer s.release(call.ID)
	ok, err := s.ShouldRecord(ctx, call.OrgID)
	if err != nil || !ok {
		return err
	}
	key := s.ObjectKey(call)
	log := s.log.With().Str("callId", call.ID.String()).Str("room", call.RoomName).Logger()
	now := s.now()
	egressID, err := s.egress.StartRecording(ctx, call.RoomName, key, s.cfg.S3)
	if err != nil {
		rec := &domain.RecordingInfo{ObjectKey: key, Status: StatusFailed, StartedAt: &now, EndedAt: &now}
		if serr := s.save(ctx, call, rec); serr != nil {
			log.Warn().Err(serr).Msg("recording: store failed status")
		}
		return fmt.Errorf("recording: start egress for call %s: %w", call.ID, err)
	}
	rec := &domain.RecordingInfo{EgressID: egressID, ObjectKey: key, Status: StatusRecording, StartedAt: &now}
	if err := s.save(ctx, call, rec); err != nil {
		return err
	}
	log.Info().Str("egressId", egressID).Str("key", key).Msg("recording: started")
	return nil
}

func (s *Service) claim(id uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.starting[id]; busy {
		return false
	}
	s.starting[id] = struct{}{}
	return true
}

func (s *Service) release(id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.starting, id)
}

// OnCallEnded stops a still-running egress so the file is finalised
// promptly (the room may outlive the SIP leg). Errors are logged only.
func (s *Service) OnCallEnded(ctx context.Context, call *domain.Call) {
	if call == nil || s.egress == nil {
		return
	}
	r := call.Recording
	if r == nil || r.Status != StatusRecording || r.EgressID == "" {
		return
	}
	if err := s.egress.StopRecording(ctx, r.EgressID); err != nil {
		s.log.Debug().Err(err).Str("egressId", r.EgressID).Msg("recording: stop egress on call end")
	}
}

// OnEgressEnded marks the recording of the call in roomName ready. key is the
// object key reported by egress (used when the call has none stored, e.g. an
// egress started by a dispatch rule). Unknown rooms are ignored.
func (s *Service) OnEgressEnded(ctx context.Context, roomName, egressID, key string, size int64, durationSec int) error {
	call, err := s.callByRoom(ctx, roomName)
	if err != nil || call == nil {
		return err
	}
	prev := call.Recording
	if prev != nil && prev.Status == StatusDeleted {
		return nil
	}
	if prev != nil && prev.Status == StatusReady && prev.EgressID != "" && egressID != "" && prev.EgressID != egressID {
		s.log.Info().Str("callId", call.ID.String()).Str("egressId", egressID).Msg("recording: ignoring second egress of a recorded call")
		return nil
	}
	rec := &domain.RecordingInfo{EgressID: egressID, ObjectKey: key, SizeBytes: size, DurationSec: durationSec, Status: StatusReady}
	if prev != nil {
		if prev.ObjectKey != "" {
			rec.ObjectKey = prev.ObjectKey
		}
		if rec.EgressID == "" {
			rec.EgressID = prev.EgressID
		}
		rec.StartedAt = prev.StartedAt
	}
	if rec.ObjectKey == "" {
		return s.markFailed(ctx, call, rec, "egress reported no file")
	}
	if s.store != nil && rec.SizeBytes == 0 {
		if sz, ok, err := s.store.Stat(ctx, rec.ObjectKey); err == nil && ok {
			rec.SizeBytes = sz
		}
	}
	if rec.DurationSec == 0 && call.DurationSec > 0 {
		rec.DurationSec = call.DurationSec
	}
	now := s.now()
	rec.EndedAt = &now
	if rec.StartedAt == nil {
		st := now.Add(-time.Duration(rec.DurationSec) * time.Second)
		rec.StartedAt = &st
	}
	if err := s.save(ctx, call, rec); err != nil {
		return err
	}
	s.log.Info().Str("callId", call.ID.String()).Str("key", rec.ObjectKey).Int64("size", rec.SizeBytes).
		Int("durationSec", rec.DurationSec).Msg("recording: ready")
	return nil
}

// OnEgressFailed marks the recording of the call in roomName failed.
func (s *Service) OnEgressFailed(ctx context.Context, roomName, egressID, reason string) error {
	call, err := s.callByRoom(ctx, roomName)
	if err != nil || call == nil {
		return err
	}
	prev := call.Recording
	if prev != nil && (prev.Status == StatusReady || prev.Status == StatusDeleted) {
		return nil
	}
	rec := &domain.RecordingInfo{EgressID: egressID}
	if prev != nil {
		*rec = *prev
		if egressID != "" {
			rec.EgressID = egressID
		}
	}
	return s.markFailed(ctx, call, rec, reason)
}

func (s *Service) markFailed(ctx context.Context, call *domain.Call, rec *domain.RecordingInfo, reason string) error {
	now := s.now()
	rec.Status = StatusFailed
	rec.EndedAt = &now
	s.log.Warn().Str("callId", call.ID.String()).Str("egressId", rec.EgressID).Str("reason", reason).Msg("recording: failed")
	return s.save(ctx, call, rec)
}

// SignedURL returns a short-lived URL of a ready recording. It fails with
// domain.ErrNotFound when the call has no ready recording.
func (s *Service) SignedURL(ctx context.Context, callID uuid.UUID) (string, time.Time, error) {
	call, err := s.Call(ctx, callID)
	if err != nil {
		return "", time.Time{}, err
	}
	r := call.Recording
	if r == nil || r.Status != StatusReady || r.ObjectKey == "" {
		return "", time.Time{}, fmt.Errorf("recording of call %s: %w", callID, domain.ErrNotFound)
	}
	if s.store == nil {
		return "", time.Time{}, errors.New("recording: object store not configured")
	}
	exp := s.now().Add(s.cfg.SignedTTL)
	u, err := s.store.SignedURL(ctx, r.ObjectKey, s.cfg.SignedTTL)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("recording: sign url: %w", err)
	}
	return u, exp, nil
}

// Delete removes a call's recording object and marks it deleted. A running
// egress is stopped first. domain.ErrNotFound when there is none.
func (s *Service) Delete(ctx context.Context, callID uuid.UUID) error {
	call, err := s.Call(ctx, callID)
	if err != nil {
		return err
	}
	return s.deleteRecording(ctx, call)
}

func (s *Service) deleteRecording(ctx context.Context, call *domain.Call) error {
	r := call.Recording
	if r == nil || r.Status == StatusDeleted {
		return fmt.Errorf("recording of call %s: %w", call.ID, domain.ErrNotFound)
	}
	if r.Status == StatusRecording && r.EgressID != "" && s.egress != nil {
		if err := s.egress.StopRecording(ctx, r.EgressID); err != nil {
			s.log.Warn().Err(err).Str("egressId", r.EgressID).Msg("recording: stop egress before delete")
		}
	}
	if r.ObjectKey != "" && s.store != nil {
		if err := s.store.Delete(ctx, r.ObjectKey); err != nil {
			return fmt.Errorf("recording: delete object: %w", err)
		}
	}
	rec := *r
	now := s.now()
	rec.Status = StatusDeleted
	rec.SizeBytes = 0
	if rec.EndedAt == nil {
		rec.EndedAt = &now
	}
	return s.save(ctx, call, &rec)
}

// RunRetention deletes recordings older than their org's retention period
// and returns how many were deleted. Failures of single recordings are
// logged and skipped.
func (s *Service) RunRetention(ctx context.Context) (int, error) {
	now := s.now()
	cursor := now.Add(-24 * time.Hour) // retention is at least one day
	days := map[uuid.UUID]int{}
	deleted := 0
	for page := 0; page < maxRetentionPages; page++ {
		calls, err := s.calls.ListCallsForRetention(ctx, cursor, retentionPage)
		if err != nil {
			return deleted, fmt.Errorf("recording: list calls for retention: %w", err)
		}
		prevCursor := cursor
		for i := range calls {
			c := &calls[i]
			if c.StartedAt.Before(cursor) {
				cursor = c.StartedAt
			}
			if c.Recording == nil || c.Recording.Status == StatusDeleted || c.Recording.Status == StatusRecording {
				continue
			}
			d, ok := days[c.OrgID]
			if !ok {
				d = s.retentionDays(ctx, c.OrgID)
				days[c.OrgID] = d
			}
			if !recordedAt(c).Before(now.AddDate(0, 0, -d)) {
				continue
			}
			if err := s.deleteRecording(ctx, c); err != nil {
				s.log.Warn().Err(err).Str("callId", c.ID.String()).Msg("recording: retention delete")
				continue
			}
			deleted++
		}
		if len(calls) < retentionPage || !cursor.Before(prevCursor) {
			break
		}
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
	}
	if deleted > 0 {
		s.log.Info().Int("deleted", deleted).Msg("recording: retention run")
	}
	return deleted, nil
}

// RunRetentionEvery runs RunRetention every interval (default 24h) until ctx
// is done. The first run happens after one minute.
func (s *Service) RunRetentionEvery(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := s.RunRetention(ctx); err != nil && ctx.Err() == nil {
				s.log.Error().Err(err).Msg("recording: retention run failed")
			}
			t.Reset(interval)
		}
	}
}

func (s *Service) retentionDays(ctx context.Context, orgID uuid.UUID) int {
	if s.orgs == nil {
		return s.cfg.RetentionDays
	}
	org, err := s.orgs.GetOrg(ctx, orgID)
	if err != nil || org == nil {
		return s.cfg.RetentionDays
	}
	if d := settingInt(org.Settings, SettingRetentionDays); d > 0 {
		return d
	}
	return s.cfg.RetentionDays
}

func recordedAt(c *domain.Call) time.Time {
	if r := c.Recording; r != nil {
		if r.EndedAt != nil {
			return *r.EndedAt
		}
		if r.StartedAt != nil {
			return *r.StartedAt
		}
	}
	return c.StartedAt
}

func (s *Service) callByRoom(ctx context.Context, roomName string) (*domain.Call, error) {
	if roomName == "" {
		return nil, nil
	}
	call, err := s.calls.GetCallByRoom(ctx, roomName)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("recording: call by room %s: %w", roomName, err)
	}
	return call, nil
}

// save persists rec, updates call in place and publishes call.updated.
func (s *Service) save(ctx context.Context, call *domain.Call, rec *domain.RecordingInfo) error {
	if err := s.calls.SetCallRecording(ctx, call.ID, rec); err != nil {
		return fmt.Errorf("recording: store recording of call %s: %w", call.ID, err)
	}
	call.Recording = rec
	if rec.Status == StatusReady {
		call.RecordingURL = URLPath(call.ID)
	} else {
		call.RecordingURL = ""
	}
	call.UpdatedAt = s.now()
	s.publish(ctx, call)
	return nil
}

func (s *Service) publish(ctx context.Context, call *domain.Call) {
	if s.bus == nil {
		return
	}
	snap := *call
	id := call.ID
	var rec domain.RecordingInfo
	if call.Recording != nil {
		rec = *call.Recording
	}
	s.bus.Publish(ctx, domain.Event{
		ID: uuid.NewString(), Type: domain.EventCallUpdated, OrgID: call.OrgID, CallID: &id, At: s.now(),
		Payload: map[string]any{"call": &snap, "recording": &rec},
	})
}

// ---------------------------------------------------------------------------
// Settings helpers (org.settings is decoded JSON)
// ---------------------------------------------------------------------------

func settingBool(m map[string]any, key string, def bool) bool {
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		if b, err := strconv.ParseBool(strings.TrimSpace(x)); err == nil {
			return b
		}
	case float64:
		return x != 0
	case int:
		return x != 0
	}
	return def
}

func settingInt(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch x := v.(type) {
	case float64:
		if x > 0 && x < math.MaxInt32 {
			return int(x)
		}
	case int:
		return x
	case int64:
		return int(x)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
			return n
		}
	}
	return 0
}
