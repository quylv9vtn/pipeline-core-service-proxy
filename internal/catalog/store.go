// Package catalog owns the region-scoped flavor/image resource catalog
// (moved out of CMP). FE reads enabled rows; CMP-side ops manage the
// catalog through the service-key-guarded admin API.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GPU models mirror the CMP PipelineGPUModel enum values so the FE contract
// is unchanged. They map 1:1 to quota units on the regional KFP.
const (
	GPUModelRTX2080 = "ai_rtx2080" // NVIDIA-GeForce-RTX-2080
	GPUModelH200    = "ai_h200"    // NVIDIA-H200
)

// Status values (CMP PipelineResourceStatus).
const (
	StatusEnabled  = "enabled"
	StatusDisabled = "disabled"
)

// ErrNotFound is returned when the row does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned on unique-constraint violations.
var ErrConflict = errors.New("conflict")

// Flavor mirrors the CMP PipelineFlavor API shape (region reduced to the
// region name — the gateway has no region UUID table).
type Flavor struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	RegionName  string    `json:"region_name"`
	CPUMilli    int       `json:"cpu_milli"`
	MemoryMiB   int       `json:"memory_mib"`
	GPUModel    *string   `json:"gpu_model"`
	GPUCount    int       `json:"gpu_count"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Image mirrors the CMP PipelineImage API shape.
type Image struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	RegionName  string    `json:"region_name"`
	Version     string    `json:"version"`
	ImageURL    string    `json:"image_url"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Store is the persistence surface of the catalog (interface for tests).
type Store interface {
	ListFlavors(ctx context.Context, region string, enabledOnly bool) ([]Flavor, error)
	GetFlavor(ctx context.Context, id string) (Flavor, error)
	CreateFlavor(ctx context.Context, f Flavor) (Flavor, error)
	UpdateFlavor(ctx context.Context, f Flavor) (Flavor, error)
	DeleteFlavor(ctx context.Context, id string) error

	ListImages(ctx context.Context, region string, enabledOnly bool) ([]Image, error)
	GetImage(ctx context.Context, id string) (Image, error)
	CreateImage(ctx context.Context, i Image) (Image, error)
	UpdateImage(ctx context.Context, i Image) (Image, error)
	DeleteImage(ctx context.Context, id string) error
}

// PGStore implements Store on PostgreSQL.
type PGStore struct {
	pool *pgxpool.Pool
}

func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

func (s *PGStore) ListFlavors(ctx context.Context, region string, enabledOnly bool) ([]Flavor, error) {
	q := `SELECT id, name, description, region_name, cpu_milli, memory_mib, gpu_model, gpu_count, status, created_at, updated_at
	      FROM flavors WHERE region_name = $1`
	args := []any{region}
	if enabledOnly {
		q += " AND status = 'enabled'"
	}
	q += " ORDER BY name"
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	ptrs, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByName[Flavor])
	if err != nil {
		return nil, err
	}
	out := make([]Flavor, len(ptrs))
	for i, p := range ptrs {
		out[i] = *p
	}
	return out, nil
}

func (s *PGStore) GetFlavor(ctx context.Context, id string) (Flavor, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, name, description, region_name, cpu_milli, memory_mib, gpu_model, gpu_count, status, created_at, updated_at
		 FROM flavors WHERE id = $1`, id)
	var f Flavor
	err := row.Scan(&f.ID, &f.Name, &f.Description, &f.RegionName, &f.CPUMilli, &f.MemoryMiB, &f.GPUModel, &f.GPUCount, &f.Status, &f.CreatedAt, &f.UpdatedAt)
	return f, mapErr(err)
}

func (s *PGStore) CreateFlavor(ctx context.Context, f Flavor) (Flavor, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO flavors (id, region_name, name, description, cpu_milli, memory_mib, gpu_model, gpu_count, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, name, description, region_name, cpu_milli, memory_mib, gpu_model, gpu_count, status, created_at, updated_at`,
		f.ID, f.RegionName, f.Name, f.Description, f.CPUMilli, f.MemoryMiB, f.GPUModel, f.GPUCount, f.Status)
	return scanFlavor(row)
}

func (s *PGStore) UpdateFlavor(ctx context.Context, f Flavor) (Flavor, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE flavors SET name = $2, description = $3, cpu_milli = $4, memory_mib = $5,
		                   gpu_model = $6, gpu_count = $7, status = $8, updated_at = now()
		WHERE id = $1
		RETURNING id, name, description, region_name, cpu_milli, memory_mib, gpu_model, gpu_count, status, created_at, updated_at`,
		f.ID, f.Name, f.Description, f.CPUMilli, f.MemoryMiB, f.GPUModel, f.GPUCount, f.Status)
	return scanFlavor(row)
}

func (s *PGStore) DeleteFlavor(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM flavors WHERE id = $1", id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) ListImages(ctx context.Context, region string, enabledOnly bool) ([]Image, error) {
	q := `SELECT id, name, description, region_name, version, image_url, status, created_at, updated_at
	      FROM images WHERE region_name = $1`
	args := []any{region}
	if enabledOnly {
		q += " AND status = 'enabled'"
	}
	q += " ORDER BY name, version"
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	ptrs, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByName[Image])
	if err != nil {
		return nil, err
	}
	out := make([]Image, len(ptrs))
	for i, p := range ptrs {
		out[i] = *p
	}
	return out, nil
}

func (s *PGStore) GetImage(ctx context.Context, id string) (Image, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, name, description, region_name, version, image_url, status, created_at, updated_at
		 FROM images WHERE id = $1`, id)
	var i Image
	err := row.Scan(&i.ID, &i.Name, &i.Description, &i.RegionName, &i.Version, &i.ImageURL, &i.Status, &i.CreatedAt, &i.UpdatedAt)
	return i, mapErr(err)
}

func (s *PGStore) CreateImage(ctx context.Context, i Image) (Image, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO images (id, region_name, name, description, version, image_url, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, name, description, region_name, version, image_url, status, created_at, updated_at`,
		i.ID, i.RegionName, i.Name, i.Description, i.Version, i.ImageURL, i.Status)
	return scanImage(row)
}

func (s *PGStore) UpdateImage(ctx context.Context, i Image) (Image, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE images SET name = $2, description = $3, version = $4, image_url = $5,
		                  status = $6, updated_at = now()
		WHERE id = $1
		RETURNING id, name, description, region_name, version, image_url, status, created_at, updated_at`,
		i.ID, i.Name, i.Description, i.Version, i.ImageURL, i.Status)
	return scanImage(row)
}

func (s *PGStore) DeleteImage(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM images WHERE id = $1", id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanFlavor(row pgx.Row) (Flavor, error) {
	var f Flavor
	err := row.Scan(&f.ID, &f.Name, &f.Description, &f.RegionName, &f.CPUMilli, &f.MemoryMiB, &f.GPUModel, &f.GPUCount, &f.Status, &f.CreatedAt, &f.UpdatedAt)
	return f, mapErr(err)
}

func scanImage(row pgx.Row) (Image, error) {
	var i Image
	err := row.Scan(&i.ID, &i.Name, &i.Description, &i.RegionName, &i.Version, &i.ImageURL, &i.Status, &i.CreatedAt, &i.UpdatedAt)
	return i, mapErr(err)
}

// mapErr translates driver errors to package-level sentinels.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if isUniqueViolation(err) {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return err
}
