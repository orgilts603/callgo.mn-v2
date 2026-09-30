package lexicon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// DefaultTTL is the safety expiry of a cached per-organisation matcher. The
// cache is invalidated explicitly on every write made through the Service; the
// TTL only covers writes done elsewhere (other replicas, manual SQL).
const DefaultTTL = 5 * time.Minute

// Entry is the agent-facing export shape of a correction (bootstrap payload).
type Entry struct {
	Wrong    string              `json:"wrong"`
	Correct  string              `json:"correct"`
	Scope    domain.LexiconScope `json:"scope"`
	Phonetic string              `json:"phonetic"`
	ID       uuid.UUID           `json:"id"`
}

type cacheItem struct {
	m        *Matcher
	loadedAt time.Time
}

// Service is the application layer of the lexicon: cached matching, CRUD with
// dedupe, hit accounting and live-event publishing.
type Service struct {
	repo domain.LexiconRepository
	bus  domain.EventBus
	log  zerolog.Logger

	ttl time.Duration
	now func() time.Time

	mu    sync.RWMutex
	cache map[uuid.UUID]cacheItem
	gen   map[uuid.UUID]uint64 // bumped by Invalidate; guards stale loads

	writeMu sync.Mutex // serialises Upsert/Update/Delete (dedupe correctness)
}

// NewService builds a Service. bus may be nil (events are then skipped).
func NewService(repo domain.LexiconRepository, bus domain.EventBus, log zerolog.Logger) *Service {
	return &Service{
		repo:  repo,
		bus:   bus,
		log:   log.With().Str("component", "lexicon").Logger(),
		ttl:   DefaultTTL,
		now:   time.Now,
		cache: make(map[uuid.UUID]cacheItem),
		gen:   make(map[uuid.UUID]uint64),
	}
}

// Matcher returns the (cached) compiled matcher of an organisation.
func (s *Service) Matcher(ctx context.Context, orgID uuid.UUID) (*Matcher, error) {
	s.mu.RLock()
	it, ok := s.cache[orgID]
	gen := s.gen[orgID]
	s.mu.RUnlock()
	if ok && s.now().Sub(it.loadedAt) < s.ttl {
		return it.m, nil
	}

	entries, err := s.repo.ListCorrections(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("lexicon: load corrections: %w", err)
	}
	m := Compile(entries)

	s.mu.Lock()
	// Only cache when no Invalidate happened while we were loading.
	if s.gen[orgID] == gen {
		s.cache[orgID] = cacheItem{m: m, loadedAt: s.now()}
	}
	s.mu.Unlock()
	return m, nil
}

// Apply runs the organisation's STT corrections over text.
func (s *Service) Apply(ctx context.Context, orgID uuid.UUID, text string) (string, []Hit, error) {
	m, err := s.Matcher(ctx, orgID)
	if err != nil {
		return text, nil, err
	}
	out, hits := m.Apply(text)
	return out, hits, nil
}

// Invalidate drops the cached matcher of an organisation.
func (s *Service) Invalidate(orgID uuid.UUID) {
	s.mu.Lock()
	delete(s.cache, orgID)
	s.gen[orgID]++
	s.mu.Unlock()
}

// List returns every correction of an organisation (uncached).
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]domain.LexiconCorrection, error) {
	items, err := s.repo.ListCorrections(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("lexicon: list: %w", err)
	}
	return items, nil
}

// Upsert creates a correction, or updates the existing one of the same
// organisation whose Wrong equals wrong case-insensitively. created reports
// which happened. Publishes lexicon.updated (action created|updated).
// sourceTurn / createdBy only overwrite the stored values when non-nil.
func (s *Service) Upsert(ctx context.Context, orgID uuid.UUID, wrong, correct string, scope domain.LexiconScope, phonetic string, sourceTurn *uuid.UUID, createdBy *uuid.UUID) (*domain.LexiconCorrection, bool, error) {
	wrong, correct, scope, phonetic, err := normalize(wrong, correct, scope, phonetic)
	if err != nil {
		return nil, false, err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	existing, err := s.repo.ListCorrections(ctx, orgID)
	if err != nil {
		return nil, false, fmt.Errorf("lexicon: upsert: %w", err)
	}
	var found *domain.LexiconCorrection
	for i := range existing {
		if strings.EqualFold(strings.Join(strings.Fields(existing[i].Wrong), " "), wrong) {
			found = &existing[i]
			break
		}
	}

	var (
		c       domain.LexiconCorrection
		created bool
		action  string
	)
	if found != nil {
		c = *found
		c.Wrong = wrong
		c.Correct = correct
		c.Scope = scope
		c.Phonetic = phonetic
		if sourceTurn != nil {
			c.SourceTurn = sourceTurn
		}
		if createdBy != nil {
			c.CreatedBy = createdBy
		}
		if err := s.repo.UpdateCorrection(ctx, &c); err != nil {
			return nil, false, fmt.Errorf("lexicon: update correction: %w", err)
		}
		action = "updated"
	} else {
		c = domain.LexiconCorrection{
			ID:         uuid.New(),
			OrgID:      orgID,
			Wrong:      wrong,
			Correct:    correct,
			Phonetic:   phonetic,
			Scope:      scope,
			SourceTurn: sourceTurn,
			CreatedBy:  createdBy,
			CreatedAt:  s.now().UTC(),
		}
		if err := s.repo.AddCorrection(ctx, &c); err != nil {
			return nil, false, fmt.Errorf("lexicon: add correction: %w", err)
		}
		created = true
		action = "created"
	}

	s.Invalidate(orgID)
	s.publish(ctx, orgID, action, c)
	return &c, created, nil
}

// Update edits the correction id (PUT /api/lexicon/{id}). It returns
// domain.ErrNotFound when id is not in the organisation and domain.ErrConflict
// when the new Wrong collides with another entry.
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, wrong, correct string, scope domain.LexiconScope, phonetic string) (*domain.LexiconCorrection, error) {
	wrong, correct, scope, phonetic, err := normalize(wrong, correct, scope, phonetic)
	if err != nil {
		return nil, err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	existing, err := s.repo.ListCorrections(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("lexicon: update: %w", err)
	}
	var found *domain.LexiconCorrection
	for i := range existing {
		if existing[i].ID == id {
			found = &existing[i]
			continue
		}
		if strings.EqualFold(strings.Join(strings.Fields(existing[i].Wrong), " "), wrong) {
			return nil, fmt.Errorf("lexicon: %q already exists: %w", wrong, domain.ErrConflict)
		}
	}
	if found == nil {
		return nil, fmt.Errorf("lexicon: correction %s: %w", id, domain.ErrNotFound)
	}
	c := *found
	c.Wrong, c.Correct, c.Scope, c.Phonetic = wrong, correct, scope, phonetic
	if err := s.repo.UpdateCorrection(ctx, &c); err != nil {
		return nil, fmt.Errorf("lexicon: update correction: %w", err)
	}
	s.Invalidate(orgID)
	s.publish(ctx, orgID, "updated", c)
	return &c, nil
}

// Delete removes a correction of the organisation and publishes
// lexicon.updated with action "deleted". Unknown ids (or ids of another
// organisation) return domain.ErrNotFound.
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	existing, err := s.repo.ListCorrections(ctx, orgID)
	if err != nil {
		return fmt.Errorf("lexicon: delete: %w", err)
	}
	var found *domain.LexiconCorrection
	for i := range existing {
		if existing[i].ID == id {
			found = &existing[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("lexicon: correction %s: %w", id, domain.ErrNotFound)
	}
	if err := s.repo.DeleteCorrection(ctx, id); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.Invalidate(orgID)
		}
		return fmt.Errorf("lexicon: delete correction: %w", err)
	}
	s.Invalidate(orgID)
	s.publish(ctx, orgID, "deleted", *found)
	return nil
}

// RecordHits increments the hit counters of the entries that fired. Duplicate
// ids are collapsed; an empty slice is a no-op.
func (s *Service) RecordHits(ctx context.Context, hits []Hit) error {
	if len(hits) == 0 {
		return nil
	}
	seen := make(map[uuid.UUID]struct{}, len(hits))
	ids := make([]uuid.UUID, 0, len(hits))
	for _, h := range hits {
		if _, dup := seen[h.ID]; dup {
			continue
		}
		seen[h.ID] = struct{}{}
		ids = append(ids, h.ID)
	}
	if err := s.repo.IncrementHits(ctx, ids); err != nil {
		return fmt.Errorf("lexicon: record hits: %w", err)
	}
	return nil
}

// Export lists every correction of the organisation in the shape the agent
// bootstrap endpoint returns (all scopes; the agent filters by scope itself).
func (s *Service) Export(ctx context.Context, orgID uuid.UUID) ([]Entry, error) {
	items, err := s.repo.ListCorrections(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("lexicon: export: %w", err)
	}
	out := make([]Entry, 0, len(items))
	for _, c := range items {
		out = append(out, Entry{Wrong: c.Wrong, Correct: c.Correct, Scope: c.Scope, Phonetic: c.Phonetic, ID: c.ID})
	}
	return out, nil
}

func (s *Service) publish(ctx context.Context, orgID uuid.UUID, action string, c domain.LexiconCorrection) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(ctx, domain.Event{
		ID:    uuid.NewString(),
		Type:  domain.EventLexiconUpdated,
		OrgID: orgID,
		At:    s.now().UTC(),
		Payload: map[string]any{
			"correction": c,
			"action":     action,
		},
	})
}

func normalize(wrong, correct string, scope domain.LexiconScope, phonetic string) (string, string, domain.LexiconScope, string, error) {
	wrong = strings.Join(strings.Fields(wrong), " ")
	correct = strings.TrimSpace(correct)
	phonetic = strings.TrimSpace(phonetic)
	if scope == "" {
		scope = domain.ScopeBoth
	}
	switch {
	case wrong == "":
		return "", "", "", "", fmt.Errorf("lexicon: wrong is required: %w", domain.ErrInvalid)
	case correct == "":
		return "", "", "", "", fmt.Errorf("lexicon: correct is required: %w", domain.ErrInvalid)
	case wrong == correct:
		return "", "", "", "", fmt.Errorf("lexicon: wrong and correct are identical: %w", domain.ErrInvalid)
	}
	switch scope {
	case domain.ScopeSTT, domain.ScopeTTS, domain.ScopeBoth:
	default:
		return "", "", "", "", fmt.Errorf("lexicon: unknown scope %q: %w", scope, domain.ErrInvalid)
	}
	return wrong, correct, scope, phonetic, nil
}
