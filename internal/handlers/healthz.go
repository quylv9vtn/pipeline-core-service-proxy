// Package handlers implements the gateway-owned endpoints: /healthz,
// /regions. Everything else is proxied.
package handlers

import (
	"encoding/json"
	"net/http"
)

// Healthz mirrors the apiserver's inline JSON style. It reports gateway
// liveness only — region health lives on /regions so readiness never
// flaps when a regional apiserver restarts.
func Healthz(version string, regionCount func() int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":             "ok",
			"version":            version,
			"regions_configured": regionCount(),
		})
	}
}
