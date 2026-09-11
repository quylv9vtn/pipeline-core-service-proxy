package middleware

import (
	"net/http"
	"runtime/debug"

	"github.com/golang/glog"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/region"
)

// Recovery converts handler panics into a 500 JSON response.
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				glog.Errorf("panic serving %s %s: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack())
				(&region.APIError{
					Status:  http.StatusInternalServerError,
					Code:    "internal_error",
					Message: "Internal gateway error.",
				}).Write(w)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
