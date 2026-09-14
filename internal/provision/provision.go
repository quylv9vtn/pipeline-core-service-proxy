// Package provision implements the CMP-facing provisioning API: idempotent
// get-or-create of the (project, region) → namespace mapping, fan-out of
// namespace creation to the regional KFP, and a background reconciler that
// retries rows stuck in 'provisioning'.
package provision

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ProjectRegion is one project_regions row.
type ProjectRegion struct {
	ProjectID  string    `json:"project_id"`
	RegionName string    `json:"region"`
	Namespace  string    `json:"namespace"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Provisioning statuses.
const (
	StatusProvisioning = "provisioning"
	StatusReady        = "ready"
	StatusFailed       = "failed"
)

// namespaceRe matches RFC 1123 DNS labels (K8s namespace rules).
var namespaceRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// ValidateNamespace enforces the namespace contract from the requirements:
// '{slug}-pipeline', ≤ 63 chars, RFC 1123 label.
func ValidateNamespace(namespace, slug string) error {
	if namespace == "" {
		return fmt.Errorf("namespace is required")
	}
	if len(namespace) > 63 {
		return fmt.Errorf("namespace must be at most 63 characters (RFC 1123 label)")
	}
	if !namespaceRe.MatchString(namespace) {
		return fmt.Errorf("namespace must be an RFC 1123 DNS label (lowercase alphanumeric and '-', start/end alphanumeric)")
	}
	if want := slug + "-pipeline"; namespace != want {
		return fmt.Errorf("namespace must be %q", want)
	}
	return nil
}

// PutInput is the CMP provisioning payload (PUT /apis/v2beta1/projects).
type PutInput struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`     // project slug
	Region    string `json:"region"`   // region name (routed by the gateway contract)
	Namespace string `json:"namespace"` // '{slug}-pipeline'
}

// ValidatePut checks the payload before any DB write.
func ValidatePut(in PutInput) error {
	if strings.TrimSpace(in.ProjectID) == "" {
		return fmt.Errorf("project_id is required")
	}
	if !uuidRe.MatchString(in.ProjectID) {
		return fmt.Errorf("project_id must be a UUID")
	}
	if !slugRe.MatchString(strings.TrimSpace(in.Name)) {
		return fmt.Errorf("name (project slug) is required, lowercase alphanumeric and '-', max 40 characters")
	}
	if strings.TrimSpace(in.Region) == "" {
		return fmt.Errorf("region is required")
	}
	return ValidateNamespace(in.Namespace, strings.TrimSpace(in.Name))
}

var (
	uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	slugRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$`)
)

// Store persists project_regions rows (interface for tests).
type Store interface {
	// Upsert inserts the row (status provisioning) or returns the existing
	// one unchanged; created reports an actual insert.
	Upsert(ctx context.Context, in PutInput) (ProjectRegion, bool, error)
	// MarkProvisioning records a (re)provisioning attempt with the wanted
	// payload and resets status to provisioning.
	MarkProvisioning(ctx context.Context, in PutInput) (ProjectRegion, error)
	// MarkStatus flips the status of an existing row.
	MarkStatus(ctx context.Context, projectID, region, status string) (ProjectRegion, error)
	Delete(ctx context.Context, projectID, region string) error
	// ListStale returns rows stuck in provisioning with updated_at older
	// than the cutoff (reconciler input, oldest first).
	ListStale(ctx context.Context, olderThan time.Time) ([]ProjectRegion, error)
}
