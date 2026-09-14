package provision

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound when the (project, region) row does not exist.
var ErrNotFound = errors.New("project region not found")

// PGStore implements Store on PostgreSQL.
type PGStore struct {
	pool *pgxpool.Pool
}

func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

const rowCols = `project_id, region_name, namespace, name, status, created_at, updated_at`

func scanRow(row pgx.Row) (ProjectRegion, error) {
	var p ProjectRegion
	err := row.Scan(&p.ProjectID, &p.RegionName, &p.Namespace, &p.Name, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func (s *PGStore) Upsert(ctx context.Context, in PutInput) (ProjectRegion, bool, error) {
	// Single statement: insert-or-return-existing, no read-modify-write race.
	row := s.pool.QueryRow(ctx, `
		WITH ins AS (
			INSERT INTO project_regions (project_id, region_name, namespace, name, status)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (project_id, region_name) DO NOTHING
			RETURNING `+rowCols+`
		)
		SELECT *, true  AS created FROM ins
		UNION ALL
		SELECT *, false FROM project_regions
		WHERE project_id = $1 AND region_name = $2
		LIMIT 1`,
		in.ProjectID, in.Region, in.Namespace, in.Name, StatusProvisioning)

	var p ProjectRegion
	var created bool
	err := row.Scan(&p.ProjectID, &p.RegionName, &p.Namespace, &p.Name, &p.Status, &p.CreatedAt, &p.UpdatedAt, &created)
	if err != nil {
		return ProjectRegion{}, false, err
	}
	return p, created, nil
}

func (s *PGStore) MarkProvisioning(ctx context.Context, in PutInput) (ProjectRegion, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE project_regions SET namespace = $3, name = $4, status = $5, updated_at = now()
		WHERE project_id = $1 AND region_name = $2
		RETURNING `+rowCols,
		in.ProjectID, in.Region, in.Namespace, in.Name, StatusProvisioning)
	p, err := scanRow(row)
	return p, mapErr(err)
}

func (s *PGStore) MarkStatus(ctx context.Context, projectID, region, status string) (ProjectRegion, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE project_regions SET status = $3, updated_at = now()
		WHERE project_id = $1 AND region_name = $2
		RETURNING `+rowCols,
		projectID, region, status)
	p, err := scanRow(row)
	return p, mapErr(err)
}

func (s *PGStore) Delete(ctx context.Context, projectID, region string) error {
	tag, err := s.pool.Exec(ctx,
		"DELETE FROM project_regions WHERE project_id = $1 AND region_name = $2",
		projectID, region)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) ListStale(ctx context.Context, olderThan time.Time) ([]ProjectRegion, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+rowCols+` FROM project_regions
		WHERE status = $1 AND updated_at < $2
		ORDER BY updated_at`, StatusProvisioning, olderThan)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectRegion
	for rows.Next() {
		var p ProjectRegion
		if err := rows.Scan(&p.ProjectID, &p.RegionName, &p.Namespace, &p.Name, &p.Status, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
