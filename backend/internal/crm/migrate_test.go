package crm

import (
	"context"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

var allTables = []string{
	"organizations", "users", "llm_configs", "agent_profiles", "sip_numbers", "contacts",
	"calls", "call_transcripts", "campaigns", "campaign_targets", "lexicon_corrections", "do_not_call",
	"knowledge_bases", "knowledge_documents", "knowledge_chunks",
}

// latestVersion is the number of the newest embedded migration.
const latestVersion = 4

// columnExists reports whether table.column exists in the public schema.
func columnExists(t *testing.T, ctx context.Context, table, column string) bool {
	t.Helper()
	var ok bool
	require.NoError(t, testPool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns
		 WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2)`, table, column).Scan(&ok))
	return ok
}

func tablesPresent(t *testing.T, ctx context.Context) int {
	t.Helper()
	n := 0
	for _, tbl := range allTables {
		var exists bool
		require.NoError(t, testPool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+tbl).Scan(&exists))
		if exists {
			n++
		}
	}
	return n
}

func TestMigrationsUpDownIdempotent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	t.Cleanup(func() {
		// Leave the schema migrated and the pool free of stale statement caches.
		require.NoError(t, Migrate(context.Background(), testDSN))
		testPool.Reset()
	})

	require.NoError(t, Migrate(ctx, testDSN), "up on a current schema is a no-op")
	require.Equal(t, len(allTables), tablesPresent(t, ctx))

	require.NoError(t, MigrateDown(ctx, testDSN))
	testPool.Reset()
	require.Equal(t, 0, tablesPresent(t, ctx))
	require.NoError(t, MigrateDown(ctx, testDSN), "down on an empty schema is a no-op")

	require.NoError(t, Migrate(ctx, testDSN))
	require.NoError(t, Migrate(ctx, testDSN))
	testPool.Reset()
	require.Equal(t, len(allTables), tablesPresent(t, ctx))

	var version int
	var dirty bool
	require.NoError(t, testPool.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty))
	require.Equal(t, latestVersion, version)
	require.False(t, dirty)
}

// TestMigrationCampaignV2DownUp migrates down to 000001 and up again on a schema
// holding v2 data (a skipped target) and checks the v2 columns come and go.
func TestMigrationCampaignV2DownUp(t *testing.T) {
	ctx, s := setup(t)
	t.Cleanup(func() {
		require.NoError(t, Migrate(context.Background(), testDSN))
		testPool.Reset()
	})
	org := newOrg(t, ctx, s, "mig")
	c := &domain.Campaign{OrgID: org.ID, Name: "v2"}
	require.NoError(t, s.CreateCampaign(ctx, c, []domain.CampaignTarget{
		{Phone: "+97699000001"},
		{Phone: "+97699000002", Status: domain.TargetSkipped, LastError: "do_not_call"},
	}))

	v2Cols := map[string][]string{
		"campaigns":        {"schedule", "outcomes", "dry_run_limit", "dry_run_dialed", "skipped"},
		"campaign_targets": {"outcome", "outcome_note"},
		"calls":            {"outcome", "outcome_note"},
	}
	for tbl, cols := range v2Cols {
		for _, col := range cols {
			require.Truef(t, columnExists(t, ctx, tbl, col), "%s.%s after up", tbl, col)
		}
	}

	require.NoError(t, runMigrations(ctx, testDSN, func(m *migrate.Migrate) error { return m.Migrate(1) }))
	testPool.Reset()
	for tbl, cols := range v2Cols {
		for _, col := range cols {
			require.Falsef(t, columnExists(t, ctx, tbl, col), "%s.%s after down", tbl, col)
		}
	}
	var exists bool
	require.NoError(t, testPool.QueryRow(ctx, `SELECT to_regclass('public.do_not_call') IS NOT NULL`).Scan(&exists))
	require.False(t, exists)
	var status string
	require.NoError(t, testPool.QueryRow(ctx,
		`SELECT status FROM campaign_targets WHERE phone = '+97699000002'`).Scan(&status))
	require.Equal(t, "failed", status, "skipped targets survive the down migration")
	_, err := testPool.Exec(ctx, `UPDATE campaign_targets SET status = 'skipped'`)
	require.Error(t, err, "v1 status constraint restored")

	require.NoError(t, Migrate(ctx, testDSN))
	testPool.Reset()
	for tbl, cols := range v2Cols {
		for _, col := range cols {
			require.Truef(t, columnExists(t, ctx, tbl, col), "%s.%s after re-up", tbl, col)
		}
	}
	got, err := s.GetCampaign(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, domain.CampaignSchedule{}, got.Schedule)
	require.Equal(t, []domain.CampaignOutcome{}, got.Outcomes)
	require.Zero(t, got.Skipped, "column default after re-up")
}

func TestMigrateBadDSN(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.Error(t, Migrate(ctx, "postgres://nobody:wrong@127.0.0.1:1/none?sslmode=disable&connect_timeout=1"))
	_, err := Open(ctx, "not a dsn ::::")
	require.Error(t, err)
}
