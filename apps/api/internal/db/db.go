package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"wise/apps/api/internal/db/dbgen"
	"wise/migrations"
)

// DB bundles the connection pool with generated queries.
type DB struct {
	Pool *pgxpool.Pool
	*dbgen.Queries
}

// Open connects to PostgreSQL and verifies connectivity.
func Open(ctx context.Context, url string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &DB{Pool: pool, Queries: dbgen.New(pool)}, nil
}

func (d *DB) Close() { d.Pool.Close() }

// InTx runs fn inside a transaction, committing on success and rolling back on error.
func (d *DB) InTx(ctx context.Context, fn func(q *dbgen.Queries) error) error {
	return pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
		return fn(d.Queries.WithTx(tx))
	})
}

func newProvider(d *DB) (*goose.Provider, *sql.DB, error) {
	sqlDB := stdlib.OpenDBFromPool(d.Pool)
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		sqlDB.Close()
		return nil, nil, fmt.Errorf("create migration provider: %w", err)
	}
	return p, sqlDB, nil
}

// MigrateUp applies all pending migrations.
func (d *DB) MigrateUp(ctx context.Context, log *slog.Logger) error {
	p, sqlDB, err := newProvider(d)
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	results, err := p.Up(ctx)
	for _, r := range results {
		log.Info("migration applied", "version", r.Source.Version, "path", r.Source.Path, "duration", r.Duration)
	}
	if err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// MigrateDown rolls back the most recent migration.
func (d *DB) MigrateDown(ctx context.Context) error {
	p, sqlDB, err := newProvider(d)
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	_, err = p.Down(ctx)
	return err
}

// MigrateReset rolls back all migrations.
func (d *DB) MigrateReset(ctx context.Context) error {
	p, sqlDB, err := newProvider(d)
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	_, err = p.DownTo(ctx, 0)
	return err
}

// MigrationStatus returns "version: state" lines.
func (d *DB) MigrationStatus(ctx context.Context) ([]string, error) {
	p, sqlDB, err := newProvider(d)
	if err != nil {
		return nil, err
	}
	defer sqlDB.Close()
	statuses, err := p.Status(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(statuses))
	for _, s := range statuses {
		out = append(out, fmt.Sprintf("%05d %-8s %s", s.Source.Version, s.State, s.Source.Path))
	}
	return out, nil
}

// ErrNoRows is returned by single-row queries that match nothing.
var ErrNoRows = pgx.ErrNoRows

// IsNotFound reports whether err means "no rows".
func IsNotFound(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// IsUniqueViolation reports whether err is a unique constraint violation.
func IsUniqueViolation(err error) bool { return pgCode(err) == "23505" }

// IsForeignKeyViolation reports whether err is a foreign key violation.
func IsForeignKeyViolation(err error) bool { return pgCode(err) == "23503" }

// IsCheckViolation reports whether err is a check constraint violation.
func IsCheckViolation(err error) bool { return pgCode(err) == "23514" }

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// ConstraintName returns the violated constraint name, if any.
func ConstraintName(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

// Listen runs fn for every NOTIFY on channel until ctx is done. It holds one
// dedicated connection and reconnects with backoff; after each (re)connect it
// calls fn("") so callers can catch up on anything missed while disconnected.
func (d *DB) Listen(ctx context.Context, channel string, log *slog.Logger, fn func(payload string)) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := d.listenOnce(ctx, channel, fn)
		if ctx.Err() != nil {
			return
		}
		log.Warn("listen connection lost; reconnecting", "channel", channel, "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (d *DB) listenOnce(ctx context.Context, channel string, fn func(string)) error {
	pooled, err := d.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	// The connection stays in LISTEN mode, so take it out of the pool for good.
	conn := pooled.Hijack()
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize()); err != nil {
		return err
	}
	fn("")
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		fn(n.Payload)
	}
}
