package middleware

import (
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

// passthrough echoes the resolved region and the received request so tests
// can assert the full middleware behavior end to end.
func passthrough(w http.ResponseWriter, r *http.Request) {
	res, ok := region.FromContext(r.Context())
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_, _ = w.Write([]byte(`{"region":"` + res.Name + `","source":"` + string(res.Source) + `","query":"` +
		r.URL.RawQuery + `","source_value":"` + string(res.Source) + `","body":` + strconvQuote(string(body)) + `}`))
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func chain(store *config.Store, max int64) http.Handler {
	return Recovery(Region(store, max)(http.HandlerFunc(passthrough)))
}

func TestRegionMiddlewareFullChain(t *testing.T) {
	store := config.NewStore(map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: "http://10.24.10.26:31132"},
	})

	t.Run("ok", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/apis/v2beta1/pipelines?region=vn-north-1&page_size=5", nil)
		w := httptest.NewRecorder()
		chain(store, 1<<20).ServeHTTP(w, r)
		require.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"region":"vn-north-1"`)
		assert.Contains(t, w.Body.String(), `"source":"query"`)
		assert.Contains(t, w.Body.String(), `"query":"page_size=5"`)
	})

	t.Run("error-json", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/apis/v2beta1/pipelines", nil)
		w := httptest.NewRecorder()
		chain(store, 1<<20).ServeHTTP(w, r)
		require.Equal(t, http.StatusBadRequest, w.Code)
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
		assert.Contains(t, w.Body.String(), "region_required")
	})

	t.Run("panic-recovered", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/apis/v2beta1/pipelines", nil)
		r.Header.Set(region.HeaderName, "vn-north-1")
		w := httptest.NewRecorder()
		Recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("boom")
		})).ServeHTTP(w, r)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		assert.Contains(t, w.Body.String(), "internal_error")
	})

	t.Run("body-region-flow", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/apis/v2beta1/experiments",
			strings.NewReader(`{"name":"n","region":"vn-north-1"}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		chain(store, 1<<20).ServeHTTP(w, r)
		require.Equal(t, http.StatusOK, w.Code)

		var got struct {
			Region string `json:"region"`
			Source string `json:"source"`
			Body   string `json:"body"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		assert.Equal(t, "vn-north-1", got.Region)
		assert.Equal(t, "body", got.Source)
		// The handler saw the stripped body.
		assert.JSONEq(t, `{"name":"n"}`, got.Body)
	})
}

func TestStatusWriterDefaults(t *testing.T) {
	sw := &statusWriter{ResponseWriter: httptest.NewRecorder()}
	_, _ = sw.Write([]byte("x"))
	assert.Equal(t, http.StatusOK, sw.status)
}
