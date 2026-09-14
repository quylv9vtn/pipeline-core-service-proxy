package store

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMigrate runs against a real PostgreSQL when TEST_DATABASE_URL is set
// (e.g. docker run -p 5432:5432 -e POSTGRES_PASSWORD=postgres postgres:16).
// Skipped otherwise; CI/dev without a DSN still runs the full unit suite.
func TestMigrate(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB migration test")
	}
	ctx := context.Background()
	pool, err := New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	require.NoError(t, Migrate(ctx, pool))

	var tableCount int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'public'
		  AND table_name IN ('project_regions', 'flavors', 'images', 'schema_versions')
	`).Scan(&tableCount))
	require.Equal(t, 4, tableCount)

	// Re-run: idempotent.
	require.NoError(t, Migrate(ctx, pool))
}
