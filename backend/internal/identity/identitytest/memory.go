// Package identitytest provides an in-memory implementation of
// domain.IdentityRepository and domain.OrgRepository for tests and demos.
package identitytest

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Memory is a goroutine-safe in-memory identity store. It also implements
// identity.UserDeleter.
type Memory struct {
	mu            sync.Mutex
	Orgs          map[uuid.UUID]domain.Organization
	Users         map[uuid.UUID]domain.User
	Invitations   map[uuid.UUID]domain.Invitation
	Resets        map[uuid.UUID]domain.PasswordReset
	Verifications map[uuid.UUID]domain.EmailVerification
	Sessions      map[uuid.UUID]domain.RefreshSession
	APIKeys       map[uuid.UUID]domain.APIKey
	AuditLog      []domain.AuditEntry
	Now           func() time.Time
}

var (
	_ domain.IdentityRepository = (*Memory)(nil)
	_ domain.OrgRepository      = (*Memory)(nil)
)

// NewMemory returns an empty store.
func NewMemory() *Memory {
	return &Memory{
		Orgs:          map[uuid.UUID]domain.Organization{},
		Users:         map[uuid.UUID]domain.User{},
		Invitations:   map[uuid.UUID]domain.Invitation{},
		Resets:        map[uuid.UUID]domain.PasswordReset{},
		Verifications: map[uuid.UUID]domain.EmailVerification{},
		Sessions:      map[uuid.UUID]domain.RefreshSession{},
		APIKeys:       map[uuid.UUID]domain.APIKey{},
		Now:           func() time.Time { return time.Now().UTC() },
	}
}

func ensureID(id *uuid.UUID) {
	if *id == uuid.Nil {
		*id = uuid.New()
	}
}

// ---- OrgRepository ----

// EnsureDefaultOrg returns (creating) the "demo" org.
func (m *Memory) EnsureDefaultOrg(ctx context.Context) (*domain.Organization, error) {
	if o, err := m.GetOrgBySlug(ctx, "demo"); err == nil {
		return o, nil
	}
	o := &domain.Organization{Name: "CallGo Demo", Slug: "demo", PlanCode: "trial", Status: domain.OrgActive, Timezone: "Asia/Ulaanbaatar"}
	if err := m.CreateOrg(ctx, o); err != nil {
		return nil, err
	}
	return o, nil
}

// GetOrg returns an org by ID.
func (m *Memory) GetOrg(_ context.Context, id uuid.UUID) (*domain.Organization, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.Orgs[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &o, nil
}

// GetOrgBySlug returns an org by slug.
func (m *Memory) GetOrgBySlug(_ context.Context, slug string) (*domain.Organization, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.Orgs {
		if o.Slug == slug {
			return &o, nil
		}
	}
	return nil, domain.ErrNotFound
}

// CreateUser inserts a user; emails are unique (case-insensitive).
func (m *Memory) CreateUser(_ context.Context, u *domain.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ensureID(&u.ID)
	for _, x := range m.Users {
		if strings.EqualFold(x.Email, u.Email) {
			return domain.ErrConflict
		}
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = m.Now()
	}
	u.UpdatedAt = u.CreatedAt
	m.Users[u.ID] = *u
	return nil
}

// GetUserByEmail looks a user up case-insensitively.
func (m *Memory) GetUserByEmail(_ context.Context, email string) (*domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.Users {
		if strings.EqualFold(u.Email, email) {
			return &u, nil
		}
	}
	return nil, domain.ErrNotFound
}

// GetUser returns a user by ID.
func (m *Memory) GetUser(_ context.Context, id uuid.UUID) (*domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.Users[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &u, nil
}

// ListUsers lists an org's users by creation time.
func (m *Memory) ListUsers(_ context.Context, orgID uuid.UUID) ([]domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.User
	for _, u := range m.Users {
		if u.OrgID == orgID {
			out = append(out, u)
		}
	}
	slices.SortFunc(out, func(a, b domain.User) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out, nil
}

// DeleteUser removes a user.
func (m *Memory) DeleteUser(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.Users[id]; !ok {
		return domain.ErrNotFound
	}
	delete(m.Users, id)
	return nil
}

// ---- IdentityRepository: orgs & users ----

// CreateOrg inserts an org; slugs are unique.
func (m *Memory) CreateOrg(_ context.Context, o *domain.Organization) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ensureID(&o.ID)
	for _, x := range m.Orgs {
		if x.Slug == o.Slug || x.ID == o.ID {
			return domain.ErrConflict
		}
	}
	if o.CreatedAt.IsZero() {
		o.CreatedAt = m.Now()
	}
	o.UpdatedAt = o.CreatedAt
	m.Orgs[o.ID] = *o
	return nil
}

// UpdateOrg replaces an org.
func (m *Memory) UpdateOrg(_ context.Context, o *domain.Organization) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.Orgs[o.ID]; !ok {
		return domain.ErrNotFound
	}
	o.UpdatedAt = m.Now()
	m.Orgs[o.ID] = *o
	return nil
}

// ListOrgs lists orgs whose name or slug contains search.
func (m *Memory) ListOrgs(_ context.Context, search string, limit, offset int) ([]domain.Organization, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []domain.Organization
	q := strings.ToLower(search)
	for _, o := range m.Orgs {
		if q == "" || strings.Contains(strings.ToLower(o.Name), q) || strings.Contains(o.Slug, q) {
			all = append(all, o)
		}
	}
	slices.SortFunc(all, func(a, b domain.Organization) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return page(all, limit, offset), len(all), nil
}

// UpdateUser replaces a user.
func (m *Memory) UpdateUser(_ context.Context, u *domain.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.Users[u.ID]; !ok {
		return domain.ErrNotFound
	}
	u.UpdatedAt = m.Now()
	m.Users[u.ID] = *u
	return nil
}

// CountUsers counts an org's users that are not disabled.
func (m *Memory) CountUsers(_ context.Context, orgID uuid.UUID) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, u := range m.Users {
		if u.OrgID == orgID && u.Status != domain.UserDisabled {
			n++
		}
	}
	return n, nil
}

// ---- invitations ----

// CreateInvitation inserts an invitation.
func (m *Memory) CreateInvitation(_ context.Context, inv *domain.Invitation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ensureID(&inv.ID)
	if inv.CreatedAt.IsZero() {
		inv.CreatedAt = m.Now()
	}
	m.Invitations[inv.ID] = *inv
	return nil
}

// GetInvitationByHash finds an invitation by token hash.
func (m *Memory) GetInvitationByHash(_ context.Context, h string) (*domain.Invitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, inv := range m.Invitations {
		if inv.TokenHash == h {
			return &inv, nil
		}
	}
	return nil, domain.ErrNotFound
}

// ListInvitations lists an org's invitations, newest first.
func (m *Memory) ListInvitations(_ context.Context, orgID uuid.UUID) ([]domain.Invitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Invitation
	for _, inv := range m.Invitations {
		if inv.OrgID == orgID {
			out = append(out, inv)
		}
	}
	slices.SortFunc(out, func(a, b domain.Invitation) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out, nil
}

// DeleteInvitation removes an invitation.
func (m *Memory) DeleteInvitation(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.Invitations[id]; !ok {
		return domain.ErrNotFound
	}
	delete(m.Invitations, id)
	return nil
}

// MarkInvitationAccepted sets AcceptedAt.
func (m *Memory) MarkInvitationAccepted(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.Invitations[id]
	if !ok {
		return domain.ErrNotFound
	}
	now := m.Now()
	inv.AcceptedAt = &now
	m.Invitations[id] = inv
	return nil
}

// ---- password resets ----

// CreatePasswordReset inserts a reset token.
func (m *Memory) CreatePasswordReset(_ context.Context, r *domain.PasswordReset) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ensureID(&r.ID)
	if r.CreatedAt.IsZero() {
		r.CreatedAt = m.Now()
	}
	m.Resets[r.ID] = *r
	return nil
}

// GetPasswordResetByHash finds a reset by token hash.
func (m *Memory) GetPasswordResetByHash(_ context.Context, h string) (*domain.PasswordReset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.Resets {
		if r.TokenHash == h {
			return &r, nil
		}
	}
	return nil, domain.ErrNotFound
}

// MarkPasswordResetUsed sets UsedAt.
func (m *Memory) MarkPasswordResetUsed(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.Resets[id]
	if !ok {
		return domain.ErrNotFound
	}
	now := m.Now()
	r.UsedAt = &now
	m.Resets[id] = r
	return nil
}

// ---- email verifications ----

// CreateEmailVerification inserts a verification token.
func (m *Memory) CreateEmailVerification(_ context.Context, v *domain.EmailVerification) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ensureID(&v.ID)
	m.Verifications[v.ID] = *v
	return nil
}

// GetEmailVerificationByHash finds a verification by token hash.
func (m *Memory) GetEmailVerificationByHash(_ context.Context, h string) (*domain.EmailVerification, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range m.Verifications {
		if v.TokenHash == h {
			return &v, nil
		}
	}
	return nil, domain.ErrNotFound
}

// MarkEmailVerificationUsed sets UsedAt.
func (m *Memory) MarkEmailVerificationUsed(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.Verifications[id]
	if !ok {
		return domain.ErrNotFound
	}
	now := m.Now()
	v.UsedAt = &now
	m.Verifications[id] = v
	return nil
}

// ---- refresh sessions ----

// CreateRefreshSession inserts a session.
func (m *Memory) CreateRefreshSession(_ context.Context, s *domain.RefreshSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ensureID(&s.ID)
	if s.CreatedAt.IsZero() {
		s.CreatedAt = m.Now()
	}
	m.Sessions[s.ID] = *s
	return nil
}

// GetRefreshSessionByHash finds a session by its current token hash.
func (m *Memory) GetRefreshSessionByHash(_ context.Context, h string) (*domain.RefreshSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.Sessions {
		if s.TokenHash == h {
			return &s, nil
		}
	}
	return nil, domain.ErrNotFound
}

// RotateRefreshSession replaces the token hash and extends the session.
func (m *Memory) RotateRefreshSession(_ context.Context, id uuid.UUID, newHash string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.Sessions[id]
	if !ok {
		return domain.ErrNotFound
	}
	s.TokenHash = newHash
	s.ExpiresAt = expiresAt
	s.LastUsed = m.Now()
	m.Sessions[id] = s
	return nil
}

// RevokeRefreshSession revokes one session.
func (m *Memory) RevokeRefreshSession(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.Sessions[id]
	if !ok {
		return domain.ErrNotFound
	}
	if s.RevokedAt == nil {
		now := m.Now()
		s.RevokedAt = &now
		m.Sessions[id] = s
	}
	return nil
}

// RevokeUserSessions revokes every session of a user.
func (m *Memory) RevokeUserSessions(_ context.Context, userID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.Now()
	for id, s := range m.Sessions {
		if s.UserID == userID && s.RevokedAt == nil {
			s.RevokedAt = &now
			m.Sessions[id] = s
		}
	}
	return nil
}

// ListRefreshSessions lists a user's sessions, newest first.
func (m *Memory) ListRefreshSessions(_ context.Context, userID uuid.UUID) ([]domain.RefreshSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.RefreshSession
	for _, s := range m.Sessions {
		if s.UserID == userID {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b domain.RefreshSession) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out, nil
}

// ---- API keys ----

// CreateAPIKey inserts a key; prefixes are unique.
func (m *Memory) CreateAPIKey(_ context.Context, k *domain.APIKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ensureID(&k.ID)
	for _, x := range m.APIKeys {
		if x.Prefix == k.Prefix {
			return domain.ErrConflict
		}
	}
	if k.CreatedAt.IsZero() {
		k.CreatedAt = m.Now()
	}
	m.APIKeys[k.ID] = *k
	return nil
}

// GetAPIKeyByPrefix finds a key (revoked ones included).
func (m *Memory) GetAPIKeyByPrefix(_ context.Context, prefix string) (*domain.APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range m.APIKeys {
		if k.Prefix == prefix {
			return &k, nil
		}
	}
	return nil, domain.ErrNotFound
}

// ListAPIKeys lists an org's keys, newest first.
func (m *Memory) ListAPIKeys(_ context.Context, orgID uuid.UUID) ([]domain.APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.APIKey
	for _, k := range m.APIKeys {
		if k.OrgID == orgID {
			out = append(out, k)
		}
	}
	slices.SortFunc(out, func(a, b domain.APIKey) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out, nil
}

// RevokeAPIKey sets RevokedAt.
func (m *Memory) RevokeAPIKey(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.APIKeys[id]
	if !ok {
		return domain.ErrNotFound
	}
	if k.RevokedAt == nil {
		now := m.Now()
		k.RevokedAt = &now
		m.APIKeys[id] = k
	}
	return nil
}

// TouchAPIKey sets LastUsedAt.
func (m *Memory) TouchAPIKey(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.APIKeys[id]
	if !ok {
		return domain.ErrNotFound
	}
	now := m.Now()
	k.LastUsedAt = &now
	m.APIKeys[id] = k
	return nil
}

// ---- audit ----

// AppendAudit appends an entry.
func (m *Memory) AppendAudit(_ context.Context, e *domain.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ensureID(&e.ID)
	if e.At.IsZero() {
		e.At = m.Now()
	}
	m.AuditLog = append(m.AuditLog, *e)
	return nil
}

// ListAudit filters an org's entries, newest first.
func (m *Memory) ListAudit(_ context.Context, orgID uuid.UUID, f domain.AuditFilter) ([]domain.AuditEntry, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.AuditEntry
	for _, e := range m.AuditLog {
		switch {
		case e.OrgID != orgID,
			f.ActorID != nil && (e.ActorID == nil || *e.ActorID != *f.ActorID),
			f.Action != "" && e.Action != f.Action,
			f.From != nil && e.At.Before(*f.From),
			f.To != nil && !e.At.Before(*f.To):
			continue
		}
		out = append(out, e)
	}
	slices.SortStableFunc(out, func(a, b domain.AuditEntry) int { return b.At.Compare(a.At) })
	return page(out, f.Limit, f.Offset), len(out), nil
}

// Actions returns the audit actions recorded so far (oldest first).
func (m *Memory) Actions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.AuditLog))
	for _, e := range m.AuditLog {
		out = append(out, e.Action)
	}
	return out
}

func page[T any](all []T, limit, offset int) []T {
	if offset >= len(all) {
		return nil
	}
	all = all[max(offset, 0):]
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}
	return all
}
