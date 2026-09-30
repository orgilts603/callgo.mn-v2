package lexicon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

type fakeRepo struct {
	mu        sync.Mutex
	items     []domain.LexiconCorrection
	listCalls int
	hits      [][]uuid.UUID
	listErr   error
	hook      func() // runs inside ListCorrections after the snapshot
}

func (r *fakeRepo) AddCorrection(_ context.Context, c *domain.LexiconCorrection) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, *c)
	return nil
}

func (r *fakeRepo) UpdateCorrection(_ context.Context, c *domain.LexiconCorrection) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.items {
		if r.items[i].ID == c.ID {
			r.items[i] = *c
			return nil
		}
	}
	return domain.ErrNotFound
}

func (r *fakeRepo) ListCorrections(_ context.Context, orgID uuid.UUID) ([]domain.LexiconCorrection, error) {
	r.mu.Lock()
	r.listCalls++
	if r.listErr != nil {
		r.mu.Unlock()
		return nil, r.listErr
	}
	var out []domain.LexiconCorrection
	for _, c := range r.items {
		if c.OrgID == orgID {
			out = append(out, c)
		}
	}
	hook := r.hook
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	return out, nil
}

func (r *fakeRepo) DeleteCorrection(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.items {
		if r.items[i].ID == id {
			r.items = append(r.items[:i], r.items[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

func (r *fakeRepo) IncrementHits(_ context.Context, ids []uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hits = append(r.hits, ids)
	return nil
}

func (r *fakeRepo) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listCalls
}

type fakeBus struct {
	mu     sync.Mutex
	events []domain.Event
}

func (b *fakeBus) Publish(_ context.Context, ev domain.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, ev)
}

func (b *fakeBus) last(t *testing.T) (domain.LexiconCorrection, string) {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	require.NotEmpty(t, b.events)
	ev := b.events[len(b.events)-1]
	assert.Equal(t, domain.EventLexiconUpdated, ev.Type)
	assert.NotEmpty(t, ev.ID)
	p, ok := ev.Payload.(map[string]any)
	require.True(t, ok)
	return p["correction"].(domain.LexiconCorrection), p["action"].(string)
}

func newSvc() (*Service, *fakeRepo, *fakeBus) {
	repo, bus := &fakeRepo{}, &fakeBus{}
	return NewService(repo, bus, zerolog.Nop()), repo, bus
}

func TestService_ApplyCachesAndInvalidates(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := newSvc()
	org := uuid.New()
	repo.items = []domain.LexiconCorrection{{ID: uuid.New(), OrgID: org, Wrong: "нэг", Correct: "1", Scope: domain.ScopeSTT}}

	out, hits, err := svc.Apply(ctx, org, "нэг хоёр")
	require.NoError(t, err)
	assert.Equal(t, "1 хоёр", out)
	assert.Len(t, hits, 1)
	_, _, _ = svc.Apply(ctx, org, "нэг")
	assert.Equal(t, 1, repo.calls(), "second Apply must hit the cache")

	// Change behind the service's back: cache still serves the old matcher.
	repo.mu.Lock()
	repo.items[0].Correct = "нэгэн"
	repo.mu.Unlock()
	out, _, _ = svc.Apply(ctx, org, "нэг")
	assert.Equal(t, "1", out)

	svc.Invalidate(org)
	out, _, _ = svc.Apply(ctx, org, "нэг")
	assert.Equal(t, "нэгэн", out)
	assert.Equal(t, 2, repo.calls())
}

func TestService_CachePerOrg(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := newSvc()
	a, b := uuid.New(), uuid.New()
	repo.items = []domain.LexiconCorrection{
		{ID: uuid.New(), OrgID: a, Wrong: "x", Correct: "A", Scope: domain.ScopeSTT},
		{ID: uuid.New(), OrgID: b, Wrong: "x", Correct: "B", Scope: domain.ScopeSTT},
	}
	oa, _, _ := svc.Apply(ctx, a, "x")
	ob, _, _ := svc.Apply(ctx, b, "x")
	assert.Equal(t, "A", oa)
	assert.Equal(t, "B", ob)
	svc.Invalidate(a)
	_, _, _ = svc.Apply(ctx, b, "x")
	assert.Equal(t, 2, repo.calls(), "invalidating org a must not evict org b")
}

func TestService_TTLExpiry(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := newSvc()
	now := time.Now()
	svc.now = func() time.Time { return now }
	org := uuid.New()

	_, _, _ = svc.Apply(ctx, org, "x")
	now = now.Add(4 * time.Minute)
	_, _, _ = svc.Apply(ctx, org, "x")
	assert.Equal(t, 1, repo.calls())
	now = now.Add(2 * time.Minute)
	_, _, _ = svc.Apply(ctx, org, "x")
	assert.Equal(t, 2, repo.calls(), "entry older than 5 minutes must reload")
}

func TestService_ApplyRepoError(t *testing.T) {
	svc, repo, _ := newSvc()
	repo.listErr = errors.New("db down")
	out, hits, err := svc.Apply(context.Background(), uuid.New(), "текст")
	require.Error(t, err)
	assert.ErrorContains(t, err, "db down")
	assert.Equal(t, "текст", out)
	assert.Nil(t, hits)
}

func TestService_InvalidateDuringLoadDoesNotCacheStale(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := newSvc()
	org := uuid.New()
	repo.items = []domain.LexiconCorrection{{ID: uuid.New(), OrgID: org, Wrong: "x", Correct: "old", Scope: domain.ScopeSTT}}
	repo.hook = func() { svc.Invalidate(org) } // write lands mid-load
	out, _, _ := svc.Apply(ctx, org, "x")
	assert.Equal(t, "old", out)
	repo.hook = nil
	repo.mu.Lock()
	repo.items[0].Correct = "new"
	repo.mu.Unlock()
	out, _, _ = svc.Apply(ctx, org, "x")
	assert.Equal(t, "new", out, "stale matcher must not have been cached")
}

func TestService_UpsertCreateThenDedupeUpdate(t *testing.T) {
	ctx := context.Background()
	svc, repo, bus := newSvc()
	org := uuid.New()
	turn, user := uuid.New(), uuid.New()

	// Prime the cache so we can verify invalidation.
	out, _, _ := svc.Apply(ctx, org, "улаанбатар")
	assert.Equal(t, "улаанбатар", out)

	c, created, err := svc.Upsert(ctx, org, "  улаанбатар ", "Улаанбаатар", domain.ScopeSTT, "", &turn, &user)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "улаанбатар", c.Wrong)
	assert.Equal(t, org, c.OrgID)
	assert.NotEqual(t, uuid.Nil, c.ID)
	assert.Equal(t, &turn, c.SourceTurn)
	got, action := bus.last(t)
	assert.Equal(t, "created", action)
	assert.Equal(t, c.ID, got.ID)

	out, _, _ = svc.Apply(ctx, org, "улаанбатар")
	assert.Equal(t, "Улаанбаатар", out, "upsert must invalidate the cache")

	// Same word, different case: updates in place.
	c.HitCount = 7
	repo.mu.Lock()
	repo.items[0].HitCount = 7
	repo.mu.Unlock()
	c2, created, err := svc.Upsert(ctx, org, "УЛААНБАТАР", "УБ", domain.ScopeBoth, "уб", nil, nil)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, c.ID, c2.ID)
	assert.Equal(t, "УБ", c2.Correct)
	assert.Equal(t, domain.ScopeBoth, c2.Scope)
	assert.Equal(t, "уб", c2.Phonetic)
	assert.Equal(t, 7, c2.HitCount, "hit count preserved")
	assert.Equal(t, &turn, c2.SourceTurn, "nil sourceTurn keeps the stored one")
	_, action = bus.last(t)
	assert.Equal(t, "updated", action)
	assert.Len(t, repo.items, 1)
	assert.Len(t, bus.events, 2)

	out, _, _ = svc.Apply(ctx, org, "улаанбатар")
	assert.Equal(t, "УБ", out)
}

func TestService_UpsertDedupeIsPerOrg(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := newSvc()
	a, b := uuid.New(), uuid.New()
	_, ca, err := svc.Upsert(ctx, a, "x", "y", domain.ScopeBoth, "", nil, nil)
	require.NoError(t, err)
	_, cb, err := svc.Upsert(ctx, b, "x", "z", domain.ScopeBoth, "", nil, nil)
	require.NoError(t, err)
	assert.True(t, ca)
	assert.True(t, cb)
	assert.Len(t, repo.items, 2)
}

func TestService_UpsertDefaultsAndValidation(t *testing.T) {
	ctx := context.Background()
	svc, _, bus := newSvc()
	org := uuid.New()

	c, _, err := svc.Upsert(ctx, org, "a", "b", "", "", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, domain.ScopeBoth, c.Scope)

	for name, args := range map[string][3]string{
		"empty wrong":   {"  ", "b", "stt"},
		"empty correct": {"a2", " ", "stt"},
		"identical":     {"q", "q", "stt"},
		"bad scope":     {"a3", "b", "nope"},
	} {
		_, _, err := svc.Upsert(ctx, org, args[0], args[1], domain.LexiconScope(args[2]), "", nil, nil)
		assert.ErrorIs(t, err, domain.ErrInvalid, name)
	}
	assert.Len(t, bus.events, 1, "invalid input publishes nothing")
}

func TestService_DeleteAndUpdate(t *testing.T) {
	ctx := context.Background()
	svc, repo, bus := newSvc()
	org, other := uuid.New(), uuid.New()
	c1, _, _ := svc.Upsert(ctx, org, "a", "b", domain.ScopeSTT, "", nil, nil)
	c2, _, _ := svc.Upsert(ctx, org, "c", "d", domain.ScopeSTT, "", nil, nil)

	// Update.
	up, err := svc.Update(ctx, org, c1.ID, "a2", "b2", domain.ScopeTTS, "ph")
	require.NoError(t, err)
	assert.Equal(t, "a2", up.Wrong)
	_, action := bus.last(t)
	assert.Equal(t, "updated", action)
	_, err = svc.Update(ctx, org, c1.ID, "C", "x", domain.ScopeSTT, "")
	assert.ErrorIs(t, err, domain.ErrConflict)
	_, err = svc.Update(ctx, org, uuid.New(), "zz", "x", domain.ScopeSTT, "")
	assert.ErrorIs(t, err, domain.ErrNotFound)

	// Delete: wrong org and unknown id are not found.
	assert.ErrorIs(t, svc.Delete(ctx, other, c1.ID), domain.ErrNotFound)
	assert.ErrorIs(t, svc.Delete(ctx, org, uuid.New()), domain.ErrNotFound)

	before := len(bus.events)
	require.NoError(t, svc.Delete(ctx, org, c2.ID))
	got, action := bus.last(t)
	assert.Equal(t, "deleted", action)
	assert.Equal(t, c2.ID, got.ID)
	assert.Len(t, bus.events, before+1)
	assert.Len(t, repo.items, 1)

	out, _, _ := svc.Apply(ctx, org, "c")
	assert.Equal(t, "c", out, "deleted rule no longer applies")
}

func TestService_RecordHits(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := newSvc()
	require.NoError(t, svc.RecordHits(ctx, nil))
	assert.Empty(t, repo.hits)

	a, b := uuid.New(), uuid.New()
	require.NoError(t, svc.RecordHits(ctx, []Hit{{ID: a}, {ID: b}, {ID: a}}))
	require.Len(t, repo.hits, 1)
	assert.Equal(t, []uuid.UUID{a, b}, repo.hits[0])
}

func TestService_Export(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := newSvc()
	org := uuid.New()
	id := uuid.New()
	repo.items = []domain.LexiconCorrection{
		{ID: id, OrgID: org, Wrong: "w", Correct: "c", Scope: domain.ScopeBoth, Phonetic: "p"},
		{ID: uuid.New(), OrgID: uuid.New(), Wrong: "other", Correct: "o", Scope: domain.ScopeSTT},
	}
	es, err := svc.Export(ctx, org)
	require.NoError(t, err)
	assert.Equal(t, []Entry{{Wrong: "w", Correct: "c", Scope: domain.ScopeBoth, Phonetic: "p", ID: id}}, es)

	es, err = svc.Export(ctx, uuid.New())
	require.NoError(t, err)
	assert.NotNil(t, es)
	assert.Empty(t, es)
}

func TestService_NilBus(t *testing.T) {
	svc := NewService(&fakeRepo{}, nil, zerolog.Nop())
	_, created, err := svc.Upsert(context.Background(), uuid.New(), "a", "b", domain.ScopeSTT, "", nil, nil)
	require.NoError(t, err)
	assert.True(t, created)
}

func TestService_ConcurrentUpsertDedupes(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := newSvc()
	org := uuid.New()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := svc.Upsert(ctx, org, "Нэг", "1", domain.ScopeSTT, "", nil, nil)
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	assert.Len(t, repo.items, 1)
}
