package proxy

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/config"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/middleware"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/region"
)

// echo is a fake regional apiserver: it reports what it received.
type echo struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	RawQuery    string `json:"raw_query"`
	Body        string `json:"body"`
	ContentType string `json:"content_type"`
	XRegion     string `json:"x_region"`
	Auth        string `json:"authorization"`
	ReqLength   int64  `json:"req_length"`
}

func echoHandler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(echo{
		Method:      r.Method,
		Path:        r.URL.Path,
		RawQuery:    r.URL.RawQuery,
		Body:        string(body),
		ContentType: r.Header.Get("Content-Type"),
		XRegion:     r.Header.Get("X-Region"),
		Auth:        r.Header.Get("Authorization"),
		ReqLength:   r.ContentLength,
	})
}

func newTestDispatcher(t *testing.T, regions map[string]config.RegionConfig) (*Dispatcher, *config.Store) {
	t.Helper()
	require.NoError(t, config.ValidateRegions(regions))
	d := NewDispatcher()
	require.NoError(t, d.Reload(regions))
	return d, config.NewStore(regions)
}

// serve starts a gateway httptest server for the given dispatcher+store.
func serve(t *testing.T, d *Dispatcher, store *config.Store) *httptest.Server {
	t.Helper()
	handler := middleware.Region(store, 1<<20, false)(d)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, url string, build func(*http.Request), out interface{}) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	if build != nil {
		build(req)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil {
		require.NoError(t, json.Unmarshal(raw, out), "body: %s", raw)
	}
	return resp
}

func TestHeaderRegionForwarded(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer upstream.Close()
	d, store := newTestDispatcher(t, map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: upstream.URL},
	})
	gw := serve(t, d, store)

	var got echo
	resp := do(t, gw.URL+"/apis/v2beta1/pipelines?page_size=5", func(r *http.Request) {
		r.Header.Set("X-Region", "vn-north-1")
		r.Header.Set("Authorization", "Bearer tok")
	}, &got)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	// Query stripped, everything else forwarded.
	assert.Equal(t, "page_size=5", got.RawQuery)
	assert.Equal(t, "vn-north-1", got.XRegion) // X-Region intentionally forwarded
	assert.Equal(t, "Bearer tok", got.Auth)    // auth headers pass through
	assert.Equal(t, "/apis/v2beta1/pipelines", got.Path)
}

func TestQueryRegionStripped(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer upstream.Close()
	d, store := newTestDispatcher(t, map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: upstream.URL},
	})
	gw := serve(t, d, store)

	var got echo
	do(t, gw.URL+"/apis/v2beta1/pipelines?region=vn-north-1&page_size=5", nil, &got)
	assert.Equal(t, "page_size=5", got.RawQuery)
	assert.Empty(t, got.XRegion)
}

func TestJSONBodyRegionStripped(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer upstream.Close()
	d, store := newTestDispatcher(t, map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: upstream.URL},
	})
	gw := serve(t, d, store)

	var got echo
	do(t, gw.URL+"/apis/v2beta1/experiments", func(r *http.Request) {
		r.Method = http.MethodPost
		r.Header.Set("Content-Type", "application/json")
		r.Body = io.NopCloser(strings.NewReader(
			`{"display_name":"d","region":"vn-north-1","metadata":{"region":"keepme"}}`))
	}, &got)

	// The upstream must see no top-level region (it rejects unknown fields)
	// but the nested one survives untouched.
	assert.NotContains(t, got.Body, `"region":"vn-north-1"`)
	assert.Contains(t, got.Body, `"region":"keepme"`)
	assert.Contains(t, got.Body, `"display_name":"d"`)
	// Content-Length was recomputed to match the stripped body.
	assert.Equal(t, int64(len(got.Body)), got.ReqLength)
}

func TestStrictErrors(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer upstream.Close()
	d, store := newTestDispatcher(t, map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: upstream.URL},
	})
	gw := serve(t, d, store)

	t.Run("missing", func(t *testing.T) {
		var got struct {
			Error        string   `json:"error"`
			ValidRegions []string `json:"valid_regions"`
		}
		resp := do(t, gw.URL+"/apis/v2beta1/pipelines", nil, &got)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		assert.Equal(t, "region_required", got.Error)
		assert.Equal(t, []string{"vn-north-1"}, got.ValidRegions)
	})
	t.Run("unknown", func(t *testing.T) {
		var got struct {
			Error        string   `json:"error"`
			ValidRegions []string `json:"valid_regions"`
		}
		resp := do(t, gw.URL+"/apis/v2beta1/pipelines", func(r *http.Request) {
			r.Header.Set("X-Region", "vn-south-9")
		}, &got)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		assert.Equal(t, "unknown_region", got.Error)
		assert.Equal(t, []string{"vn-north-1"}, got.ValidRegions)
	})
	t.Run("multipart-without-region", func(t *testing.T) {
		var got struct {
			Error string `json:"error"`
		}
		resp := do(t, gw.URL+"/apis/v2beta1/pipelines/upload", func(r *http.Request) {
			r.Method = http.MethodPost
			r.Header.Set("Content-Type", "multipart/form-data; boundary=b")
			r.Body = io.NopCloser(strings.NewReader("--b--\r\n"))
		}, &got)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		assert.Equal(t, "region_required_for_multipart", got.Error)
	})
}

func TestMultipartPassthrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseMultipartForm(32<<20))
		file, _, err := r.FormFile("uploadfile")
		require.NoError(t, err)
		defer file.Close()
		content, _ := io.ReadAll(file)
		_, _ = w.Write(content)
	}))
	defer upstream.Close()
	d, store := newTestDispatcher(t, map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: upstream.URL},
	})
	gw := serve(t, d, store)

	payload := "name: my-pipeline"
	body := "--BND\r\nContent-Disposition: form-data; name=\"uploadfile\"; filename=\"p.yaml\"\r\n" +
		"Content-Type: application/octet-stream\r\n\r\n" + payload + "\r\n--BND--\r\n"

	req, err := http.NewRequest(http.MethodPost,
		gw.URL+"/apis/v2beta1/pipelines/upload?region=vn-north-1&name=gw",
		strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=BND")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, payload, string(respBody))
}

func TestUpstreamUnavailable502(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(echoHandler))
	deadURL := dead.URL
	dead.Close() // nothing listens here anymore

	d, store := newTestDispatcher(t, map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: deadURL},
	})
	gw := serve(t, d, store)

	var got struct {
		Error  string `json:"error"`
		Region string `json:"region"`
	}
	resp := do(t, gw.URL+"/apis/v2beta1/runs", func(r *http.Request) {
		r.Header.Set("X-Region", "vn-north-1")
	}, &got)
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	assert.Equal(t, "upstream_unavailable", got.Error)
	assert.Equal(t, "vn-north-1", got.Region)
}

func TestStreamingPassthrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "chunk-%d\n", i)
			flusher.Flush()
			time.Sleep(80 * time.Millisecond)
		}
	}))
	defer upstream.Close()
	d, store := newTestDispatcher(t, map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: upstream.URL},
	})
	gw := serve(t, d, store)

	resp, err := http.Get(gw.URL + "/apis/v1alpha1/runs/r/nodes/n/log?region=vn-north-1")
	require.NoError(t, err)
	defer resp.Body.Close()

	// First chunk must arrive while the upstream is still sending (the
	// gateway must not buffer the whole stream).
	reader := make(chan string, 3)
	go func() {
		buf := make([]byte, 8)
		for i := 0; i < 3; i++ {
			n, _ := io.ReadFull(resp.Body, buf)
			reader <- string(buf[:n])
		}
	}()
	select {
	case chunk := <-reader:
		assert.Equal(t, "chunk-0\n", chunk)
	case <-time.After(1 * time.Second):
		t.Fatal("no first chunk within 1s — stream was buffered")
	}
}

func TestGzipPassthrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		_, _ = gz.Write([]byte("gzipped-content"))
		_ = gz.Close()
	}))
	defer upstream.Close()
	d, store := newTestDispatcher(t, map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: upstream.URL},
	})
	gw := serve(t, d, store)

	req, err := http.NewRequest(http.MethodGet, gw.URL+"/apis/v2beta1/pipelines", nil)
	require.NoError(t, err)
	req.Header.Set("X-Region", "vn-north-1")
	req.Header.Set("Accept-Encoding", "gzip")
	// Transport must not auto-decompress: use a bare client transport.
	resp, err := (&http.Client{Transport: &http.Transport{DisableCompression: false}}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	assert.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))
	gr, err := gzip.NewReader(strings.NewReader(string(raw)))
	require.NoError(t, err)
	content, _ := io.ReadAll(gr)
	assert.Equal(t, "gzipped-content", string(content))
}

func TestHotReload(t *testing.T) {
	var mu sync.Mutex
	servedBy := ""
	up1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		servedBy = "one"
		mu.Unlock()
	}))
	defer up1.Close()
	up2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		servedBy = "two"
		mu.Unlock()
	}))
	defer up2.Close()

	d, store := newTestDispatcher(t, map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: up1.URL},
	})
	gw := serve(t, d, store)

	do(t, gw.URL+"/apis/v2beta1/healthz", func(r *http.Request) { r.Header.Set("X-Region", "vn-north-1") }, nil)
	mu.Lock()
	assert.Equal(t, "one", servedBy)
	mu.Unlock()

	require.NoError(t, d.Reload(map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: up2.URL},
	}))
	store.Swap(map[string]config.RegionConfig{"vn-north-1": {Endpoint: up2.URL}})

	do(t, gw.URL+"/apis/v2beta1/healthz", func(r *http.Request) { r.Header.Set("X-Region", "vn-north-1") }, nil)
	mu.Lock()
	assert.Equal(t, "two", servedBy)
	mu.Unlock()
}

func TestReloadInvalidKeepsOld(t *testing.T) {
	up1 := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer up1.Close()
	d := NewDispatcher()
	require.NoError(t, d.Reload(map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: up1.URL},
	}))
	err := d.Reload(map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: "::not-a-url"},
	})
	require.Error(t, err)
	tgt, ok := d.Target("vn-north-1")
	require.True(t, ok)
	assert.Equal(t, up1.URL, tgt.Config.Endpoint)
}

func TestRegionRemovedMidReload400(t *testing.T) {
	up1 := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer up1.Close()
	d, store := newTestDispatcher(t, map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: up1.URL},
	})
	gw := serve(t, d, store)

	// Simulate the middleware validating against the old store, then the
	// dispatcher having already swapped (hot-reload race).
	require.NoError(t, d.Reload(map[string]config.RegionConfig{
		"vn-south-2": {Endpoint: up1.URL},
	}))
	store.Swap(map[string]config.RegionConfig{"vn-south-2": {Endpoint: up1.URL}})

	// Old context region no longer exists in the table.
	req, _ := http.NewRequest(http.MethodGet, gw.URL+"/apis/v2beta1/pipelines", nil)
	req = req.WithContext(region.IntoContext(req.Context(), &region.Result{Name: "vn-north-1", Source: region.SourceHeader}))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHealthChecker(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/apis/v2beta1/healthz", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()

	d, _ := newTestDispatcher(t, map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: up.URL},
	})
	h := NewHealthChecker(d, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx)

	deadline := time.After(2 * time.Second)
	for {
		snap := h.Snapshot()
		if st, ok := snap["vn-north-1"]; ok && st.Healthy != nil && *st.Healthy {
			break
		}
		select {
		case <-deadline:
			t.Fatal("region never became healthy")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestHealthCheckerDown(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(echoHandler))
	deadURL := dead.URL
	dead.Close()

	d, _ := newTestDispatcher(t, map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: deadURL},
	})
	h := NewHealthChecker(d, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx)

	deadline := time.After(2 * time.Second)
	for {
		snap := h.Snapshot()
		if st, ok := snap["vn-north-1"]; ok && st.Healthy != nil && !*st.Healthy && st.LastError != "" {
			return
		}
		select {
		case <-deadline:
			t.Fatal("region never reported unhealthy")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
