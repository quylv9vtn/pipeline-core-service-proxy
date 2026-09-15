package catalog

import (
	"errors"
	"fmt"
	"strings"
)

// isUniqueViolation reports PostgreSQL unique_violation (23505) without
// pulling in the pgconn error type at every call site.
func isUniqueViolation(err error) bool {
	var s interface{ SQLState() string }
	if errors.As(err, &s) {
		return s.SQLState() == "23505"
	}
	return false
}

// ValidateFlavor checks admin payload invariants that the DB CHECK
// constraints would otherwise surface as raw 500s.
func ValidateFlavor(f Flavor) error {
	if strings.TrimSpace(f.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(f.RegionName) == "" {
		return fmt.Errorf("region_name is required")
	}
	if f.CPUMilli < 1 {
		return fmt.Errorf("cpu_milli must be >= 1")
	}
	if f.MemoryMiB < 1 {
		return fmt.Errorf("memory_mib must be >= 1")
	}
	if (f.GPUModel == nil || *f.GPUModel == "") != (f.GPUCount == 0) {
		return fmt.Errorf("gpu_model and gpu_count must either both be set or both be empty")
	}
	if f.GPUModel != nil && *f.GPUModel != GPUModelRTX2080 && *f.GPUModel != GPUModelH200 {
		return fmt.Errorf("gpu_model must be %q or %q", GPUModelRTX2080, GPUModelH200)
	}
	if f.GPUCount < 0 {
		return fmt.Errorf("gpu_count must be >= 0")
	}
	if f.Status != "" && f.Status != StatusEnabled && f.Status != StatusDisabled {
		return fmt.Errorf("status must be %q or %q", StatusEnabled, StatusDisabled)
	}
	return nil
}

// ValidateImage checks admin payload invariants for images.
func ValidateImage(i Image) error {
	if strings.TrimSpace(i.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(i.RegionName) == "" {
		return fmt.Errorf("region_name is required")
	}
	if strings.TrimSpace(i.Version) == "" {
		return fmt.Errorf("version is required")
	}
	if strings.TrimSpace(i.ImageURL) == "" {
		return fmt.Errorf("image_url is required")
	}
	if i.Status != "" && i.Status != StatusEnabled && i.Status != StatusDisabled {
		return fmt.Errorf("status must be %q or %q", StatusEnabled, StatusDisabled)
	}
	return nil
}
