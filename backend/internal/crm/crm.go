// Package crm is the PostgreSQL persistence adapter of CallGo.mn. It owns the
// schema (embedded golang-migrate migrations) and implements every repository
// port declared in internal/domain on a single *Store.
package crm

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Compile-time proof that *Store satisfies every repository port.
var (
	_ domain.OrgRepository          = (*Store)(nil)
	_ domain.SIPNumberRepository    = (*Store)(nil)
	_ domain.AgentProfileRepository = (*Store)(nil)
	_ domain.LLMConfigRepository    = (*Store)(nil)
	_ domain.CallRepository         = (*Store)(nil)
	_ domain.ContactRepository      = (*Store)(nil)
	_ domain.CampaignRepository     = (*Store)(nil)
	_ domain.DoNotCallRepository    = (*Store)(nil)
	_ domain.LexiconRepository      = (*Store)(nil)
)

// Pool defaults applied by Open unless the DSN sets them explicitly
// (pool_max_conns, pool_min_conns, ...).
const (
	defaultMaxConns          = 20
	defaultMinConns          = 2
	defaultMaxConnLifetime   = time.Hour
	defaultMaxConnIdleTime   = 15 * time.Minute
	defaultHealthCheckPeriod = time.Minute
	connectTimeout           = 10 * time.Second
)

// Open creates a pgx connection pool for dsn (URL or keyword/value form) and
// verifies connectivity with a ping.
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("crm: parse dsn: %w", err)
	}
	if !strings.Contains(dsn, "pool_max_conns") {
		cfg.MaxConns = defaultMaxConns
	}
	if !strings.Contains(dsn, "pool_min_conns") {
		cfg.MinConns = defaultMinConns
	}
	if !strings.Contains(dsn, "pool_max_conn_lifetime") {
		cfg.MaxConnLifetime = defaultMaxConnLifetime
	}
	if !strings.Contains(dsn, "pool_max_conn_idle_time") {
		cfg.MaxConnIdleTime = defaultMaxConnIdleTime
	}
	if !strings.Contains(dsn, "pool_health_check_period") {
		cfg.HealthCheckPeriod = defaultHealthCheckPeriod
	}
	if cfg.ConnConfig.ConnectTimeout == 0 {
		cfg.ConnConfig.ConnectTimeout = connectTimeout
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	if _, ok := cfg.ConnConfig.RuntimeParams["application_name"]; !ok {
		cfg.ConnConfig.RuntimeParams["application_name"] = "callgo-backend"
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("crm: create pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("crm: ping database: %w", err)
	}
	return pool, nil
}

// Migrate applies all pending up migrations. It is safe to call on every
// start (no-op when the schema is current) and from several processes at
// once (golang-migrate takes a Postgres advisory lock).
func Migrate(ctx context.Context, dsn string) error {
	return runMigrations(ctx, dsn, func(m *migrate.Migrate) error { return m.Up() })
}

// MigrateDown rolls back every migration (drops all CallGo tables). Intended
// for tests and local resets.
func MigrateDown(ctx context.Context, dsn string) error {
	return runMigrations(ctx, dsn, func(m *migrate.Migrate) error { return m.Down() })
}

func runMigrations(ctx context.Context, dsn string, run func(*migrate.Migrate) error) (err error) {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("crm: open embedded migrations: %w", err)
	}
	connCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("crm: parse dsn: %w", err)
	}
	db := stdlib.OpenDB(*connCfg)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return fmt.Errorf("crm: migrate: ping database: %w", err)
	}
	drv, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		_ = db.Close()
		return fmt.Errorf("crm: migrate: init postgres driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "postgres", drv)
	if err != nil {
		_ = drv.Close()
		return fmt.Errorf("crm: migrate: init: %w", err)
	}
	defer func() {
		srcErr, dbErr := m.Close()
		if err == nil {
			err = errors.Join(srcErr, dbErr)
		}
	}()

	// Translate context cancellation into golang-migrate's graceful stop.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			select {
			case m.GracefulStop <- true:
			default:
			}
		case <-done:
		}
	}()

	if err := run(m); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("crm: migrate: %w", err)
	}
	return ctx.Err()
}

// querier is the subset of pgx shared by *pgxpool.Pool and pgx.Tx, so the same
// repository code runs inside or outside a transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
	CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Store implements all domain repository ports on PostgreSQL.
type Store struct {
	db     querier
	cipher *keyCipher
}

// New wraps a pool. encryptionKey protects LLM API keys at rest with
// AES-256-GCM: a 32-byte key is used as-is, any other length is stretched
// with SHA-256.
func New(pool *pgxpool.Pool, encryptionKey []byte) *Store {
	return &Store{db: pool, cipher: newKeyCipher(encryptionKey)}
}

// inTx runs fn with a Store bound to a transaction (a savepoint when already
// inside one), committing on success.
func (s *Store) inTx(ctx context.Context, fn func(tx *Store) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("crm: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(&Store{db: tx, cipher: s.cipher}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("crm: commit: %w", mapErr(err))
	}
	return nil
}

// dbErr wraps err with the operation name, translating well-known Postgres
// errors into domain sentinels.
func dbErr(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("crm: %s: %w", op, mapErr(err))
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return fmt.Errorf("%w: %s", domain.ErrConflict, pgErr.ConstraintName)
		case "23503", "23514", "23502", "22P02", "22001": // fk, check, not null, bad text repr, too long
			return fmt.Errorf("%w: %s", domain.ErrInvalid, pgErr.Message)
		}
	}
	return err
}

// affected returns ErrNotFound when a write matched no row.
func affected(op string, tag pgconn.CommandTag, err error) error {
	if err != nil {
		return dbErr(op, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("crm: %s: %w", op, domain.ErrNotFound)
	}
	return nil
}

// nilIfZero lets the column DEFAULT (gen_random_uuid()) apply for zero IDs via
// COALESCE($n, gen_random_uuid()).
func nilIfZero(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// nilIfZeroTime maps a zero time to NULL so COALESCE($n, now()) applies.
func nilIfZeroTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func clampPage(limit, offset, def, max int) (int, int) {
	if limit <= 0 {
		limit = def
	}
	if limit > max {
		limit = max
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// likePattern escapes LIKE metacharacters and wraps q in %…%.
func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}
