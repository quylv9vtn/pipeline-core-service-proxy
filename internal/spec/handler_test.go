package spec

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/region"
)

type okLookup struct{}

func (okLookup) Resolve(_ context.Context, _, _, _ string) (Resources, error) {
	return Resources{ImageURL: "registry/img:9", CPU: 2, MemoryGiB: 4}, nil
}

func buildUpload(t *testing.T, specYAML, steps string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	require.NoError(t, writer.WriteField("name", "my-pipe"))
	if steps != "" {
		require.NoError(t, writer.WriteField("steps", steps))
	}
	part, err := writer.CreateFormFile("uploadfile", "pipe.yaml")
	require.NoError(t, err)
	_, err = part.Write([]byte(specYAML))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/apis/v2beta1/pipelines/upload", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req.WithContext(region.IntoContext(req.Context(), &region.Result{Name: "vn-north-1"}))
}

func TestHandleUpload(t *testing.T) {
	var captured *http.Request
	var capturedBody []byte
	forward := func(w http.ResponseWriter, r *http.Request) {
		captured = r
		capturedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}
	up := &Uploader{Lookup: okLookup{}, Forward: forward}

	t.Run("rewrites spec and strips steps", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := buildUpload(t, sampleIR, `{
			"preprocess":{"flavor_id":"0b9e6c7a-0000-4000-8000-000000000001","image_id":"0b9e6c7a-0000-4000-8000-000000000002"},
			"train":{"flavor_id":"0b9e6c7a-0000-4000-8000-000000000003","image_id":"0b9e6c7a-0000-4000-8000-000000000004"}}`)
		up.HandleUpload(rec, req)

		require.NotNil(t, captured)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.NotContains(t, string(capturedBody), "steps")
		assert.Contains(t, string(capturedBody), "registry/img:9")
		assert.Contains(t, string(capturedBody), "resourceCpuRequest: \"2\"")
		assert.Contains(t, string(capturedBody), "name", "name field forwarded")
		assert.Equal(t, int64(len(capturedBody)), captured.ContentLength)
		assert.Contains(t, captured.Header.Get("Content-Type"), "multipart/form-data")
	})

	t.Run("bad spec rejected before forward", func(t *testing.T) {
		captured = nil
		rec := httptest.NewRecorder()
		req := buildUpload(t, "not: [valid: ir", "")
		up.HandleUpload(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "invalid_pipeline_spec")
		assert.Nil(t, captured, "must not reach upstream")
	})

	t.Run("uncovered step rejected", func(t *testing.T) {
		captured = nil
		rec := httptest.NewRecorder()
		req := buildUpload(t, sampleIR, "")
		up.HandleUpload(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "missing configuration")
		assert.Nil(t, captured)
	})

	t.Run("missing file rejected", func(t *testing.T) {
		captured = nil
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		require.NoError(t, writer.WriteField("name", "x"))
		require.NoError(t, writer.Close())
		req := httptest.NewRequest(http.MethodPost, "/apis/v2beta1/pipelines/upload", bytes.NewReader(buf.Bytes()))
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req = req.WithContext(region.IntoContext(req.Context(), &region.Result{Name: "vn-north-1"}))

		rec := httptest.NewRecorder()
		up.HandleUpload(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "uploadfile")
		assert.Nil(t, captured)
	})
}
