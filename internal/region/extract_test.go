package region

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/config"
)

func testStore() *config.Store {
	return config.NewStore(map[string]config.RegionConfig{
		"vn-north-1": {Endpoint: "http://10.24.10.26:31132"},
		"vn-south-2": {Endpoint: "https://api.vn-south-2.example.com"},
	})
}

func run(t *testing.T, r *http.Request, maxBody int64) (*Result, *APIError) {
	t.Helper()
	return Process(r, testStore(), maxBody)
}

func jsonReq(t *testing.T, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/apis/v2beta1/experiments", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestProcessHeader(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/apis/v2beta1/pipelines?page_size=5", nil)
	r.Header.Set(HeaderName, "vn-north-1")
	res, apiErr := run(t, r, 1<<20)
	require.Nil(t, apiErr)
	assert.Equal(t, "vn-north-1", res.Name)
	assert.Equal(t, SourceHeader, res.Source)
	// Header wins; a stale region query pair is still stripped.
	assert.Equal(t, "page_size=5", r.URL.RawQuery)
}

func TestProcessHeaderWhitespaceFallsThroughToQuery(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/apis/v2beta1/pipelines?region=vn-south-2", nil)
	r.Header.Set(HeaderName, "   ")
	res, apiErr := run(t, r, 1<<20)
	require.Nil(t, apiErr)
	assert.Equal(t, "vn-south-2", res.Name)
	assert.Equal(t, SourceQuery, res.Source)
	assert.Empty(t, r.URL.RawQuery)
}

func TestProcessQuery(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/apis/v2beta1/pipelines?page_size=5&region=vn-north-1&filter=x", nil)
	res, apiErr := run(t, r, 1<<20)
	require.Nil(t, apiErr)
	assert.Equal(t, SourceQuery, res.Source)
	// Order and encoding of the other params preserved; region gone.
	assert.Equal(t, "page_size=5&filter=x", r.URL.RawQuery)
}

func TestProcessBodyRegionStripped(t *testing.T) {
	r := jsonReq(t, `{"display_name":"d","region":"vn-north-1","description":"x"}`)
	res, apiErr := run(t, r, 1<<20)
	require.Nil(t, apiErr)
	assert.Equal(t, "vn-north-1", res.Name)
	assert.Equal(t, SourceBody, res.Source)

	body, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	assert.JSONEq(t, `{"display_name":"d","description":"x"}`, string(body))
	assert.NotContains(t, string(body), `"region"`)
	assert.Equal(t, int64(len(body)), r.ContentLength)
	assert.Equal(t, strconv.Itoa(len(body)), r.Header.Get("Content-Length"))
}

func TestProcessBodyNestedRegionPreserved(t *testing.T) {
	r := jsonReq(t, `{"metadata":{"region":"keepme"},"region":"vn-north-1"}`)
	res, apiErr := run(t, r, 1<<20)
	require.Nil(t, apiErr)
	require.Equal(t, "vn-north-1", res.Name)

	body, _ := io.ReadAll(r.Body)
	assert.Contains(t, string(body), `"region":"keepme"`)
}

func TestProcessMissingRegion(t *testing.T) {
	cases := []struct {
		name string
		req  *http.Request
	}{
		{"get-no-body", httptest.NewRequest(http.MethodGet, "/apis/v2beta1/pipelines", nil)},
		{"empty-header", func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/apis/v2beta1/pipelines", nil)
			r.Header.Set(HeaderName, "")
			return r
		}()},
		{"json-object-without-region", jsonReq(t, `{"display_name":"d"}`)},
		{"json-array-body", jsonReq(t, `[1,2,3]`)},
		{"malformed-json", jsonReq(t, `{not json`)},
		{"empty-region-in-body", jsonReq(t, `{"region":"  "}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, apiErr := run(t, tc.req, 1<<20)
			require.Nil(t, res)
			require.NotNil(t, apiErr)
			assert.Equal(t, http.StatusBadRequest, apiErr.Status)
			assert.Equal(t, "region_required", apiErr.Code)
			assert.ElementsMatch(t, []string{"vn-north-1", "vn-south-2"}, apiErr.ValidRegions)
			assert.NotEmpty(t, apiErr.Hint)
		})
	}
}

func TestProcessUnknownRegion(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/apis/v2beta1/pipelines", nil)
	r.Header.Set(HeaderName, "vn-south-9")
	res, apiErr := run(t, r, 1<<20)
	require.Nil(t, res)
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.Status)
	assert.Equal(t, "unknown_region", apiErr.Code)
	assert.Contains(t, apiErr.Message, "vn-south-9")
	assert.ElementsMatch(t, []string{"vn-north-1", "vn-south-2"}, apiErr.ValidRegions)
}

func TestProcessBodyNonStringRegion(t *testing.T) {
	r := jsonReq(t, `{"region":123}`)
	res, apiErr := run(t, r, 1<<20)
	require.Nil(t, res)
	require.NotNil(t, apiErr)
	assert.Equal(t, "invalid_region_field", apiErr.Code)
	assert.Equal(t, http.StatusBadRequest, apiErr.Status)
}

func TestProcessMultipart(t *testing.T) {
	t.Run("with-header-passes", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/apis/v2beta1/pipelines/upload", strings.NewReader("--b\r\n"))
		r.Header.Set("Content-Type", "multipart/form-data; boundary=b")
		r.Header.Set(HeaderName, "vn-north-1")
		res, apiErr := run(t, r, 1<<20)
		require.Nil(t, apiErr)
		assert.Equal(t, "vn-north-1", res.Name)
	})
	t.Run("without-header-rejected", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/apis/v2beta1/pipelines/upload", strings.NewReader("--b\r\n"))
		r.Header.Set("Content-Type", "multipart/form-data; boundary=b")
		res, apiErr := run(t, r, 1<<20)
		require.Nil(t, res)
		require.NotNil(t, apiErr)
		assert.Equal(t, "region_required_for_multipart", apiErr.Code)
	})
	t.Run("with-query-passes", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/apis/v2beta1/pipelines/upload?region=vn-north-1&name=p", strings.NewReader("--b\r\n"))
		r.Header.Set("Content-Type", "multipart/form-data; boundary=b")
		res, apiErr := run(t, r, 1<<20)
		require.Nil(t, apiErr)
		assert.Equal(t, "vn-north-1", res.Name)
		assert.Equal(t, "name=p", r.URL.RawQuery)
	})
}

func TestProcessPayloadTooLarge(t *testing.T) {
	big := `{"display_name":"` + strings.Repeat("x", 64) + `","region":"vn-north-1"}`
	r := jsonReq(t, big)
	res, apiErr := run(t, r, 32)
	require.Nil(t, res)
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusRequestEntityTooLarge, apiErr.Status)
	assert.Equal(t, "payload_too_large", apiErr.Code)
}

func TestProcessPrecedenceHeaderBeatsQueryBeatsBody(t *testing.T) {
	r := jsonReq(t, `{"region":"vn-south-2"}`)
	r.URL.RawQuery = "region=vn-north-1"
	r.Header.Set(HeaderName, "vn-north-1")
	res, apiErr := run(t, r, 1<<20)
	require.Nil(t, apiErr)
	assert.Equal(t, "vn-north-1", res.Name)
	assert.Equal(t, SourceHeader, res.Source)
}

func TestAPIErrorWrite(t *testing.T) {
	w := httptest.NewRecorder()
	UnknownRegion("nope", []string{"vn-north-1"}).Write(w)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Contains(t, w.Body.String(), `"error":"unknown_region"`)
	assert.Contains(t, w.Body.String(), `"valid_regions":["vn-north-1"]`)
}

func TestHasBodyChunked(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader([]byte(`{}`)))
	r.Header.Set("Content-Type", "application/json")
	r.ContentLength = -1 // chunked
	assert.True(t, hasBody(r))
}
