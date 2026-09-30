package httpapi

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// fakeDB implements every repository port in memory. All getters return copies.
type fakeDB struct {
	mu        sync.Mutex
	orgs      map[uuid.UUID]domain.Organization
	users     map[uuid.UUID]domain.User
	numbers   map[uuid.UUID]domain.SIPNumber
	profiles  map[uuid.UUID]domain.AgentProfile
	llms      map[uuid.UUID]domain.LLMConfig
	calls     map[uuid.UUID]domain.Call
	turns     map[uuid.UUID][]domain.TranscriptTurn
	contacts  map[uuid.UUID]domain.Contact
	campaigns map[uuid.UUID]domain.Campaign
	targets   map[uuid.UUID][]domain.CampaignTarget
	lexicon   map[uuid.UUID]domain.LexiconCorrection
	hits      []uuid.UUID
}

func newFakeDB() *fakeDB {
	return &fakeDB{
		orgs: map[uuid.UUID]domain.Organization{}, users: map[uuid.UUID]domain.User{},
		numbers: map[uuid.UUID]domain.SIPNumber{}, profiles: map[uuid.UUID]domain.AgentProfile{},
		llms: map[uuid.UUID]domain.LLMConfig{}, calls: map[uuid.UUID]domain.Call{},
		turns: map[uuid.UUID][]domain.TranscriptTurn{}, contacts: map[uuid.UUID]domain.Contact{},
		campaigns: map[uuid.UUID]domain.Campaign{}, targets: map[uuid.UUID][]domain.CampaignTarget{},
		lexicon: map[uuid.UUID]domain.LexiconCorrection{},
	}
}

func ensureID(id *uuid.UUID) {
	if *id == uuid.Nil {
		*id = uuid.New()
	}
}

// --- OrgRepository + OrgCreator ---

func (f *fakeDB) EnsureDefaultOrg(context.Context) (*domain.Organization, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.orgs {
		if o.Slug == "demo" {
			return &o, nil
		}
	}
	o := domain.Organization{ID: uuid.New(), Name: "CallGo Demo", Slug: "demo", CreatedAt: time.Now()}
	f.orgs[o.ID] = o
	return &o, nil
}

func (f *fakeDB) CreateOrg(_ context.Context, o *domain.Organization) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ensureID(&o.ID)
	for _, x := range f.orgs {
		if x.Slug == o.Slug {
			return domain.ErrConflict
		}
	}
	f.orgs[o.ID] = *o
	return nil
}

func (f *fakeDB) GetOrg(_ context.Context, id uuid.UUID) (*domain.Organization, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.orgs[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &o, nil
}

func (f *fakeDB) GetOrgBySlug(_ context.Context, slug string) (*domain.Organization, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.orgs {
		if o.Slug == slug {
			return &o, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeDB) CreateUser(_ context.Context, u *domain.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ensureID(&u.ID)
	for _, x := range f.users {
		if strings.EqualFold(x.Email, u.Email) {
			return domain.ErrConflict
		}
	}
	f.users[u.ID] = *u
	return nil
}

func (f *fakeDB) GetUserByEmail(_ context.Context, email string) (*domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if strings.EqualFold(u.Email, email) {
			return &u, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeDB) GetUser(_ context.Context, id uuid.UUID) (*domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &u, nil
}

func (f *fakeDB) ListUsers(_ context.Context, orgID uuid.UUID) ([]domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.User
	for _, u := range f.users {
		if u.OrgID == orgID {
			out = append(out, u)
		}
	}
	return out, nil
}

// --- SIPNumberRepository ---

func (f *fakeDB) CreateSIPNumber(_ context.Context, n *domain.SIPNumber) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ensureID(&n.ID)
	for _, x := range f.numbers {
		if x.Number == n.Number {
			return domain.ErrConflict
		}
	}
	f.numbers[n.ID] = *n
	return nil
}

func (f *fakeDB) UpdateSIPNumber(_ context.Context, n *domain.SIPNumber) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.numbers[n.ID]; !ok {
		return domain.ErrNotFound
	}
	f.numbers[n.ID] = *n
	return nil
}

func (f *fakeDB) DeleteSIPNumber(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.numbers[id]; !ok {
		return domain.ErrNotFound
	}
	delete(f.numbers, id)
	return nil
}

func (f *fakeDB) GetSIPNumber(_ context.Context, id uuid.UUID) (*domain.SIPNumber, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.numbers[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &n, nil
}

func (f *fakeDB) GetSIPNumberByNumber(_ context.Context, number string) (*domain.SIPNumber, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, n := range f.numbers {
		if n.Number == number {
			return &n, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeDB) ListSIPNumbers(_ context.Context, orgID uuid.UUID) ([]domain.SIPNumber, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.SIPNumber
	for _, n := range f.numbers {
		if n.OrgID == orgID {
			out = append(out, n)
		}
	}
	return out, nil
}

// --- AgentProfileRepository ---

func (f *fakeDB) CreateAgentProfile(_ context.Context, p *domain.AgentProfile) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ensureID(&p.ID)
	f.profiles[p.ID] = *p
	return nil
}

func (f *fakeDB) UpdateAgentProfile(_ context.Context, p *domain.AgentProfile) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.profiles[p.ID]; !ok {
		return domain.ErrNotFound
	}
	f.profiles[p.ID] = *p
	return nil
}

func (f *fakeDB) DeleteAgentProfile(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.profiles, id)
	return nil
}

func (f *fakeDB) GetAgentProfile(_ context.Context, id uuid.UUID) (*domain.AgentProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.profiles[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &p, nil
}

func (f *fakeDB) ListAgentProfiles(_ context.Context, orgID uuid.UUID) ([]domain.AgentProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.AgentProfile
	for _, p := range f.profiles {
		if p.OrgID == orgID {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// --- LLMConfigRepository (stores the key "encrypted": list hides it) ---

func (f *fakeDB) CreateLLMConfig(_ context.Context, c *domain.LLMConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ensureID(&c.ID)
	f.llms[c.ID] = *c
	return nil
}

func (f *fakeDB) UpdateLLMConfig(_ context.Context, c *domain.LLMConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.llms[c.ID]; !ok {
		return domain.ErrNotFound
	}
	f.llms[c.ID] = *c
	return nil
}

func (f *fakeDB) DeleteLLMConfig(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.llms, id)
	return nil
}

func (f *fakeDB) GetLLMConfig(_ context.Context, id uuid.UUID) (*domain.LLMConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.llms[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &c, nil
}

func (f *fakeDB) GetDefaultLLMConfig(_ context.Context, orgID uuid.UUID) (*domain.LLMConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.llms {
		if c.OrgID == orgID && c.IsDefault {
			return &c, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeDB) ListLLMConfigs(_ context.Context, orgID uuid.UUID) ([]domain.LLMConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.LLMConfig
	for _, c := range f.llms {
		if c.OrgID == orgID {
			c.APIKey = "" // like the real repo: listing does not decrypt
			out = append(out, c)
		}
	}
	return out, nil
}

// --- CallRepository ---

func (f *fakeDB) CreateCall(_ context.Context, c *domain.Call) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ensureID(&c.ID)
	for _, x := range f.calls {
		if x.RoomName != "" && x.RoomName == c.RoomName {
			return domain.ErrConflict
		}
	}
	f.calls[c.ID] = *c
	return nil
}

func (f *fakeDB) UpdateCall(_ context.Context, c *domain.Call) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.calls[c.ID]; !ok {
		return domain.ErrNotFound
	}
	f.calls[c.ID] = *c
	return nil
}

func (f *fakeDB) GetCall(_ context.Context, id uuid.UUID) (*domain.Call, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.calls[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &c, nil
}

func (f *fakeDB) GetCallByRoom(_ context.Context, room string) (*domain.Call, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.RoomName == room {
			return &c, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeDB) ListCalls(_ context.Context, fl domain.CallFilter) ([]domain.Call, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Call
	for _, c := range f.calls {
		if c.OrgID != fl.OrgID {
			continue
		}
		if len(fl.Status) > 0 && !slices.Contains(fl.Status, c.Status) {
			continue
		}
		if fl.Direction != "" && c.Direction != fl.Direction {
			continue
		}
		if fl.CampaignID != nil && (c.CampaignID == nil || *c.CampaignID != *fl.CampaignID) {
			continue
		}
		if fl.Search != "" && !strings.Contains(c.FromNumber+" "+c.ToNumber+" "+c.Summary, fl.Search) {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	total := len(out)
	if fl.Offset > len(out) {
		fl.Offset = len(out)
	}
	out = out[fl.Offset:]
	if fl.Limit > 0 && len(out) > fl.Limit {
		out = out[:fl.Limit]
	}
	return out, total, nil
}

func (f *fakeDB) ListActiveCalls(_ context.Context, orgID uuid.UUID) ([]domain.Call, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Call
	for _, c := range f.calls {
		if c.OrgID == orgID && !c.Status.IsTerminal() {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeDB) AddTurn(_ context.Context, t *domain.TranscriptTurn) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ensureID(&t.ID)
	f.turns[t.CallID] = append(f.turns[t.CallID], *t)
	return nil
}

func (f *fakeDB) UpdateTurnText(_ context.Context, id uuid.UUID, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for cid, ts := range f.turns {
		for i := range ts {
			if ts[i].ID == id {
				f.turns[cid][i].Text = text
				return nil
			}
		}
	}
	return domain.ErrNotFound
}

func (f *fakeDB) GetTurn(_ context.Context, id uuid.UUID) (*domain.TranscriptTurn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ts := range f.turns {
		for _, t := range ts {
			if t.ID == id {
				return &t, nil
			}
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeDB) ListTurns(_ context.Context, callID uuid.UUID) ([]domain.TranscriptTurn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.turns[callID]), nil
}

func (f *fakeDB) Stats(_ context.Context, orgID uuid.UUID) (domain.CallStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var st domain.CallStats
	for _, c := range f.calls {
		if c.OrgID != orgID {
			continue
		}
		st.TotalCalls++
		if !c.Status.IsTerminal() {
			st.ActiveCalls++
		}
	}
	return st, nil
}

func (f *fakeDB) DailySeries(_ context.Context, _ uuid.UUID, days int) ([]domain.DailyCallCount, error) {
	out := make([]domain.DailyCallCount, days)
	for i := range out {
		out[i].Day = time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, i-days+1)
	}
	return out, nil
}

// --- ContactRepository ---

func (f *fakeDB) UpsertContact(_ context.Context, c *domain.Contact) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.contacts {
		if x.OrgID == c.OrgID && x.Phone == c.Phone {
			c.ID, c.CreatedAt = x.ID, x.CreatedAt
			break
		}
	}
	ensureID(&c.ID)
	f.contacts[c.ID] = *c
	return nil
}

func (f *fakeDB) GetContact(_ context.Context, id uuid.UUID) (*domain.Contact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.contacts[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &c, nil
}

func (f *fakeDB) GetContactByPhone(_ context.Context, orgID uuid.UUID, phone string) (*domain.Contact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.contacts {
		if c.OrgID == orgID && c.Phone == phone {
			return &c, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeDB) ListContacts(_ context.Context, orgID uuid.UUID, search string, limit, offset int) ([]domain.Contact, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Contact
	for _, c := range f.contacts {
		if c.OrgID == orgID && (search == "" || strings.Contains(c.Phone+c.Name, search)) {
			out = append(out, c)
		}
	}
	total := len(out)
	if offset > len(out) {
		offset = len(out)
	}
	out = out[offset:]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, total, nil
}

func (f *fakeDB) DeleteContact(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.contacts, id)
	return nil
}

// --- CampaignRepository + CampaignDeleter ---

func (f *fakeDB) CreateCampaign(_ context.Context, c *domain.Campaign, targets []domain.CampaignTarget) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ensureID(&c.ID)
	f.campaigns[c.ID] = *c
	f.targets[c.ID] = slices.Clone(targets)
	return nil
}

func (f *fakeDB) UpdateCampaign(_ context.Context, c *domain.Campaign) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.campaigns[c.ID] = *c
	return nil
}

func (f *fakeDB) GetCampaign(_ context.Context, id uuid.UUID) (*domain.Campaign, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.campaigns[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &c, nil
}

func (f *fakeDB) ListCampaigns(_ context.Context, orgID uuid.UUID) ([]domain.Campaign, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Campaign
	for _, c := range f.campaigns {
		if c.OrgID == orgID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeDB) ListRunningCampaigns(context.Context) ([]domain.Campaign, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Campaign
	for _, c := range f.campaigns {
		if c.Status == domain.CampaignRunning {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeDB) ListTargets(_ context.Context, campaignID uuid.UUID, limit, offset int) ([]domain.CampaignTarget, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	all := f.targets[campaignID]
	total := len(all)
	if offset > total {
		offset = total
	}
	out := slices.Clone(all[offset:])
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, total, nil
}

func (f *fakeDB) ClaimTargets(context.Context, uuid.UUID, int) ([]domain.CampaignTarget, error) {
	return nil, nil
}

func (f *fakeDB) UpdateTarget(_ context.Context, t *domain.CampaignTarget) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ts := f.targets[t.CampaignID]
	for i := range ts {
		if ts[i].ID == t.ID {
			ts[i] = *t
			return nil
		}
	}
	return domain.ErrNotFound
}

func (f *fakeDB) CountActiveTargets(context.Context, uuid.UUID) (int, error) { return 0, nil }

func (f *fakeDB) DeleteCampaign(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.campaigns, id)
	delete(f.targets, id)
	return nil
}

// --- LexiconRepository ---

func (f *fakeDB) AddCorrection(_ context.Context, c *domain.LexiconCorrection) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ensureID(&c.ID)
	f.lexicon[c.ID] = *c
	return nil
}

func (f *fakeDB) UpdateCorrection(_ context.Context, c *domain.LexiconCorrection) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.lexicon[c.ID]; !ok {
		return domain.ErrNotFound
	}
	f.lexicon[c.ID] = *c
	return nil
}

func (f *fakeDB) ListCorrections(_ context.Context, orgID uuid.UUID) ([]domain.LexiconCorrection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.LexiconCorrection
	for _, c := range f.lexicon {
		if c.OrgID == orgID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Wrong < out[j].Wrong })
	return out, nil
}

func (f *fakeDB) DeleteCorrection(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.lexicon, id)
	return nil
}

func (f *fakeDB) IncrementHits(_ context.Context, ids []uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range ids {
		if c, ok := f.lexicon[id]; ok {
			c.HitCount++
			f.lexicon[id] = c
		}
		f.hits = append(f.hits, id)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Collaborator fakes
// ---------------------------------------------------------------------------

type fakeTelephony struct {
	mu           sync.Mutex
	provisioned  []string
	deprovisions []string
	dials        []domain.OutboundCallRequest
	hangups      []string
	transfers    []string
	provisionErr error
	dialErr      error
}

func (t *fakeTelephony) EnsureNumberProvisioned(_ context.Context, n *domain.SIPNumber) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.provisionErr != nil {
		return t.provisionErr
	}
	t.provisioned = append(t.provisioned, n.Number)
	n.InboundTrunkID, n.OutboundTrunkID, n.DispatchRuleID = "ST_in", "ST_out", "SDR_1"
	return nil
}

func (t *fakeTelephony) DeprovisionNumber(_ context.Context, n *domain.SIPNumber) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.deprovisions = append(t.deprovisions, n.Number)
	return nil
}

func (t *fakeTelephony) Dial(_ context.Context, req domain.OutboundCallRequest) (domain.OutboundCallResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dials = append(t.dials, req)
	if t.dialErr != nil {
		return domain.OutboundCallResult{}, t.dialErr
	}
	return domain.OutboundCallResult{ParticipantID: "sip_" + req.ToNumber, SIPCallID: "SCL_1"}, nil
}

func (t *fakeTelephony) Hangup(_ context.Context, room string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.hangups = append(t.hangups, room)
	return nil
}

func (t *fakeTelephony) TransferCall(_ context.Context, room, participant, to string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.transfers = append(t.transfers, room+"|"+participant+"|"+to)
	return nil
}

func (t *fakeTelephony) ListActiveRooms(context.Context) ([]string, error) { return nil, nil }

type fakeBus struct {
	mu     sync.Mutex
	events []domain.Event
}

func (b *fakeBus) Publish(_ context.Context, ev domain.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, ev)
}

func (b *fakeBus) ofType(t domain.EventType) []domain.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []domain.Event
	for _, e := range b.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

func (b *fakeBus) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = nil
}

type fakeHub struct {
	mu      sync.Mutex
	orgID   uuid.UUID
	initial []domain.Call
}

func (h *fakeHub) ServeWS(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, initial func(context.Context) []domain.Call) {
	h.mu.Lock()
	h.orgID = orgID
	h.initial = initial(r.Context())
	h.mu.Unlock()
	w.WriteHeader(http.StatusSwitchingProtocols)
}

type fakeCampaigns struct {
	db    *fakeDB
	mu    sync.Mutex
	ended []uuid.UUID
}

func (c *fakeCampaigns) setStatus(ctx context.Context, id uuid.UUID, st domain.CampaignStatus) error {
	camp, err := c.db.GetCampaign(ctx, id)
	if err != nil {
		return err
	}
	camp.Status = st
	return c.db.UpdateCampaign(ctx, camp)
}

func (c *fakeCampaigns) Start(ctx context.Context, id uuid.UUID) error {
	return c.setStatus(ctx, id, domain.CampaignRunning)
}

func (c *fakeCampaigns) Pause(ctx context.Context, id uuid.UUID) error {
	return c.setStatus(ctx, id, domain.CampaignPaused)
}

func (c *fakeCampaigns) OnCallEnded(_ context.Context, call *domain.Call) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ended = append(c.ended, call.ID)
}

// csvParser implements TargetParser and ContactParser with a minimal CSV reader.
type csvParser struct{}

func readCSV(r io.Reader) ([]map[string]string, []RowError, error) {
	recs, err := csv.NewReader(r).ReadAll()
	if err != nil {
		return nil, nil, err
	}
	if len(recs) == 0 {
		return nil, nil, errors.New("empty CSV")
	}
	header := recs[0]
	var rows []map[string]string
	var rowErrs []RowError
	for i, rec := range recs[1:] {
		m := map[string]string{}
		for j, h := range header {
			if j < len(rec) {
				m[strings.TrimSpace(strings.ToLower(h))] = strings.TrimSpace(rec[j])
			}
		}
		if m["phone"] == "" {
			rowErrs = append(rowErrs, RowError{Row: i + 2, Message: "phone is required"})
			continue
		}
		rows = append(rows, m)
	}
	return rows, rowErrs, nil
}

func (csvParser) ParseTargets(r io.Reader) (ParsedTargets, error) {
	rows, rowErrs, err := readCSV(r)
	if err != nil {
		return ParsedTargets{}, err
	}
	out := ParsedTargets{Skipped: len(rowErrs), Errors: rowErrs}
	for _, m := range rows {
		t := domain.CampaignTarget{Phone: m["phone"], Name: m["name"], Vars: map[string]string{}}
		for k, v := range m {
			if k != "phone" && k != "name" {
				t.Vars[k] = v
			}
		}
		out.Targets = append(out.Targets, t)
	}
	return out, nil
}

func (csvParser) ParseContacts(r io.Reader) (ParsedContacts, error) {
	rows, rowErrs, err := readCSV(r)
	if err != nil {
		return ParsedContacts{}, err
	}
	out := ParsedContacts{Skipped: len(rowErrs), Errors: rowErrs}
	for _, m := range rows {
		out.Contacts = append(out.Contacts, domain.Contact{Phone: m["phone"], Name: m["name"]})
	}
	return out, nil
}

type fakeLexiconEngine struct {
	mu          sync.Mutex
	invalidated []uuid.UUID
}

func (l *fakeLexiconEngine) Apply(_ context.Context, _ uuid.UUID, text string) (string, []LexiconHit, error) {
	return strings.ToUpper(text), []LexiconHit{{ID: uuid.Nil, Wrong: "x", Correct: "y"}}, nil
}

func (l *fakeLexiconEngine) Invalidate(orgID uuid.UUID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.invalidated = append(l.invalidated, orgID)
}

type fakeTester struct {
	mu   sync.Mutex
	last domain.LLMConfig
	err  error
}

func (f *fakeTester) Test(_ context.Context, cfg domain.LLMConfig, prompt string) (string, time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = cfg
	if f.err != nil {
		return "", time.Millisecond, f.err
	}
	return "pong: " + prompt, 12 * time.Millisecond, nil
}
