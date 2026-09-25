package middleware

import (
	"net/http"
	"strconv"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/region"
)

// PageSize validates the page_size query parameter on GET requests: when
// present it must be a positive integer. KFP treats 0 (and negatives) as
// "no pagination" and returns the entire collection — a footgun for large
// projects that the gateway rejects up front instead of proxying.
func PageSize() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				if v := r.URL.Query().Get("page_size"); v != "" {
					if n, err := strconv.Atoi(v); err != nil || n < 1 {
						(&region.APIError{
							Status: http.StatusBadRequest,
							Code:   "invalid_page_size",
							Message: "The page_size parameter must be a positive integer when provided " +
								"(omit it to use the server default).",
						}).Write(w)
						return
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
