package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/config"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/region"
)

// stubNSResolver implements namespace.Store for testing.
type stubNSResolver struct {
	// map[projectID+"|"+region]namespace; absent key → not provisioned.
	entries map[string]string
}

func (s *stubNSResolver) Resolve(_ context.Context, projectID, regionName string) (string, bool, bool) {
	ns, ok := s.entries[projectID+"|"+regionName]
	return ns, ok, false
}

// listUpstream serves a minimal KFP-style list response.
func listUpstream(t *testing.T, key string, items []map[string]interface{}, totalSize int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		type resp struct {
			Items     []map[string]interface{} `json:"-"` // handled below
			TotalSize int                      `json:"total_size"`
		}
		obj := map[string]interface{}{
			key:          items,
			"total_size": totalSize,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(obj)
	}))
}

// buildFanoutGateway wires a Fanout middleware in front of a stub dispatcher.
func buildFanoutGateway(
	t *testing.T,
	regions map[string]config.RegionConfig,
	nsResolver *stubNSResolver,
) *httptest.Server {
	t.Helper()
	d, store := newTestDispatcher(t, regions)
	// The inner handler is the regular single-region chain (Region middleware).
	inner := middleware_Region(store)(d)
	handler := FanoutMiddleware(d, nsResolver, false)(inner)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// middleware_Region wraps region.Process middleware for test wiring.
// We borrow the region middleware already tested in proxy_test.go.
func middleware_Region(store *config.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			res, apiErr := region.Process(r, store, 1<<20)
			if apiErr != nil {
				apiErr.Write(w)
				return
			}
			r = r.WithContext(region.IntoContext(r.Context(), res))
			next.ServeHTTP(w, r)
		})
	}
}

func TestFanoutNotTriggeredForNormalRegion(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"runs": []interface{}{}, "total_size": 0})
	}))
	defer up.Close()

	gw := buildFanoutGateway(t, map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: up.URL},
	}, nil)

	// Normal request with explicit region — must not trigger fan-out.
	req, _ := http.NewRequest(http.MethodGet, gw.URL+"/apis/v2beta1/runs", nil)
	req.Header.Set("X-Region", "vn-north-1")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestFanoutMethodNotAllowed(t *testing.T) {
	d := NewDispatcher()
	_ = d.Reload(map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: "http://127.0.0.1:1"},
	})
	handler := FanoutMiddleware(d, nil, false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/apis/v2beta1/runs", strings.NewReader("{}"))
	req.Header.Set("X-Region", "all")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

func TestFanoutMergesTwoRegions(t *testing.T) {
	up1 := listUpstream(t, "runs", []map[string]interface{}{
		{"run_id": "r1", "display_name": "run-one"},
	}, 1)
	defer up1.Close()
	up2 := listUpstream(t, "runs", []map[string]interface{}{
		{"run_id": "r2", "display_name": "run-two"},
	}, 1)
	defer up2.Close()

	d, _ := newTestDispatcher(t, map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: up1.URL},
		"vn-south-2": {Endpoint: up2.URL},
	})
	handler := FanoutMiddleware(d, nil, false)(d) // inner doesn't matter for "all"
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/apis/v2beta1/runs", nil)
	req.Header.Set("X-Region", "all")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Runs []struct {
			RunID  string `json:"run_id"`
			Region string `json:"region"`
		} `json:"runs"`
		TotalSize int `json:"total_size"`
	}
	raw, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(raw, &body), "body: %s", raw)

	assert.Equal(t, 2, body.TotalSize)
	assert.Len(t, body.Runs, 2)
	// Every item must carry a non-empty region.
	for _, item := range body.Runs {
		assert.NotEmpty(t, item.Region)
	}
	// Both region values must appear.
	regions := map[string]bool{}
	for _, item := range body.Runs {
		regions[item.Region] = true
	}
	assert.True(t, regions["vn-north-1"] || regions["vn-south-2"])
	assert.Len(t, regions, 2)
}

func TestFanoutQueryParamAll(t *testing.T) {
	up := listUpstream(t, "pipelines", nil, 0)
	defer up.Close()
	d, _ := newTestDispatcher(t, map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: up.URL},
	})
	handler := FanoutMiddleware(d, nil, false)(d)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// ?region=all as query param.
	resp, err := http.Get(srv.URL + "/apis/v2beta1/pipelines?region=all")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestFanoutNamespaceInjection(t *testing.T) {
	// The upstream records which namespace it received.
	var gotNamespace string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotNamespace = r.URL.Query().Get("namespace")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"runs": []interface{}{}, "total_size": 0})
	}))
	defer up.Close()

	d, _ := newTestDispatcher(t, map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: up.URL},
	})
	ns := &stubNSResolver{entries: map[string]string{
		"proj-1|vn-north-1": "ns-proj-1",
	}}
	handler := FanoutMiddleware(d, ns, false)(d)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/apis/v2beta1/runs", nil)
	req.Header.Set("X-Region", "all")
	req.Header.Set("Project-ID", "proj-1")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "ns-proj-1", gotNamespace)
}

func TestFanoutSkipsUnprovisionedRegion(t *testing.T) {
	up1 := listUpstream(t, "runs", []map[string]interface{}{{"run_id": "r1"}}, 1)
	defer up1.Close()
	up2 := listUpstream(t, "runs", []map[string]interface{}{{"run_id": "r2"}}, 1)
	defer up2.Close()

	d, _ := newTestDispatcher(t, map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: up1.URL},
		"vn-south-2": {Endpoint: up2.URL},
	})
	// Project only provisioned in vn-north-1.
	ns := &stubNSResolver{entries: map[string]string{
		"proj-1|vn-north-1": "ns-proj-1",
	}}
	handler := FanoutMiddleware(d, ns, false)(d)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/apis/v2beta1/runs", nil)
	req.Header.Set("X-Region", "all")
	req.Header.Set("Project-ID", "proj-1")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var body struct {
		Runs      []map[string]interface{} `json:"runs"`
		TotalSize int                      `json:"total_size"`
	}
	raw, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(raw, &body), "body: %s", raw)

	// Only the vn-north-1 run should appear.
	assert.Equal(t, 1, body.TotalSize)
	assert.Len(t, body.Runs, 1)
}

func TestRegionInjectedInSingleRegionResponse(t *testing.T) {
	// Single-region requests via the ReverseProxy must get "region" injected.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"run_id": "abc",
			"state":  "RUNNING",
		})
	}))
	defer up.Close()

	d, store := newTestDispatcher(t, map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: up.URL},
	})
	srv := serve(t, d, store)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/apis/v2beta1/runs/abc", nil)
	req.Header.Set("X-Region", "vn-north-1")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var body map[string]interface{}
	raw, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(raw, &body), "body: %s", raw)
	assert.Equal(t, "vn-north-1", body["region"])
}

func TestRegionNotInjectedForNonJSON(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("log line 1\nlog line 2\n"))
	}))
	defer up.Close()

	d, store := newTestDispatcher(t, map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: up.URL},
	})
	srv := serve(t, d, store)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/apis/v1alpha1/runs/r/log", nil)
	req.Header.Set("X-Region", "vn-north-1")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "log line 1\nlog line 2\n", string(body))
}
