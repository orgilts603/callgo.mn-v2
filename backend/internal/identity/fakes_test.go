package identity

import (
	"context"
	"net/url"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/identity/identitytest"
)

type sentMail struct{ to, subject, text, html string }

type captureMailer struct {
	mu   sync.Mutex
	sent []sentMail
}

func (m *captureMailer) Send(_ context.Context, to, subject, text, html string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sentMail{to, subject, text, html})
	return nil
}

func (m *captureMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

var tokenRe = regexp.MustCompile(`(/[a-z-]+)\?token=([^\s"<]+)`)

// lastToken returns the token of the newest mail to `to` whose link path is path.
func (m *captureMailer) lastToken(t *testing.T, to, path string) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.sent) - 1; i >= 0; i-- {
		if m.sent[i].to != to {
			continue
		}
		for _, match := range tokenRe.FindAllStringSubmatch(m.sent[i].text, -1) {
			if match[1] == path {
				tok, err := url.QueryUnescape(match[2])
				require.NoError(t, err)
				return tok
			}
		}
	}
	t.Fatalf("no %s mail to %s", path, to)
	return ""
}

type fakeSubs struct {
	mu     sync.Mutex
	subs   map[uuid.UUID]*domain.Subscription
	failOn bool
}

func (f *fakeSubs) StartTrial(_ context.Context, orgID uuid.UUID) (*domain.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOn {
		return nil, context.DeadlineExceeded
	}
	now := time.Now().UTC()
	end := now.Add(14 * 24 * time.Hour)
	s := &domain.Subscription{ID: uuid.New(), OrgID: orgID, PlanCode: "trial", Status: domain.SubTrialing,
		CurrentPeriodStart: now, CurrentPeriodEnd: end, TrialEndsAt: &end, CreatedAt: now, UpdatedAt: now}
	f.subs[orgID] = s
	return s, nil
}

func (f *fakeSubs) get(_ context.Context, orgID uuid.UUID) (*domain.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.subs[orgID]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return s, nil
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fixture struct {
	svc   *Service
	repo  *identitytest.Memory
	mail  *captureMailer
	subs  *fakeSubs
	clock *clock
	plan  domain.Plan
}

var testSecret = []byte("identity-test-secret")

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		repo:  identitytest.NewMemory(),
		mail:  &captureMailer{},
		subs:  &fakeSubs{subs: map[uuid.UUID]*domain.Subscription{}},
		clock: &clock{now: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)},
		plan:  domain.Plan{Code: "trial", MaxUsers: 2, Features: []string{}},
	}
	f.repo.Now = f.clock.Now
	f.svc = New(f.repo, f.repo, f.mail, f.subs, Config{AppURL: "https://app.callgo.mn", AllowSignup: true, JWTSecret: testSecret}, zerolog.Nop())
	f.svc.now = f.clock.Now
	f.svc.Limits = func(context.Context, uuid.UUID) (domain.Plan, error) { return f.plan, nil }
	f.svc.Subscription = f.subs.get
	return f
}

var client = Client{IP: "203.0.113.7", UserAgent: "test-agent"}

func (f *fixture) signup(t *testing.T, org, email string) *AuthResult {
	t.Helper()
	res, err := f.svc.Signup(context.Background(), SignupInput{OrgName: org, Email: email, Password: "password1", Name: "Owner " + org}, client)
	require.NoError(t, err)
	return res
}

// addMember invites and accepts a member with role.
func (f *fixture) addMember(t *testing.T, owner *AuthResult, email string, role domain.Role) *AuthResult {
	t.Helper()
	ctx := context.Background()
	prev := f.plan.MaxUsers
	f.plan.MaxUsers = 0
	defer func() { f.plan.MaxUsers = prev }()
	_, err := f.svc.Invite(ctx, actorOf(owner), email, role)
	require.NoError(t, err)
	res, err := f.svc.AcceptInvitation(ctx, AcceptInvitationInput{Token: f.mail.lastToken(t, email, "/accept-invitation"), Name: "Member", Password: "password1"}, client)
	require.NoError(t, err)
	return res
}

func actorOf(r *AuthResult) Actor {
	return Actor{UserID: r.User.ID, OrgID: r.Org.ID, Role: r.User.Role, Email: r.User.Email}
}
