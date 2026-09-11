package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/config"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/proxy"
)

func newDispatcher(t *testing.T) (*proxy.Dispatcher, *proxy.HealthChecker) {
	t.Helper()
	regions := map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: "http://10.24.10.26:31132"},
	}
	d := proxy.NewDispatcher()
	require.NoError(t, d.Reload(regions))
	return d, proxy.NewHealthChecker(d, 0) // probing disabled
}

func TestHealthz(t *testing.T) {
	d, _ := newDispatcher(t)
	srv := httptest.NewServer(Healthz("test-version", func() int { return len(d.Targets()) }))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got struct {
		Status            string `json:"status"`
		Version           string `json:"version"`
		RegionsConfigured int    `json:"regions_configured"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, "ok", got.Status)
	assert.Equal(t, "test-version", got.Version)
	assert.Equal(t, 1, got.RegionsConfigured)
}

func TestRegions(t *testing.T) {
	d, h := newDispatcher(t)
	srv := httptest.NewServer(Regions(d, h))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	var got struct {
		Regions []struct {
			Name      string     `json:"name"`
			Endpoint  string     `json:"endpoint"`
			Healthy   *bool      `json:"healthy"`
			LastCheck *time.Time `json:"last_check"`
		} `json:"regions"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Len(t, got.Regions, 1)
	assert.Equal(t, "vn-north-1", got.Regions[0].Name)
	assert.Equal(t, "http://10.24.10.26:31132", got.Regions[0].Endpoint)
	// healthy is null until the first probe completes.
	assert.Nil(t, got.Regions[0].Healthy)
	assert.Nil(t, got.Regions[0].LastCheck)
}
