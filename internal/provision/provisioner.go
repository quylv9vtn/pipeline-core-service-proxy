package provision

import (
	"context"
	"fmt"
	"time"

	"github.com/golang/glog"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/metrics"
)

// Regions looks up region configs (satisfied by *config.Store).
type Regions interface {
	Endpoint(region string) (endpoint string, ok bool)
}

// Provisioner owns the provisioning state machine:
//
//	Put:      upsert row (provisioning) → fan-out namespace create → ready
//	          (idempotent: an already-ready row with identical payload is a
//	          no-op success; regional failure keeps 'provisioning')
//	Delete:   remove the mapping (namespace cleanup inside the cluster is
//	          the regional KFP's operational responsibility)
//	Run:      background reconciler retrying stale 'provisioning' rows
type Provisioner struct {
	store   Store
	regions Regions
	client  *KFPClient
}

func New(store Store, regions Regions, client *KFPClient) *Provisioner {
	return &Provisioner{store: store, regions: regions, client: client}
}

// Put implements get-or-create semantics. Returns the row and whether this
// call created it.
func (p *Provisioner) Put(ctx context.Context, in PutInput) (ProjectRegion, bool, error) {
	row, created, err := p.store.Upsert(ctx, in)
	if err != nil {
		return ProjectRegion{}, false, fmt.Errorf("upsert: %w", err)
	}

	// Idempotency fast path: ready + identical payload → success, no fan-out.
	if !created && row.Status == StatusReady &&
		row.Namespace == in.Namespace && row.Name == in.Name {
		return row, created, nil
	}

	row, err = p.ensureNamespace(ctx, in)
	return row, created, err
}

// ensureNamespace runs the fan-out and flips the row status accordingly.
func (p *Provisioner) ensureNamespace(ctx context.Context, in PutInput) (ProjectRegion, error) {
	row, err := p.store.MarkProvisioning(ctx, in)
	if err != nil {
		return ProjectRegion{}, fmt.Errorf("mark provisioning: %w", err)
	}

	endpoint, ok := p.regions.Endpoint(in.Region)
	if !ok {
		metrics.ProvisioningOps.WithLabelValues(in.Region, "rejected").Inc()
		return row, fmt.Errorf("unknown region %q", in.Region)
	}
	if err := p.client.EnsureNamespace(ctx, in.Region, endpoint, in.ProjectID, in.Name, in.Namespace); err != nil {
		// Keep 'provisioning': the reconciler retries.
		metrics.ProvisioningOps.WithLabelValues(in.Region, "failed").Inc()
		glog.Errorf("provision %s/%s: namespace fan-out failed: %v", in.Region, in.ProjectID, err)
		return row, nil
	}
	ready, err := p.store.MarkStatus(ctx, in.ProjectID, in.Region, StatusReady)
	if err != nil {
		return row, fmt.Errorf("mark ready: %w", err)
	}
	metrics.ProvisioningOps.WithLabelValues(in.Region, "ready").Inc()
	return ready, nil
}

// Delete removes the mapping. Missing row → ErrNotFound.
func (p *Provisioner) Delete(ctx context.Context, projectID, region string) error {
	return p.store.Delete(ctx, projectID, region)
}

// Run is the background reconciler: every interval it re-runs the namespace
// fan-out for rows stuck in 'provisioning' past twice the interval.
func (p *Provisioner) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.reconcileOnce(ctx, 2*interval)
		}
	}
}

func (p *Provisioner) reconcileOnce(ctx context.Context, staleAfter time.Duration) {
	stale, err := p.store.ListStale(ctx, time.Now().Add(-staleAfter))
	if err != nil {
		glog.Errorf("provision reconciler: list stale: %v", err)
		return
	}
	for _, row := range stale {
		in := PutInput{
			ProjectID: row.ProjectID,
			Name:      row.Name,
			Region:    row.RegionName,
			Namespace: row.Namespace,
		}
		if _, err := p.ensureNamespace(ctx, in); err != nil {
			glog.Errorf("provision reconciler: %s/%s: %v", row.RegionName, row.ProjectID, err)
		}
	}
}
