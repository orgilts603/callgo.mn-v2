package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// rtNumbers is a SIP number repository (only the methods the routing and
// callback handlers use).
type rtNumbers struct {
	domain.SIPNumberRepository
	mu    sync.Mutex
	items map[uuid.UUID]domain.SIPNumber
}

func (f *rtNumbers) GetSIPNumber(_ context.Context, id uuid.UUID) (*domain.SIPNumber, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.items[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &n, nil
}

func (f *rtNumbers) UpdateSIPNumber(_ context.Context, n *domain.SIPNumber) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[n.ID] = *n
	return nil
}

func (f *rtNumbers) get(id uuid.UUID) domain.SIPNumber {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.items[id]
}

type rtProfiles struct {
	domain.AgentProfileRepository
	items []domain.AgentProfile
}

func (f *rtProfiles) ListAgentProfiles(_ context.Context, orgID uuid.UUID) ([]domain.AgentProfile, error) {
	var out []domain.AgentProfile
	for _, p := range f.items {
		if p.OrgID == orgID {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *rtProfiles) GetAgentProfile(_ context.Context, id uuid.UUID) (*domain.AgentProfile, error) {
	for _, p := range f.items {
		if p.ID == id {
			return &p, nil
		}
	}
	return nil, domain.ErrNotFound
}

type rtOrgs struct {
	domain.OrgRepository
	orgs map[uuid.UUID]domain.Organization
}

func (f *rtOrgs) GetOrg(_ context.Context, id uuid.UUID) (*domain.Organization, error) {
	o, ok := f.orgs[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &o, nil
}

type rtContacts struct{ domain.ContactRepository }

func (rtContacts) GetContactByPhone(context.Context, uuid.UUID, string) (*domain.Contact, error) {
	return nil, domain.ErrNotFound
}

func (rtContacts) GetContact(context.Context, uuid.UUID) (*domain.Contact, error) {
	return nil, domain.ErrNotFound
}

// rtRepo is the callback half of an IntegrationsRepository.
type rtRepo struct {
	domain.IntegrationsRepository
	mu    sync.Mutex
	items map[uuid.UUID]domain.CallbackRequest
	order []uuid.UUID
}

func newRTRepo() *rtRepo { return &rtRepo{items: map[uuid.UUID]domain.CallbackRequest{}} }

func (f *rtRepo) CreateCallback(_ context.Context, c *domain.CallbackRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[c.ID] = *c
	f.order = append(f.order, c.ID)
	return nil
}

func (f *rtRepo) UpdateCallback(_ context.Context, c *domain.CallbackRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[c.ID] = *c
	return nil
}

func (f *rtRepo) GetCallback(_ context.Context, id uuid.UUID) (*domain.CallbackRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.items[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &c, nil
}

func (f *rtRepo) ListCallbacks(_ context.Context, orgID uuid.UUID, status domain.CallbackStatus, limit, offset int) ([]domain.CallbackRequest, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.CallbackRequest
	for _, id := range f.order {
		c := f.items[id]
		if c.OrgID == orgID && (status == "" || c.Status == status) {
			out = append(out, c)
		}
	}
	total := len(out)
	if offset > len(out) {
		offset = len(out)
	}
	out = out[offset:]
	if limit < len(out) {
		out = out[:limit]
	}
	return out, total, nil
}

// rtHardRepo adds hard deletion to rtRepo.
type rtHardRepo struct {
	*rtRepo
	deleted []uuid.UUID
}

func (f *rtHardRepo) DeleteCallback(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.items, id)
	f.deleted = append(f.deleted, id)
	return nil
}

type rtBus struct {
	mu     sync.Mutex
	events []domain.Event
}

func (b *rtBus) Publish(_ context.Context, ev domain.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, ev)
}

type rtAudit struct {
	mu      sync.Mutex
	entries []domain.AuditEntry
}

func (a *rtAudit) Append(_ context.Context, e *domain.AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, *e)
	return nil
}

func (a *rtAudit) actions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, e := range a.entries {
		out = append(out, e.Action)
	}
	return out
}

// rtWorld holds two orgs and the users acting on them.
type rtWorld struct {
	org, org2 domain.Organization
	admin     auth.Claims
	operator  auth.Claims
	foreign   auth.Claims
	profile   domain.AgentProfile
	profile2  domain.AgentProfile // org2's
	number    domain.SIPNumber
	numbers   *rtNumbers
	profiles  *rtProfiles
	orgs      *rtOrgs
	audit     *rtAudit
}

func newRTWorld() *rtWorld {
	w := &rtWorld{audit: &rtAudit{}}
	w.org = domain.Organization{ID: uuid.New(), Name: "Demo", Timezone: "Asia/Ulaanbaatar"}
	w.org2 = domain.Organization{ID: uuid.New(), Name: "Other", Timezone: "Asia/Ulaanbaatar"}
	w.admin = auth.Claims{UserID: uuid.New(), OrgID: w.org.ID, Role: domain.RoleAdmin}
	w.operator = auth.Claims{UserID: uuid.New(), OrgID: w.org.ID, Role: domain.RoleOperator}
	w.foreign = auth.Claims{UserID: uuid.New(), OrgID: w.org2.ID, Role: domain.RoleOwner}
	w.profile = domain.AgentProfile{ID: uuid.New(), OrgID: w.org.ID, Name: "Sales"}
	w.profile2 = domain.AgentProfile{ID: uuid.New(), OrgID: w.org2.ID, Name: "Foreign"}
	w.number = domain.SIPNumber{ID: uuid.New(), OrgID: w.org.ID, Number: "+97677001234", Active: true,
		AllowInbound: true, AllowOutbound: true, AgentProfileID: &w.profile.ID}
	w.numbers = &rtNumbers{items: map[uuid.UUID]domain.SIPNumber{w.number.ID: w.number}}
	w.profiles = &rtProfiles{items: []domain.AgentProfile{w.profile, w.profile2}}
	w.orgs = &rtOrgs{orgs: map[uuid.UUID]domain.Organization{w.org.ID: w.org, w.org2.ID: w.org2}}
	return w
}

// rtRouter builds a router that authenticates requests from the X-Test-Actor
// header ("admin", "operator", "foreign"; none = unauthenticated 401).
func (w *rtWorld) rtRouter(mount func(chi.Router)) http.Handler {
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
				var c auth.Claims
				switch req.Header.Get("X-Test-Actor") {
				case "admin":
					c = w.admin
				case "operator":
					c = w.operator
				case "foreign":
					c = w.foreign
				default:
					auth.WriteError(rw, http.StatusUnauthorized, "unauthorized", "missing bearer token")
					return
				}
				next.ServeHTTP(rw, req.WithContext(auth.WithClaims(req.Context(), c)))
			})
		})
		mount(r)
	})
	return r
}

type rtResponse struct {
	Code int
	Raw  []byte
}

func (r rtResponse) decode(t *testing.T, v any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(r.Raw, v), "body: %s", r.Raw)
}

func (r rtResponse) errCode(t *testing.T) string {
	t.Helper()
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	r.decode(t, &e)
	return e.Error.Code
}

func rtDo(t *testing.T, h http.Handler, actor, method, path string, body any) rtResponse {
	t.Helper()
	var rd *bytes.Reader
	switch b := body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case string:
		rd = bytes.NewReader([]byte(b))
	default:
		raw, err := json.Marshal(b)
		require.NoError(t, err)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	if actor != "" {
		req.Header.Set("X-Test-Actor", actor)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rtResponse{Code: rec.Code, Raw: rec.Body.Bytes()}
}

var rtNow = time.Date(2026, 9, 30, 3, 0, 0, 0, time.UTC) // Wednesday 11:00 in Ulaanbaatar

func rtLog() zerolog.Logger { return zerolog.Nop() }
