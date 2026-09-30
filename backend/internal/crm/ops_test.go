package crm

import (
	"context"
	"io/fs"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// opsTables are created by migration 000006.
var opsTables = []string{"webhooks", "webhook_deliveries", "sms_messages", "callback_requests"}

// opsColumns are added to existing tables by migration 000006.
var opsColumns = map[string][]string{
	"sip_numbers":    {"routing"},
	"agent_profiles": {"post_call_actions"},
	"calls":          {"recording", "handoff", "operator_id", "usage"},
}

func opsTableExists(t *testing.T, ctx context.Context, table string) bool {
	t.Helper()
	var ok bool
	require.NoError(t, testPool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+table).Scan(&ok))
	return ok
}

// versionBeforeOps is the migration version just below 000006 (000005 when
// the identity/billing migration is present, otherwise 000004).
func versionBeforeOps(t *testing.T) uint {
	t.Helper()
	m, err := fs.Glob(migrationsFS, "migrations/000005_*.up.sql")
	require.NoError(t, err)
	if len(m) > 0 {
		return 5
	}
	return 4
}

func TestMigrationSaaSOpsDownUp(t *testing.T) {
	ctx, s := setup(t)
	t.Cleanup(func() {
		require.NoError(t, Migrate(context.Background(), testDSN))
		testPool.Reset()
	})

	for _, tbl := range opsTables {
		require.Truef(t, opsTableExists(t, ctx, tbl), "%s after up", tbl)
	}
	for tbl, cols := range opsColumns {
		for _, col := range cols {
			require.Truef(t, columnExists(t, ctx, tbl, col), "%s.%s after up", tbl, col)
		}
	}
	org := newOrg(t, ctx, s, "opsmig")
	n := &domain.SIPNumber{OrgID: org.ID, Number: "+97677000001", Routing: domain.RoutingConfig{MenuPrompt: "x"}}
	require.NoError(t, s.CreateSIPNumber(ctx, n))

	require.NoError(t, runMigrations(ctx, testDSN, func(m *migrate.Migrate) error {
		return m.Migrate(versionBeforeOps(t))
	}))
	testPool.Reset()
	for _, tbl := range opsTables {
		require.Falsef(t, opsTableExists(t, ctx, tbl), "%s after down", tbl)
	}
	for tbl, cols := range opsColumns {
		for _, col := range cols {
			require.Falsef(t, columnExists(t, ctx, tbl, col), "%s.%s after down", tbl, col)
		}
	}

	require.NoError(t, Migrate(ctx, testDSN))
	testPool.Reset()
	var version int
	var dirty bool
	require.NoError(t, testPool.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty))
	require.GreaterOrEqual(t, version, 6)
	require.False(t, dirty)
	for _, tbl := range opsTables {
		require.Truef(t, opsTableExists(t, ctx, tbl), "%s after re-up", tbl)
	}
	got, err := s.GetSIPNumber(ctx, n.ID)
	require.NoError(t, err)
	require.True(t, got.Routing.IsZero(), "column default after re-up")
	require.Equal(t, []domain.MenuOption{}, got.Routing.Menu)
}

func TestSIPNumberRoutingRoundTrip(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "routing")
	p1 := &domain.AgentProfile{OrgID: org.ID, Name: "Sales"}
	p2 := &domain.AgentProfile{OrgID: org.ID, Name: "Night"}
	require.NoError(t, s.CreateAgentProfile(ctx, p1))
	require.NoError(t, s.CreateAgentProfile(ctx, p2))

	n := &domain.SIPNumber{OrgID: org.ID, Number: "+97677000002", AgentProfileID: &p1.ID, Active: true}
	require.NoError(t, s.CreateSIPNumber(ctx, n))
	var raw string
	require.NoError(t, testPool.QueryRow(ctx, `SELECT routing::text FROM sip_numbers WHERE id = $1`, n.ID).Scan(&raw))
	require.Equal(t, "{}", raw, "zero routing stored as {}")
	got, err := s.GetSIPNumber(ctx, n.ID)
	require.NoError(t, err)
	require.True(t, got.Routing.IsZero())
	require.Equal(t, []domain.MenuOption{}, got.Routing.Menu)

	n.Routing = domain.RoutingConfig{
		BusinessHours: domain.CampaignSchedule{
			Timezone: "Asia/Ulaanbaatar", Weekdays: []time.Weekday{time.Monday, time.Friday},
			StartTime: "09:00", EndTime: "18:00",
		},
		AfterHoursProfile: &p2.ID,
		AfterHoursMessage: "Бид ажлын цагаар хариулна.",
		MenuPrompt:        "Борлуулалт 1, бусад 2",
		Menu: []domain.MenuOption{
			{Key: "1", Label: "Борлуулалт", AgentProfileID: p1.ID},
			{Key: "2", Label: "Бусад", AgentProfileID: p2.ID},
		},
		MenuTimeoutSec: 6,
		MenuRepeat:     2,
	}
	require.NoError(t, s.UpdateSIPNumber(ctx, n))
	got, err = s.GetSIPNumber(ctx, n.ID)
	require.NoError(t, err)
	require.Equal(t, n.Routing, got.Routing)

	byNum, err := s.GetSIPNumberByNumber(ctx, n.Number)
	require.NoError(t, err)
	require.Equal(t, n.Routing, byNum.Routing)
	list, err := s.ListSIPNumbers(ctx, org.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, n.Routing, list[0].Routing)

	// Created with routing directly.
	n2 := &domain.SIPNumber{OrgID: org.ID, Number: "+97677000003", Routing: domain.RoutingConfig{MenuTimeoutSec: 4}}
	require.NoError(t, s.CreateSIPNumber(ctx, n2))
	got, err = s.GetSIPNumber(ctx, n2.ID)
	require.NoError(t, err)
	require.Equal(t, 4, got.Routing.MenuTimeoutSec)

	// Clearing routing goes back to {}.
	n.Routing = domain.RoutingConfig{}
	require.NoError(t, s.UpdateSIPNumber(ctx, n))
	require.NoError(t, testPool.QueryRow(ctx, `SELECT routing::text FROM sip_numbers WHERE id = $1`, n.ID).Scan(&raw))
	require.Equal(t, "{}", raw)
}

func TestAgentProfilePostCallActionsRoundTrip(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "pca")
	p := &domain.AgentProfile{OrgID: org.ID, Name: "Support"}
	require.NoError(t, s.CreateAgentProfile(ctx, p))
	var raw string
	require.NoError(t, testPool.QueryRow(ctx,
		`SELECT post_call_actions::text FROM agent_profiles WHERE id = $1`, p.ID).Scan(&raw))
	require.Equal(t, "[]", raw)
	got, err := s.GetAgentProfile(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, []domain.PostCallAction{}, got.PostCallActions)

	whID := uuid.New()
	p.PostCallActions = []domain.PostCallAction{
		{Type: domain.ActionSMS, Outcomes: []string{"agreed"}, Template: "Сайн байна уу {{name}}"},
		{Type: domain.ActionWebhook, Outcomes: []string{}, WebhookID: &whID},
		{Type: domain.ActionCallback, Outcomes: []string{"callback"}, DelayMin: 30},
	}
	require.NoError(t, s.UpdateAgentProfile(ctx, p))
	got, err = s.GetAgentProfile(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, p.PostCallActions, got.PostCallActions)

	list, err := s.ListAgentProfiles(ctx, org.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, p.PostCallActions, list[0].PostCallActions)

	p2 := &domain.AgentProfile{OrgID: org.ID, Name: "Created with actions",
		PostCallActions: []domain.PostCallAction{{Type: domain.ActionSMS, Outcomes: []string{}, Template: "hi"}}}
	require.NoError(t, s.CreateAgentProfile(ctx, p2))
	got, err = s.GetAgentProfile(ctx, p2.ID)
	require.NoError(t, err)
	require.Equal(t, p2.PostCallActions, got.PostCallActions)
}

func TestCallRecordingHandoffUsageRoundTrip(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "callops")
	op := &domain.User{OrgID: org.ID, Email: "op@example.mn", Name: "Operator"}
	require.NoError(t, s.CreateUser(ctx, op))

	// Defaults: nothing set.
	plain := newCall(t, ctx, s, domain.Call{OrgID: org.ID})
	got, err := s.GetCall(ctx, plain.ID)
	require.NoError(t, err)
	require.Nil(t, got.Recording)
	require.Nil(t, got.Usage)
	require.Nil(t, got.OperatorID)
	require.Equal(t, domain.HandoffNone, got.Handoff)
	var recNull, usageNull bool
	require.NoError(t, testPool.QueryRow(ctx,
		`SELECT recording IS NULL, usage IS NULL FROM calls WHERE id = $1`, plain.ID).Scan(&recNull, &usageNull))
	require.True(t, recNull)
	require.True(t, usageNull)

	start := time.Now().UTC().Truncate(time.Second)
	end := start.Add(90 * time.Second)
	rec := &domain.RecordingInfo{EgressID: "EG_1", ObjectKey: "rec/a.ogg", SizeBytes: 12345, DurationSec: 90,
		Status: "ready", StartedAt: &start, EndedAt: &end}
	usage := &domain.CallUsage{LLMTokensIn: 1200, LLMTokensOut: 340, STTSeconds: 88.5, TTSChars: 900,
		LLMModel: "gpt-4o-mini", CostMNT: 420}
	c := newCall(t, ctx, s, domain.Call{OrgID: org.ID, Recording: rec, Usage: usage,
		Handoff: domain.HandoffActive, OperatorID: &op.ID})
	got, err = s.GetCall(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, rec, got.Recording)
	require.Equal(t, usage, got.Usage)
	require.Equal(t, domain.HandoffActive, got.Handoff)
	require.Equal(t, &op.ID, got.OperatorID)

	// UpdateCall overwrites (and clears) the new fields.
	got.Recording = nil
	got.Usage = &domain.CallUsage{CostMNT: 1}
	got.Handoff = domain.HandoffEnded
	got.OperatorID = nil
	require.NoError(t, s.UpdateCall(ctx, got))
	again, err := s.GetCall(ctx, c.ID)
	require.NoError(t, err)
	require.Nil(t, again.Recording)
	require.Equal(t, &domain.CallUsage{CostMNT: 1}, again.Usage)
	require.Equal(t, domain.HandoffEnded, again.Handoff)
	require.Nil(t, again.OperatorID)

	// Listing paths scan the new columns too.
	byRoom, err := s.GetCallByRoom(ctx, c.RoomName)
	require.NoError(t, err)
	require.Equal(t, again.Usage, byRoom.Usage)
	list, _, err := s.ListCalls(ctx, domain.CallFilter{OrgID: org.ID})
	require.NoError(t, err)
	require.Len(t, list, 2)

	// Invalid handoff state is rejected by the CHECK constraint.
	bad := domain.Call{OrgID: org.ID, Handoff: "bogus", RoomName: "bad-room"}
	bad.Direction, bad.Status = domain.DirectionInbound, domain.StatusRinging
	requireErrIs(t, s.CreateCall(ctx, &bad), domain.ErrInvalid)

	// Deleting the operator nulls the reference.
	require.NoError(t, s.SetCallHandoff(ctx, c.ID, domain.HandoffActive, &op.ID))
	_, err = testPool.Exec(ctx, `DELETE FROM users WHERE id = $1`, op.ID)
	require.NoError(t, err)
	again, err = s.GetCall(ctx, c.ID)
	require.NoError(t, err)
	require.Nil(t, again.OperatorID)
}

func TestSetCallRecordingAndHandoff(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "setrec")
	op := &domain.User{OrgID: org.ID, Email: "op2@example.mn"}
	require.NoError(t, s.CreateUser(ctx, op))
	c := newCall(t, ctx, s, domain.Call{OrgID: org.ID})

	rec := &domain.RecordingInfo{EgressID: "EG_2", Status: "recording"}
	require.NoError(t, s.SetCallRecording(ctx, c.ID, rec))
	got, err := s.GetCall(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, rec, got.Recording)
	require.True(t, got.UpdatedAt.After(c.UpdatedAt) || got.UpdatedAt.Equal(c.UpdatedAt))

	require.NoError(t, s.SetCallRecording(ctx, c.ID, nil))
	got, err = s.GetCall(ctx, c.ID)
	require.NoError(t, err)
	require.Nil(t, got.Recording)
	requireErrIs(t, s.SetCallRecording(ctx, uuid.New(), rec), domain.ErrNotFound)

	require.NoError(t, s.SetCallHandoff(ctx, c.ID, domain.HandoffRequested, nil))
	got, err = s.GetCall(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, domain.HandoffRequested, got.Handoff)
	require.Nil(t, got.OperatorID)

	require.NoError(t, s.SetCallHandoff(ctx, c.ID, domain.HandoffActive, &op.ID))
	require.NoError(t, s.SetCallHandoff(ctx, c.ID, domain.HandoffEnded, nil))
	got, err = s.GetCall(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, domain.HandoffEnded, got.Handoff)
	require.Equal(t, &op.ID, got.OperatorID, "nil operator keeps the one who handled the call")

	require.NoError(t, s.SetCallHandoff(ctx, c.ID, domain.HandoffNone, nil))
	got, err = s.GetCall(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, domain.HandoffNone, got.Handoff)
	require.Nil(t, got.OperatorID, "HandoffNone clears the operator")

	requireErrIs(t, s.SetCallHandoff(ctx, c.ID, "bogus", nil), domain.ErrInvalid)
	requireErrIs(t, s.SetCallHandoff(ctx, uuid.New(), domain.HandoffActive, nil), domain.ErrNotFound)
	requireErrIs(t, s.SetCallHandoff(ctx, c.ID, domain.HandoffActive, ptr(uuid.New())), domain.ErrInvalid)
}

func TestListCallsForRetention(t *testing.T) {
	ctx, s := setup(t)
	org1 := newOrg(t, ctx, s, "ret1")
	org2 := newOrg(t, ctx, s, "ret2")
	now := time.Now().UTC()
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	ready := func() *domain.RecordingInfo { return &domain.RecordingInfo{Status: "ready", ObjectKey: "k"} }

	old1 := newCall(t, ctx, s, domain.Call{OrgID: org1.ID, EndedAt: at(-40 * 24 * time.Hour), Recording: ready()})
	old2 := newCall(t, ctx, s, domain.Call{OrgID: org2.ID, EndedAt: at(-35 * 24 * time.Hour), Recording: ready()})
	old3 := newCall(t, ctx, s, domain.Call{OrgID: org1.ID, EndedAt: at(-31 * 24 * time.Hour), Recording: ready()})
	// Not eligible: recent, deleted, failed, no recording, not ended.
	newCall(t, ctx, s, domain.Call{OrgID: org1.ID, EndedAt: at(-time.Hour), Recording: ready()})
	newCall(t, ctx, s, domain.Call{OrgID: org1.ID, EndedAt: at(-40 * 24 * time.Hour),
		Recording: &domain.RecordingInfo{Status: "deleted"}})
	newCall(t, ctx, s, domain.Call{OrgID: org1.ID, EndedAt: at(-40 * 24 * time.Hour),
		Recording: &domain.RecordingInfo{Status: "failed"}})
	newCall(t, ctx, s, domain.Call{OrgID: org1.ID, EndedAt: at(-40 * 24 * time.Hour)})
	newCall(t, ctx, s, domain.Call{OrgID: org1.ID, Recording: ready()})

	before := now.Add(-30 * 24 * time.Hour)
	list, err := s.ListCallsForRetention(ctx, before, 10)
	require.NoError(t, err)
	ids := make([]uuid.UUID, len(list))
	for i, c := range list {
		ids[i] = c.ID
		require.Equal(t, "ready", c.Recording.Status)
	}
	require.Equal(t, []uuid.UUID{old1.ID, old2.ID, old3.ID}, ids, "oldest first, all orgs")

	list, err = s.ListCallsForRetention(ctx, before, 2)
	require.NoError(t, err)
	require.Len(t, list, 2)

	// Marking a recording deleted removes it from the sweep.
	require.NoError(t, s.SetCallRecording(ctx, old1.ID, &domain.RecordingInfo{Status: "deleted"}))
	list, err = s.ListCallsForRetention(ctx, before, 10)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, old2.ID, list[0].ID)
}
