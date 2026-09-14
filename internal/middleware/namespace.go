package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/metrics"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/namespace"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/region"
)

// HeaderProjectID is the canonical project header on project-scoped
// requests. The enforcer forwards it; the gateway resolves it to a KFP
// namespace and strips it before proxying (upstream has no use for it).
const HeaderProjectID = "Project-ID"

// Namespace resolves Project-ID → namespace for project-scoped endpoints
// and injects it into the upstream request per the regional KFP contract.
// It must run after the Region middleware (needs the resolved region in
// context) and after Enforcer (cross-checks X-CMP-Project-ID).
//
// Rejects: 400 missing/invalid Project-ID, 403 assertion mismatch,
// 412 project_not_provisioned. On single-resource endpoints it verifies the
// response's namespace and replaces it with 404 on mismatch (no existence
// leak across projects).
func Namespace(resolver namespace.Store, maxJSONBodyBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			res, ok := region.FromContext(r.Context())
			if !ok {
				(&region.APIError{
					Status:  http.StatusInternalServerError,
					Code:    "internal_error",
					Message: "No region resolved for request.",
				}).Write(w)
				return
			}

			m := namespace.Classify(r.Method, r.URL.Path)
			if !m.Scoped {
				next.ServeHTTP(w, r)
				return
			}

			projectID := strings.TrimSpace(r.Header.Get(HeaderProjectID))
			if projectID == "" {
				namespace.ErrMissingProjectID().Write(w)
				return
			}
			if _, err := uuid.Parse(projectID); err != nil {
				namespace.ErrInvalidProjectID(projectID).Write(w)
				return
			}
			if asserted := strings.TrimSpace(r.Header.Get(HeaderCMPProjectID)); asserted != "" && asserted != projectID {
				metrics.AuthRejects.WithLabelValues("project_id_mismatch").Inc()
				namespace.ErrProjectMismatch(projectID, asserted).Write(w)
				return
			}

			start := time.Now()
			ns, ok, dbErr := resolver.Resolve(r.Context(), projectID, res.Name)
			metrics.NamespaceResolutionDuration.WithLabelValues(res.Name).Observe(time.Since(start).Seconds())
			if dbErr {
				metrics.NamespaceResolution.WithLabelValues(res.Name, "error").Inc()
				(&region.APIError{
					Status:  http.StatusInternalServerError,
					Code:    "namespace_lookup_failed",
					Message: "Failed to resolve the project namespace.",
				}).Write(w)
				return
			}
			if !ok {
				metrics.NamespaceResolution.WithLabelValues(res.Name, "miss").Inc()
				namespace.ErrNotProvisioned(projectID, res.Name).Write(w)
				return
			}
			metrics.NamespaceResolution.WithLabelValues(res.Name, "hit").Inc()

			switch m.Inject {
			case namespace.InjectQuery:
				r.URL.RawQuery = region.SetQueryParam(r.URL.RawQuery, "namespace", ns)
			case namespace.InjectJSONBody:
				if apiErr := injectJSONNamespace(r, ns, maxJSONBodyBytes); apiErr != nil {
					apiErr.Write(w)
					return
				}
			case namespace.InjectNone:
			}

			// Upstream never needs the project identity.
			r.Header.Del(HeaderProjectID)

			if !m.CheckResponse {
				next.ServeHTTP(w, r)
				return
			}
			cw := &checkWriter{ResponseWriter: w, namespace: ns, region: res.Name}
			next.ServeHTTP(cw, r)
			cw.finish()
		})
	}
}

// injectJSONNamespace buffers a JSON body and sets the top-level namespace
// field, overwriting any client-supplied value (the namespace is derived
// from the authorized project, never from the payload).
func injectJSONNamespace(r *http.Request, ns string, maxBytes int64) *region.APIError {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	if err != nil {
		return &region.APIError{
			Status:  http.StatusBadRequest,
			Code:    "invalid_request_body",
			Message: "Failed to read request body: " + err.Error(),
		}
	}
	if int64(len(body)) > maxBytes {
		return &region.APIError{
			Status:  http.StatusRequestEntityTooLarge,
			Code:    "payload_too_large",
			Message: "Request body exceeds the gateway JSON buffer limit.",
		}
	}
	region.ReplaceBody(r, region.SetTopLevelJSONKey(body, "namespace", ns))
	return nil
}

// checkWriter buffers the response of a single-resource request so the
// namespace can be verified before anything reaches the client.
type checkWriter struct {
	http.ResponseWriter
	status    int
	buf       bytes.Buffer
	header    http.Header
	namespace string
	region    string
	flushed   bool
}

func (c *checkWriter) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
		c.header = c.Header().Clone()
	}
}

func (c *checkWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.WriteHeader(http.StatusOK)
	}
	if c.flushed {
		return c.ResponseWriter.Write(b)
	}
	return c.buf.Write(b)
}

func (c *checkWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		c.commit()
		f.Flush()
	}
}

// commit writes the buffered status, headers and body out verbatim.
func (c *checkWriter) commit() {
	if c.flushed {
		return
	}
	c.flushed = true
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if c.header != nil {
		for k, vv := range c.header {
			for _, v := range vv {
				c.Header().Add(k, v)
			}
		}
	}
	if c.Header().Get("Content-Length") != "" {
		c.Header().Set("Content-Length", strconv.Itoa(c.buf.Len()))
	}
	c.ResponseWriter.WriteHeader(c.status)
	if c.buf.Len() > 0 {
		_, _ = c.ResponseWriter.Write(c.buf.Bytes())
	}
}

// finish replays the buffered response, substituting 404 when the resource
// lives in another namespace. Resources without a namespace field pass
// through (best-effort enforcement).
func (c *checkWriter) finish() {
	if c.flushed {
		return
	}
	if c.status == http.StatusOK && isJSON(c.header) {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(c.buf.Bytes(), &obj); err == nil {
			if raw, has := obj["namespace"]; has {
				var resNS string
				if err := json.Unmarshal(raw, &resNS); err == nil && resNS != "" && resNS != c.namespace {
					// Cross-namespace access: hide the resource.
					c.Header().Del("Content-Length")
					namespace.ErrNamespaceBlocked(c.namespace, c.region).Write(c.ResponseWriter)
					return
				}
			}
		}
	}
	c.commit()
}

func isJSON(h http.Header) bool {
	mt, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	return err == nil && (mt == "application/json" || strings.HasSuffix(mt, "+json"))
}
