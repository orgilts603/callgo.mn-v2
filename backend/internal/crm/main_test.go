package crm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

const defaultTestDSN = "postgres://callgo:callgo@localhost:5432/callgo_test?sslmode=disable"

var (
	testDSN   string
	testPool  *pgxpool.Pool
	testStore *Store
	testKey   = []byte("0123456789abcdef0123456789abcdef") // 32 bytes
)

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	testDSN = os.Getenv("CALLGO_TEST_DATABASE_URL")
	explicit := testDSN != ""
	if !explicit {
		testDSN = defaultTestDSN
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	pool, err := Open(ctx, testDSN)
	if err != nil {
		if explicit {
			fmt.Fprintf(os.Stderr, "crm tests: cannot connect to CALLGO_TEST_DATABASE_URL: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "crm tests: SKIP, no database at %s: %v\n", defaultTestDSN, err)
		return 0
	}
	defer pool.Close()

	// Start from an empty schema so the full migration history is exercised.
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;`); err != nil {
		fmt.Fprintf(os.Stderr, "crm tests: reset schema: %v\n", err)
		return 1
	}
	if err := Migrate(ctx, testDSN); err != nil {
		fmt.Fprintf(os.Stderr, "crm tests: migrate: %v\n", err)
		return 1
	}
	pool.Reset()

	testPool = pool
	testStore = New(pool, testKey)
	return m.Run()
}

// setup truncates every table and returns a fresh context and store.
func setup(t *testing.T) (context.Context, *Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	_, err := testPool.Exec(ctx, `TRUNCATE organizations, users, llm_configs, agent_profiles, sip_numbers,
		contacts, calls, call_transcripts, campaigns, campaign_targets, lexicon_corrections, do_not_call CASCADE`)
	require.NoError(t, err)
	return ctx, testStore
}

func newOrg(t *testing.T, ctx context.Context, s *Store, slug string) *domain.Organization {
	t.Helper()
	o := &domain.Organization{Name: "Org " + slug, Slug: slug}
	require.NoError(t, s.CreateOrg(ctx, o))
	return o
}

func newCall(t *testing.T, ctx context.Context, s *Store, c domain.Call) *domain.Call {
	t.Helper()
	if c.Direction == "" {
		c.Direction = domain.DirectionInbound
	}
	if c.Status == "" {
		c.Status = domain.StatusRinging
	}
	if c.RoomName == "" {
		c.RoomName = "call-" + uuid.NewString()
	}
	require.NoError(t, s.CreateCall(ctx, &c))
	return &c
}

func requireErrIs(t *testing.T, err, target error) {
	t.Helper()
	require.Error(t, err)
	require.Truef(t, errors.Is(err, target), "want %v, got %v", target, err)
}

func ptr[T any](v T) *T { return &v }
