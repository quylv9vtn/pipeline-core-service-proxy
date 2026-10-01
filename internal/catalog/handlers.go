package catalog

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/region"
)

// handler bundles the store with JSON helpers.
type handler struct {
	store Store
}

// catalogListRegion maps the resolved region to the store's region filter:
// the all-regions sentinel becomes the empty filter (every region, each row
// carrying its own region_name); anything else filters to that region.
func catalogListRegion(res *region.Result) string {
	if res != nil && res.Name == region.AllRegions {
		return ""
	}
	return res.Name
}

// FEListFlavors serves GET /apis/v2beta1/flavors — enabled-only rows of the
// region resolved by the Region middleware (region-scoped, no Project-ID).
// X-Region: all lists enabled rows of every region.
func FEListFlavors(store Store) http.HandlerFunc {
	h := handler{store: store}
	return func(w http.ResponseWriter, r *http.Request) {
		res, ok := region.FromContext(r.Context())
		if !ok {
			(&region.APIError{Status: http.StatusInternalServerError, Code: "internal_error",
				Message: "No region resolved for request."}).Write(w)
			return
		}
		flavors, err := h.store.ListFlavors(r.Context(), catalogListRegion(res), true)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if flavors == nil {
			flavors = []Flavor{}
		}
		writeJSON(w, http.StatusOK, flavors)
	}
}

// FEListImages serves GET /apis/v2beta1/images — enabled-only rows.
// X-Region: all lists enabled rows of every region.
func FEListImages(store Store) http.HandlerFunc {
	h := handler{store: store}
	return func(w http.ResponseWriter, r *http.Request) {
		res, ok := region.FromContext(r.Context())
		if !ok {
			(&region.APIError{Status: http.StatusInternalServerError, Code: "internal_error",
				Message: "No region resolved for request."}).Write(w)
			return
		}
		images, err := h.store.ListImages(r.Context(), catalogListRegion(res), true)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if images == nil {
			images = []Image{}
		}
		writeJSON(w, http.StatusOK, images)
	}
}

// AdminListFlavors serves GET /apis/v2beta1/admin/flavors (all statuses).
func AdminListFlavors(store Store) http.HandlerFunc {
	h := handler{store: store}
	return func(w http.ResponseWriter, r *http.Request) {
		regionName := strings.TrimSpace(r.URL.Query().Get("region"))
		if regionName == "" {
			(&region.APIError{Status: http.StatusBadRequest, Code: "region_required",
				Message: "The ?region= query parameter is required."}).Write(w)
			return
		}
		flavors, err := h.store.ListFlavors(r.Context(), regionName, false)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if flavors == nil {
			flavors = []Flavor{}
		}
		writeJSON(w, http.StatusOK, flavors)
	}
}

// AdminCreateFlavor serves POST /apis/v2beta1/admin/flavors. The server
// assigns the UUID; region_name comes from the payload.
func AdminCreateFlavor(store Store) http.HandlerFunc {
	h := handler{store: store}
	return func(w http.ResponseWriter, r *http.Request) {
		var f Flavor
		if !decode(w, r, &f) {
			return
		}
		if err := ValidateFlavor(f); err != nil {
			badRequest(w, err.Error())
			return
		}
		if f.Status == "" {
			f.Status = StatusEnabled
		}
		f.ID = uuid.NewString()
		created, err := h.store.CreateFlavor(r.Context(), f)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, created)
	}
}

// AdminUpdateFlavor serves PUT /apis/v2beta1/admin/flavors/{id}. Region is
// immutable after creation.
func AdminUpdateFlavor(store Store) http.HandlerFunc {
	h := handler{store: store}
	return func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]
		current, err := h.store.GetFlavor(r.Context(), id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		var in Flavor
		if !decode(w, r, &in) {
			return
		}
		// Merge onto current: region immutable, empty status keeps current.
		in.ID = current.ID
		in.RegionName = current.RegionName
		if in.Status == "" {
			in.Status = current.Status
		}
		if err := ValidateFlavor(in); err != nil {
			badRequest(w, err.Error())
			return
		}
		updated, err := h.store.UpdateFlavor(r.Context(), in)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	}
}

// AdminDeleteFlavor serves DELETE /apis/v2beta1/admin/flavors/{id}.
func AdminDeleteFlavor(store Store) http.HandlerFunc {
	h := handler{store: store}
	return func(w http.ResponseWriter, r *http.Request) {
		err := h.store.DeleteFlavor(r.Context(), mux.Vars(r)["id"])
		if err != nil {
			writeStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// AdminListImages serves GET /apis/v2beta1/admin/images (all statuses).
func AdminListImages(store Store) http.HandlerFunc {
	h := handler{store: store}
	return func(w http.ResponseWriter, r *http.Request) {
		regionName := strings.TrimSpace(r.URL.Query().Get("region"))
		if regionName == "" {
			(&region.APIError{Status: http.StatusBadRequest, Code: "region_required",
				Message: "The ?region= query parameter is required."}).Write(w)
			return
		}
		images, err := h.store.ListImages(r.Context(), regionName, false)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if images == nil {
			images = []Image{}
		}
		writeJSON(w, http.StatusOK, images)
	}
}

// AdminCreateImage serves POST /apis/v2beta1/admin/images.
func AdminCreateImage(store Store) http.HandlerFunc {
	h := handler{store: store}
	return func(w http.ResponseWriter, r *http.Request) {
		var i Image
		if !decode(w, r, &i) {
			return
		}
		if err := ValidateImage(i); err != nil {
			badRequest(w, err.Error())
			return
		}
		if i.Status == "" {
			i.Status = StatusEnabled
		}
		i.ID = uuid.NewString()
		created, err := h.store.CreateImage(r.Context(), i)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, created)
	}
}

// AdminUpdateImage serves PUT /apis/v2beta1/admin/images/{id}. Region is
// immutable after creation.
func AdminUpdateImage(store Store) http.HandlerFunc {
	h := handler{store: store}
	return func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]
		current, err := h.store.GetImage(r.Context(), id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		var in Image
		if !decode(w, r, &in) {
			return
		}
		in.ID = current.ID
		in.RegionName = current.RegionName
		if in.Status == "" {
			in.Status = current.Status
		}
		if err := ValidateImage(in); err != nil {
			badRequest(w, err.Error())
			return
		}
		updated, err := h.store.UpdateImage(r.Context(), in)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	}
}

// AdminDeleteImage serves DELETE /apis/v2beta1/admin/images/{id}.
func AdminDeleteImage(store Store) http.HandlerFunc {
	h := handler{store: store}
	return func(w http.ResponseWriter, r *http.Request) {
		err := h.store.DeleteImage(r.Context(), mux.Vars(r)["id"])
		if err != nil {
			writeStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	defer func() { _ = r.Body.Close() }()
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(into); err != nil {
		badRequest(w, "Invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func badRequest(w http.ResponseWriter, msg string) {
	(&region.APIError{Status: http.StatusBadRequest, Code: "invalid_payload", Message: msg}).Write(w)
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		(&region.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Resource not found."}).Write(w)
	case errors.Is(err, ErrConflict):
		(&region.APIError{Status: http.StatusConflict, Code: "conflict",
			Message: "A resource with the same unique key already exists in this region."}).Write(w)
	default:
		(&region.APIError{Status: http.StatusInternalServerError, Code: "internal_error",
			Message: "Catalog operation failed."}).Write(w)
	}
}
