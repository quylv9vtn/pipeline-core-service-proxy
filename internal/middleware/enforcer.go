package middleware

import (
	"net/http"
	"strings"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/metrics"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/region"
)

// CMP assertion headers injected by the Enforcer Proxy (which authorizes
// the caller against CMP /internal/authz before forwarding).
const (
	HeaderCMPUserGID   = "X-CMP-User-GID"
	HeaderCMPProjectID = "X-CMP-Project-ID"
	// HeaderKubeflowUser is the audit identity forwarded to regional KFP.
	HeaderKubeflowUser = "Kubeflow-UserId"
)

// Enforcer rejects requests that lack the X-CMP-* assertion headers. The
// gateway does not re-authorize (that is the enforcer's job) — it only
// verifies the assertion is present, per the trust boundary decision
// (network policy ensures only the enforcer can reach the gateway).
// required=false short-circuits (dev mode without an enforcer in front).
func Enforcer(required bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !required {
				next.ServeHTTP(w, r)
				return
			}
			if strings.TrimSpace(r.Header.Get(HeaderCMPUserGID)) == "" ||
				strings.TrimSpace(r.Header.Get(HeaderCMPProjectID)) == "" {
				metrics.AuthRejects.WithLabelValues("enforcer_missing_assertion").Inc()
				(&region.APIError{
					Status:  http.StatusUnauthorized,
					Code:    "enforcer_assertion_required",
					Message: "Requests must carry X-CMP-User-GID and X-CMP-Project-ID assertion headers from the enforcer.",
				}).Write(w)
				return
			}
			// Forward the caller identity to the regional KFP for audit
			// (KFP multi-user mode stamps it on created resources).
			if r.Header.Get(HeaderKubeflowUser) == "" {
				r.Header.Set(HeaderKubeflowUser, strings.TrimSpace(r.Header.Get(HeaderCMPUserGID)))
			}
			next.ServeHTTP(w, r)
		})
	}
}