package namespace

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// entry is a cached resolution outcome. ok=false is a negative entry
// (unprovisioned project) cached briefly to absorb retry storms.
type entry struct {
	namespace string
	ok        bool
	expires   time.Time
}

type cacheKey struct {
	projectID string
	region    string
}

// Store resolves (project_id, region) → namespace. *Resolver implements it;
// the interface keeps the HTTP middleware testable without a database.
type Store interface {
	// Resolve returns the namespace. ok=false: not provisioned; dbErr=true:
	// backend failure (caller surfaces 500). dbErr and ok are mutually exclusive.
	Resolve(ctx context.Context, projectID, regionName string) (ns string, ok bool, dbErr bool)
}

// Resolver looks up (project_id, region) → namespace with an in-process TTL
// cache in front of the project_regions table (the hot path of every
// project-scoped request).
type Resolver struct {
	pool   *pgxpool.Pool
	posTTL time.Duration
	negTTL time.Duration

	mu    sync.Mutex
	cache map[cacheKey]entry
	now   func() time.Time // stubbed in tests
}

func NewResolver(pool *pgxpool.Pool, posTTL time.Duration) *Resolver {
	negTTL := posTTL / 10
	if negTTL < time.Second {
		negTTL = time.Second
	}
	return &Resolver{
		pool:   pool,
		posTTL: posTTL,
		negTTL: negTTL,
		cache:  make(map[cacheKey]entry),
		now:    time.Now,
	}
}

// Resolve returns the namespace for the project in the region.
// ok=false means not provisioned (or not ready); dbErr reports a backend
// failure (never cached).
func (r *Resolver) Resolve(ctx context.Context, projectID, regionName string) (ns string, ok, dbErr bool) {
	key := cacheKey{projectID, regionName}

	r.mu.Lock()
	if e, hit := r.cache[key]; hit && r.now().Before(e.expires) {
		r.mu.Unlock()
		return e.namespace, e.ok, false
	}
	r.mu.Unlock()

	var namespace, status string
	err := r.pool.QueryRow(ctx,
		"SELECT namespace, status FROM project_regions WHERE project_id = $1 AND region_name = $2",
		projectID, regionName,
	).Scan(&namespace, &status)
	if err != nil {
		// No row → negative cache; anything else is a backend error the
		// caller must surface as 500.
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", false, true
		}
		r.store(key, entry{ok: false, expires: r.now().Add(r.negTTL)})
		return "", false, false
	}
	if status != "ready" {
		r.store(key, entry{ok: false, expires: r.now().Add(r.negTTL)})
		return "", false, false
	}
	r.store(key, entry{namespace: namespace, ok: true, expires: r.now().Add(r.posTTL)})
	return namespace, true, false
}

// Invalidate drops the cached entry so a provisioning flip is visible
// immediately to the replica that performed it.
func (r *Resolver) Invalidate(projectID, regionName string) {
	r.mu.Lock()
	delete(r.cache, cacheKey{projectID, regionName})
	r.mu.Unlock()
}

func (r *Resolver) store(key cacheKey, e entry) {
	r.mu.Lock()
	r.cache[key] = e
	r.mu.Unlock()
}
