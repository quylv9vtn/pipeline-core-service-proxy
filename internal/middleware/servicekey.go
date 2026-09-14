package middleware

import (
	"crypto/subtle"
	"net/http"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/metrics"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/region"
)

// HeaderServiceKey guards CMP-to-gateway endpoints (provisioning, catalog
// admin, and the service-to-service quota path). CMP sends it in the
// X-Service-Key header.
const HeaderServiceKey = "X-Service-Key"

// ServiceKey rejects requests whose X-Service-Key does not match the
// configured shared secret (constant-time compare). An empty key disables
// the middleware — configuration validation prevents that in production.
func ServiceKey(key string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := r.Header.Get(HeaderServiceKey)
			if key == "" || subtle.ConstantTimeCompare([]byte(got), []byte(key)) != 1 {
				metrics.AuthRejects.WithLabelValues("service_key_invalid").Inc()
				(&region.APIError{
					Status:  http.StatusUnauthorized,
					Code:    "service_key_required",
					Message: "A valid " + HeaderServiceKey + " header is required for this endpoint.",
				}).Write(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
