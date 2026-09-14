package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/golang/glog"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/region"
)

// statusRecorder captures the response status and size for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// regionHolder lets the outer Logging middleware observe the region that the
// inner Region middleware resolved: r.WithContext cannot propagate a value
// back into the caller's request, so Region writes through this pointer.
type regionHolder struct {
	name string
}

type regionHolderKey struct{}

func withRegionHolder(ctx context.Context) context.Context {
	return context.WithValue(ctx, regionHolderKey{}, &regionHolder{})
}

// loggedRegion reports the region for the request log: the middleware-resolved
// value when available (any source: header, query, or JSON body), else the
// raw X-Region header.
func loggedRegion(r *http.Request) string {
	if h, ok := r.Context().Value(regionHolderKey{}).(*regionHolder); ok && h.name != "" {
		return h.name
	}
	return r.Header.Get(region.HeaderName)
}

// Logging emits one glog line per request. /metrics scrapes are skipped.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		r = r.WithContext(withRegionHolder(r.Context()))
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		glog.Infof("%s %s region=%s status=%d dur=%s bytes=%d",
			r.Method, r.URL.RequestURI(), loggedRegion(r), rec.status, time.Since(start), rec.bytes)
	})
}
