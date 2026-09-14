package store

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"

	"github.com/golang/glog"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// advisoryLockKey is an arbitrary constant scoping pg_advisory_lock so
// concurrent gateway replicas never apply the same migration twice.
const advisoryLockKey = 0x70697065 // "pipe"

// Migrate applies all embedded migrations that the schema_versions table
// does not record yet. A session-level advisory lock serializes concurrent
// replicas; each migration runs in its own transaction and is recorded in
// the same transaction (exactly-once).
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire: %w", err)
	}
	defer conn.Release()

	_, err = conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey)
	if err != nil {
		return fmt.Errorf("advisory lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", advisoryLockKey)
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_versions (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_versions: %w", err)
	}

	names, err := migrationNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		var exists bool
		if err := conn.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM schema_versions WHERE version = $1)", name,
		).Scan(&exists); err != nil {
			return fmt.Errorf("check %s: %w", name, err)
		}
		if exists {
			continue
		}
		if err := applyOne(ctx, conn, name); err != nil {
			return err
		}
		glog.Infof("migration applied: %s", name)
	}
	return nil
}

func migrationNames() ([]string, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // NNNN_ prefix gives the order
	return names, nil
}

func applyOne(ctx context.Context, conn *pgxpool.Conn, name string) error {
	sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin %s: %w", name, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
		return fmt.Errorf("apply %s: %w", name, err)
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO schema_versions (version) VALUES ($1)", name); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return tx.Commit(ctx)
}
