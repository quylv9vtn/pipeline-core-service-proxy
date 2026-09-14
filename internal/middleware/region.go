package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/config"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/metrics"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/region"
)

// statusWriter wraps the ResponseWriter so the region middleware can record
// status-code metrics (including its own 400 error paths).
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Region runs the strict extract→validate→strip pass, then delegates to the
// dispatcher with the resolved region in the request context, recording
// per-region metrics.
func Region(store *config.Store, maxJSONBodyBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}

			res, apiErr := region.Process(r, store, maxJSONBodyBytes)
			if apiErr != nil {
				// Report the attempted region to the outer Logging middleware
				// when the error carries one (e.g. unknown_region).
				if h, ok := r.Context().Value(regionHolderKey{}).(*regionHolder); ok && apiErr.Region != "" {
					h.name = apiErr.Region
				}
				status := apiErr.Status
				metrics.RequestsTotal.WithLabelValues("-", r.Method, strconv.Itoa(status)).Inc()
				metrics.RequestDuration.WithLabelValues("-", r.Method).
					Observe(time.Since(start).Seconds())
				apiErr.Write(sw)
				return
			}

			// Hand the resolved region back to the outer Logging middleware.
			if h, ok := r.Context().Value(regionHolderKey{}).(*regionHolder); ok {
				h.name = res.Name
			}

			regionLabel := res.Name
			metrics.InflightRequests.WithLabelValues(regionLabel).Inc()
			r = r.WithContext(region.IntoContext(r.Context(), res))
			next.ServeHTTP(sw, r)
			metrics.InflightRequests.WithLabelValues(regionLabel).Dec()

			if sw.status == 0 {
				sw.status = http.StatusOK
			}
			metrics.RequestsTotal.WithLabelValues(regionLabel, r.Method, strconv.Itoa(sw.status)).Inc()
			metrics.RequestDuration.WithLabelValues(regionLabel, r.Method).
				Observe(time.Since(start).Seconds())
		})
	}
}
