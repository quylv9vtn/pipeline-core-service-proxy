package region

import (
	"encoding/json"
	"net/http"
)

// Source records where the region came from.
type Source string

const (
	SourceHeader Source = "header"
	SourceQuery  Source = "query"
	SourceBody   Source = "body"
)

// HeaderName is the canonical region header.
const HeaderName = "X-Region"

// QueryParam is the canonical region query parameter.
const QueryParam = "region"

// JSONField is the reserved top-level region field in JSON bodies.
const JSONField = "region"

const regionHint = "Provide it via the X-Region header, a ?region= query parameter, or a top-level \"region\" field in a JSON request body."

// APIError is a gateway-native error rendered as JSON.
type APIError struct {
	Status       int      `json:"-"`
	Code         string   `json:"error"`
	Message      string   `json:"message"`
	ValidRegions []string `json:"valid_regions,omitempty"`
	Hint         string   `json:"hint,omitempty"`
	Region       string   `json:"region,omitempty"`
}

func (e *APIError) Write(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(e)
}

func missingRegion(valid []string) *APIError {
	return &APIError{
		Status:       http.StatusBadRequest,
		Code:         "region_required",
		Message:      "A region is required on every request.",
		ValidRegions: valid,
		Hint:         regionHint,
	}
}

func UnknownRegion(name string, valid []string) *APIError {
	return &APIError{
		Status:       http.StatusBadRequest,
		Code:         "unknown_region",
		Message:      "Unknown region \"" + name + "\".",
		Region:       name,
		ValidRegions: valid,
		Hint:         regionHint,
	}
}

func invalidRegionField() *APIError {
	return &APIError{
		Status:  http.StatusBadRequest,
		Code:    "invalid_region_field",
		Message: "The top-level \"region\" field must be a JSON string.",
		Hint:    regionHint,
	}
}

func multipartRegion() *APIError {
	return &APIError{
		Status: http.StatusBadRequest,
		Code:   "region_required_for_multipart",
		Message: "Multipart uploads must carry the region via the X-Region header or the ?region= " +
			"query parameter; the gateway never parses multipart bodies.",
		Hint: regionHint,
	}
}

func payloadTooLarge() *APIError {
	return &APIError{
		Status:  http.StatusRequestEntityTooLarge,
		Code:    "payload_too_large",
		Message: "Request body exceeds the gateway JSON buffer limit.",
	}
}

// UpstreamUnavailable builds the 502 returned when a regional apiserver
// cannot be reached. No retry is attempted: a gateway must never replay a
// possibly-applied non-idempotent request.
func UpstreamUnavailable(name string, err error) *APIError {
	msg := "upstream unreachable"
	if err != nil {
		msg += ": " + err.Error()
	}
	return &APIError{
		Status:  http.StatusBadGateway,
		Code:    "upstream_unavailable",
		Message: msg,
		Region:  name,
	}
}
