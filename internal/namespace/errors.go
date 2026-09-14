package namespace

import (
	"net/http"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/region"
)

// Gateway-native errors of the namespace middleware. Every error states the
// project_id so FE can display which entity is at fault.
func ErrMissingProjectID() *region.APIError {
	return &region.APIError{
		Status:  http.StatusBadRequest,
		Code:    "project_id_required",
		Message: "A Project-ID header is required on project-scoped endpoints.",
		Hint:    "The enforcer proxy injects it from the authorized project; direct calls must set it explicitly.",
	}
}

func ErrInvalidProjectID(value string) *region.APIError {
	return &region.APIError{
		Status:  http.StatusBadRequest,
		Code:    "invalid_project_id",
		Message: "The Project-ID header must be a UUID.",
		Region:  value,
	}
}

func ErrProjectMismatch(header, asserted string) *region.APIError {
	return &region.APIError{
		Status:  http.StatusForbidden,
		Code:    "project_mismatch",
		Message: "The Project-ID header does not match the enforcer assertion (X-CMP-Project-ID).",
		Hint:    "header=" + header + " asserted=" + asserted,
	}
}

func ErrNotProvisioned(projectID, regionName string) *region.APIError {
	return &region.APIError{
		Status: http.StatusPreconditionFailed,
		Code:   "project_not_provisioned",
		Message: "The project is not activated for the AI Pipeline service in this region. " +
			"Provision it first (CMP activates the service per project+region).",
		Region: regionName,
		Hint:   "project_id=" + projectID,
	}
}

func ErrNamespaceBlocked(ns, regionName string) *region.APIError {
	return &region.APIError{
		Status: http.StatusNotFound,
		Code:   "not_found",
		Message: "The requested resource does not exist in this project's namespace.",
		Region: regionName,
		Hint:   "namespace=" + ns,
	}
}
