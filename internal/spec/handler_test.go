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
	return Resources{ImageURL: "registry/img:9", CPUMilli: 2000, MemoryMiB: 4096}, nil
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
		assert.Contains(t, string(capturedBody), "resourceCpuRequest: 2000m")
		assert.Equal(t, "my-pipe", captured.URL.Query().Get("name"), "form name promoted to query param")
		assert.NotContains(t, string(capturedBody), "my-pipe", "name moved out of the body")
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

	t.Run("duplicate display name rejected with 409 before forward", func(t *testing.T) {
		captured = nil
		up2 := &Uploader{Lookup: okLookup{}, Forward: forward, Lister: fakeLister{"my-pipe": {}}}
		rec := httptest.NewRecorder()
		up2.HandleUpload(rec, buildUpload(t, sampleIR, `{
			"preprocess":{"flavor_id":"0b9e6c7a-0000-4000-8000-000000000001","image_id":"0b9e6c7a-0000-4000-8000-000000000002"},
			"train":{"flavor_id":"0b9e6c7a-0000-4000-8000-000000000003","image_id":"0b9e6c7a-0000-4000-8000-000000000004"}}`))
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Contains(t, rec.Body.String(), "display_name_already_exists")
		assert.Contains(t, rec.Body.String(), "my-pipe")
		assert.Nil(t, captured)
	})

	t.Run("name generated from display name when absent", func(t *testing.T) {
		captured = nil
		up2 := &Uploader{Lookup: okLookup{}, Forward: forward, Lister: fakeLister{}}
		rec := httptest.NewRecorder()
		req := buildUploadWithName(t, sampleIR, "display_name", "Xin chào Việt Nam", `{
			"preprocess":{"flavor_id":"0b9e6c7a-0000-4000-8000-000000000001","image_id":"0b9e6c7a-0000-4000-8000-000000000002"},
			"train":{"flavor_id":"0b9e6c7a-0000-4000-8000-000000000003","image_id":"0b9e6c7a-0000-4000-8000-000000000004"}}`)
		up2.HandleUpload(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		q := captured.URL.Query()
		assert.Equal(t, "Xin chào Việt Nam", q.Get("display_name"))
		assert.Regexp(t, `^xin-chao-viet-nam-[a-z0-9]{6}$`, q.Get("name"))
	})

	t.Run("explicit name is kept verbatim", func(t *testing.T) {
		captured = nil
		up2 := &Uploader{Lookup: okLookup{}, Forward: forward, Lister: fakeLister{}}
		rec := httptest.NewRecorder()
		up2.HandleUpload(rec, buildUpload(t, sampleIR, `{
			"preprocess":{"flavor_id":"0b9e6c7a-0000-4000-8000-000000000001","image_id":"0b9e6c7a-0000-4000-8000-000000000002"},
			"train":{"flavor_id":"0b9e6c7a-0000-4000-8000-000000000003","image_id":"0b9e6c7a-0000-4000-8000-000000000004"}}`))
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "my-pipe", captured.URL.Query().Get("name"))
	})

	t.Run("upload_version skips name resolution", func(t *testing.T) {
		captured = nil
		up2 := &Uploader{Lookup: okLookup{}, Forward: forward, Lister: fakeLister{"my-pipe": {}}}
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		require.NoError(t, writer.WriteField("name", "my-pipe"))
		require.NoError(t, writer.WriteField("steps", `{
			"preprocess":{"flavor_id":"0b9e6c7a-0000-4000-8000-000000000001","image_id":"0b9e6c7a-0000-4000-8000-000000000002"},
			"train":{"flavor_id":"0b9e6c7a-0000-4000-8000-000000000003","image_id":"0b9e6c7a-0000-4000-8000-000000000004"}}`))
		part, err := writer.CreateFormFile("uploadfile", "pipe.yaml")
		require.NoError(t, err)
		_, err = part.Write([]byte(sampleIR))
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		req := httptest.NewRequest(http.MethodPost, "/apis/v2beta1/pipelines/upload_version", bytes.NewReader(buf.Bytes()))
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req = req.WithContext(region.IntoContext(req.Context(), &region.Result{Name: "vn-north-1"}))

		rec := httptest.NewRecorder()
		up2.HandleUpload(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		// No uniqueness check, no suffix: the name passes through as-is.
		assert.Equal(t, "my-pipe", captured.URL.Query().Get("name"))
		_, hasDisplay := captured.URL.Query()["display_name"]
		assert.False(t, hasDisplay)
	})
}

// fakeLister is a PipelineLister stub serving a fixed display-name set.
type fakeLister map[string]struct{}

func (f fakeLister) ListDisplayNames(_ context.Context, _, _ string) (map[string]struct{}, error) {
	return f, nil
}

// buildUploadWithName is buildUpload without its default name field, plus
// one extra form field — for exercising the generated-name path.
func buildUploadWithName(t *testing.T, specYAML, extraKey, extraValue, steps string) *http.Request {
	t.Helper()
	req := buildUpload(t, specYAML, steps)
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	delete(req.MultipartForm.Value, "name")
	req.MultipartForm.Value[extraKey] = []string{extraValue}
	return req
}

func TestMoveFieldsToQuery(t *testing.T) {
	build := func(rawQuery string, fields map[string]string) (*http.Request, *Upload) {
		req := httptest.NewRequest(http.MethodPost, "/apis/v2beta1/pipelines/upload?"+rawQuery, nil)
		up := &Upload{Fields: map[string][]string{}}
		for k, v := range fields {
			up.Fields[k] = []string{v}
		}
		return req, up
	}

	t.Run("promotes form fields to KFP query params", func(t *testing.T) {
		req, up := build("", map[string]string{
			"name":         "my-pipe",
			"display_name": "My Pipe",
			"pipeline_id":  "abc",
		})
		up.MoveFieldsToQuery(req)

		q := req.URL.Query()
		assert.Equal(t, "my-pipe", q.Get("name"))
		assert.Equal(t, "My Pipe", q.Get("display_name"))
		assert.Equal(t, "abc", q.Get("pipelineid"), "pipeline_id maps to KFP's pipelineid key")
		assert.Empty(t, up.Fields, "promoted fields are dropped from the forwarded body")
	})

	t.Run("explicit query param wins over form field", func(t *testing.T) {
		req, up := build("name=explicit", map[string]string{"name": "from-form"})
		up.MoveFieldsToQuery(req)

		assert.Equal(t, "explicit", req.URL.Query().Get("name"))
		assert.Empty(t, up.Fields)
	})

	t.Run("unmapped and empty fields stay in the body", func(t *testing.T) {
		req, up := build("", map[string]string{"custom": "keep", "name": ""})
		up.MoveFieldsToQuery(req)

		assert.Empty(t, req.URL.RawQuery)
		assert.Equal(t, []string{"keep"}, up.Fields["custom"])
		assert.Equal(t, []string{""}, up.Fields["name"], "empty value is not promoted, stays verbatim in the body")
	})
}
