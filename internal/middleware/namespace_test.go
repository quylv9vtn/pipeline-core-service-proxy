package middleware

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/namespace"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/region"
)

type fakeResolver struct {
	ns    string
	ok    bool
	dbErr bool
}

func (f fakeResolver) Resolve(_ context.Context, _, _ string) (string, bool, bool) {
	return f.ns, f.ok, f.dbErr
}

const testProjectID = "0b9e6c7a-0000-4000-8000-000000000001"

func nsRequest(method, path, body string) *http.Request {
	var rd io.Reader
	if body != "" {
		rd = stringReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req.WithContext(region.IntoContext(req.Context(), &region.Result{Name: "vn-north-1"}))
}

type stringRd struct{ s string }

func (r *stringRd) Read(p []byte) (int, error) {
	if len(r.s) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.s)
	r.s = r.s[n:]
	return n, nil
}

func stringReader(s string) io.Reader { return &stringRd{s} }

// captured records what the downstream handler received.
type captured struct {
	req      *http.Request
	body     []byte
	rawQuery string
}

func runNamespace(t *testing.T, resolver namespace.Store, req *http.Request, upstream func(w http.ResponseWriter)) (*httptest.ResponseRecorder, *captured) {
	t.Helper()
	cap := &captured{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.req = r
		cap.rawQuery = r.URL.RawQuery
		if r.Body != nil {
			cap.body, _ = io.ReadAll(r.Body)
		}
		if upstream != nil {
			upstream(w)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	Namespace(resolver, 1<<20)(next).ServeHTTP(rec, req)
	return rec, cap
}

func TestNamespaceMiddleware(t *testing.T) {
	ready := fakeResolver{ns: "proj-pipeline", ok: true}

	t.Run("non-scoped endpoint passes through untouched", func(t *testing.T) {
		req := nsRequest(http.MethodGet, "/apis/v2beta1/tasks", "")
		rec, cap := runNamespace(t, fakeResolver{}, req, nil)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.NotNil(t, cap.req)
	})

	t.Run("missing Project-ID rejected", func(t *testing.T) {
		req := nsRequest(http.MethodGet, "/apis/v2beta1/runs", "")
		rec, _ := runNamespace(t, ready, req, nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "project_id_required")
	})

	t.Run("invalid Project-ID rejected", func(t *testing.T) {
		req := nsRequest(http.MethodGet, "/apis/v2beta1/runs", "")
		req.Header.Set(HeaderProjectID, "not-a-uuid")
		rec, _ := runNamespace(t, ready, req, nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "invalid_project_id")
	})

	t.Run("mismatch with enforcer assertion", func(t *testing.T) {
		req := nsRequest(http.MethodGet, "/apis/v2beta1/runs", "")
		req.Header.Set(HeaderProjectID, testProjectID)
		req.Header.Set(HeaderCMPProjectID, "0b9e6c7a-0000-4000-8000-000000000002")
		rec, _ := runNamespace(t, ready, req, nil)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Contains(t, rec.Body.String(), "project_mismatch")
	})

	t.Run("not provisioned", func(t *testing.T) {
		req := nsRequest(http.MethodGet, "/apis/v2beta1/runs", "")
		req.Header.Set(HeaderProjectID, testProjectID)
		rec, _ := runNamespace(t, fakeResolver{ok: false}, req, nil)
		assert.Equal(t, http.StatusPreconditionFailed, rec.Code)
		assert.Contains(t, rec.Body.String(), "project_not_provisioned")
	})

	t.Run("db error surfaces 500", func(t *testing.T) {
		req := nsRequest(http.MethodGet, "/apis/v2beta1/runs", "")
		req.Header.Set(HeaderProjectID, testProjectID)
		rec, _ := runNamespace(t, fakeResolver{dbErr: true}, req, nil)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.Contains(t, rec.Body.String(), "namespace_lookup_failed")
	})

	t.Run("query injection on list", func(t *testing.T) {
		req := nsRequest(http.MethodGet, "/apis/v2beta1/runs?pageSize=10", "")
		req.Header.Set(HeaderProjectID, testProjectID)
		rec, cap := runNamespace(t, ready, req, nil)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "pageSize=10&namespace=proj-pipeline", cap.rawQuery)
		assert.Empty(t, cap.req.Header.Get(HeaderProjectID), "Project-ID must be stripped")
	})

	t.Run("JSON body injection overwrites client namespace", func(t *testing.T) {
		req := nsRequest(http.MethodPost, "/apis/v2beta1/experiments",
			`{"name":"e1","namespace":"client-evil"}`)
		req.Header.Set(HeaderProjectID, testProjectID)
		rec, cap := runNamespace(t, ready, req, nil)
		assert.Equal(t, http.StatusOK, rec.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(cap.body, &body))
		assert.Equal(t, "proj-pipeline", body["namespace"])
		assert.Equal(t, "e1", body["name"])
		assert.Equal(t, strconv.Itoa(len(cap.body)), cap.req.Header.Get("Content-Length"))
	})

	t.Run("run create body untouched", func(t *testing.T) {
		body := `{"experiment_id":"e1","name":"r1"}`
		req := nsRequest(http.MethodPost, "/apis/v2beta1/runs", body)
		req.Header.Set(HeaderProjectID, testProjectID)
		rec, cap := runNamespace(t, ready, req, nil)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, body, string(cap.body))
	})
}

func TestNamespaceResponseCheck(t *testing.T) {
	ready := fakeResolver{ns: "proj-pipeline", ok: true}

	t.Run("foreign namespace replaced with 404", func(t *testing.T) {
		req := nsRequest(http.MethodGet, "/apis/v2beta1/runs/run-1", "")
		req.Header.Set(HeaderProjectID, testProjectID)
		rec, _ := runNamespace(t, ready, req, func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"run_id":"run-1","namespace":"other-ns"}`))
		})
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Contains(t, rec.Body.String(), "not_found")
	})

	t.Run("own namespace passes through", func(t *testing.T) {
		req := nsRequest(http.MethodGet, "/apis/v2beta1/runs/run-1", "")
		req.Header.Set(HeaderProjectID, testProjectID)
		rec, _ := runNamespace(t, ready, req, func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"run_id":"run-1","namespace":"proj-pipeline"}`))
		})
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "run-1")
	})

	t.Run("no namespace field passes through", func(t *testing.T) {
		req := nsRequest(http.MethodGet, "/apis/v2beta1/pipelines/p1", "")
		req.Header.Set(HeaderProjectID, testProjectID)
		rec, _ := runNamespace(t, ready, req, func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"pipeline_id":"p1","name":"pipe"}`))
		})
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("error responses pass through unchecked", func(t *testing.T) {
		req := nsRequest(http.MethodGet, "/apis/v2beta1/runs/run-1", "")
		req.Header.Set(HeaderProjectID, testProjectID)
		rec, _ := runNamespace(t, ready, req, func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not_found","message":"gone"}`))
		})
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Contains(t, rec.Body.String(), "gone")
	})
}
