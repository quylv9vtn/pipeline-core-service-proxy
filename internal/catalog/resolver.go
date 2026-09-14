package catalog

import (
	"context"
	"fmt"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/spec"
)

var _ spec.ResourceLookup = (*SpecResolver)(nil)

// SpecResolver implements the spec.ResourceLookup contract on top of the
// catalog store: flavor/image must exist, be enabled, and belong to the
// same region (the CMP validation contract, unchanged).
type SpecResolver struct {
	Store Store
}

// Resolve looks up both rows and validates the region/status invariants.
func (r *SpecResolver) Resolve(ctx context.Context, regionName, flavorID, imageID string) (spec.Resources, error) {
	flavor, err := r.Store.GetFlavor(ctx, flavorID)
	if err != nil || flavor.RegionName != regionName || flavor.Status != StatusEnabled {
		return spec.Resources{}, fmt.Errorf("flavor %s must exist, be enabled, and belong to the pipeline region", flavorID)
	}
	image, err := r.Store.GetImage(ctx, imageID)
	if err != nil || image.RegionName != regionName || image.Status != StatusEnabled {
		return spec.Resources{}, fmt.Errorf("image %s must exist, be enabled, and belong to the pipeline region", imageID)
	}
	res := spec.Resources{
		ImageURL:  image.ImageURL,
		CPU:       flavor.CPU,
		MemoryGiB: flavor.MemoryGiB,
		GPUCount:  flavor.GPUCount,
	}
	if flavor.GPUModel != nil {
		res.GPUModel = *flavor.GPUModel
	}
	return res, nil
}
