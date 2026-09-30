package crm

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var allTables = []string{
	"organizations", "users", "llm_configs", "agent_profiles", "sip_numbers", "contacts",
	"calls", "call_transcripts", "campaigns", "campaign_targets", "lexicon_corrections",
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
	require.Equal(t, 1, version)
	require.False(t, dirty)
}

func TestMigrateBadDSN(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.Error(t, Migrate(ctx, "postgres://nobody:wrong@127.0.0.1:1/none?sslmode=disable&connect_timeout=1"))
	_, err := Open(ctx, "not a dsn ::::")
	require.Error(t, err)
}
