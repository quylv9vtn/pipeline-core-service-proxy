package provision

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gorilla/mux"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/region"
)

// PutProject serves PUT /apis/v2beta1/projects (CMP service activation,
// service-key guarded): idempotent get-or-create + namespace fan-out.
//
// Status codes: 201 created, 200 already present (idempotent replay or
// reconciled), 400 invalid payload, 500 regional failure kept as
// 'provisioning' for the background reconciler (the response still carries
// the row with status provisioning — CMP treats non-2xx as activation
// failure and retries, which lands in the same idempotent path).
func PutProject(p *Provisioner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in PutInput
		defer func() { _ = r.Body.Close() }()
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			(&region.APIError{Status: http.StatusBadRequest, Code: "invalid_payload",
				Message: "Invalid JSON body: " + err.Error()}).Write(w)
			return
		}
		if err := ValidatePut(in); err != nil {
			(&region.APIError{Status: http.StatusBadRequest, Code: "invalid_payload",
				Message: err.Error()}).Write(w)
			return
		}

		row, created, err := p.Put(r.Context(), in)
		if err != nil {
			(&region.APIError{Status: http.StatusInternalServerError, Code: "provisioning_failed",
				Message: err.Error(), Region: in.Region}).Write(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if created {
			w.WriteHeader(http.StatusCreated)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		_ = json.NewEncoder(w).Encode(row)
	}
}

// DeleteProject serves DELETE /apis/v2beta1/projects/{project_id}?region=
// (CMP service deactivation). The gateway removes the mapping; resources
// inside the namespace are the regional KFP's operational responsibility.
func DeleteProject(p *Provisioner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := mux.Vars(r)["project_id"]
		regionName := r.URL.Query().Get("region")
		if regionName == "" {
			(&region.APIError{Status: http.StatusBadRequest, Code: "region_required",
				Message: "The ?region= query parameter is required."}).Write(w)
			return
		}
		if err := p.Delete(r.Context(), projectID, regionName); err != nil {
			if errors.Is(err, ErrNotFound) {
				// Idempotent deactivation.
				w.WriteHeader(http.StatusNoContent)
				return
			}
			(&region.APIError{Status: http.StatusInternalServerError, Code: "internal_error",
				Message: "Failed to remove the project mapping."}).Write(w)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
