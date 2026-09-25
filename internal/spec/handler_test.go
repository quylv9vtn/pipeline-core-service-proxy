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

func buildUpload(t *testing.T, path, specYAML, steps string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if steps != "" {
		require.NoError(t, writer.WriteField("steps", steps))
	}
	part, err := writer.CreateFormFile("uploadfile", "pipe.yaml")
	require.NoError(t, err)
	_, err = part.Write([]byte(specYAML))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req.WithContext(region.IntoContext(req.Context(), &region.Result{Name: "vn-north-1"}))
}

// addFormField injects a form field into an already-built request (the
// multipart form is parsed once and reused by ParseUpload).
func addFormField(t *testing.T, req *http.Request, key, value string) *http.Request {
	t.Helper()
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	req.MultipartForm.Value[key] = []string{value}
	return req
}

const fullSteps = `{
	"preprocess":{"flavor_id":"0b9e6c7a-0000-4000-8000-000000000001","image_id":"0b9e6c7a-0000-4000-8000-000000000002"},
	"train":{"flavor_id":"0b9e6c7a-0000-4000-8000-000000000003","image_id":"0b9e6c7a-0000-4000-8000-000000000004"}}`

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
		req := buildUpload(t, uploadPipelinePath, sampleIR, fullSteps)
		up.HandleUpload(rec, req)

		require.NotNil(t, captured)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.NotContains(t, string(capturedBody), "steps")
		assert.Contains(t, string(capturedBody), "registry/img:9")
		assert.Contains(t, string(capturedBody), "resourceCpuRequest: 2000m")
		// No display_name sent: the filename stem becomes it, and the
		// technical name is generated from it.
		assert.Equal(t, "pipe", captured.URL.Query().Get("display_name"))
		assert.Regexp(t, `^pipe-[a-z0-9]{6}$`, captured.URL.Query().Get("name"))
		assert.Equal(t, int64(len(capturedBody)), captured.ContentLength)
		assert.Contains(t, captured.Header.Get("Content-Type"), "multipart/form-data")
	})

	t.Run("bad spec rejected before forward", func(t *testing.T) {
		captured = nil
		rec := httptest.NewRecorder()
		req := buildUpload(t, uploadPipelinePath, "not: [valid: ir", "")
		up.HandleUpload(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "invalid_pipeline_spec")
		assert.Nil(t, captured, "must not reach upstream")
	})

	t.Run("uncovered step rejected", func(t *testing.T) {
		captured = nil
		rec := httptest.NewRecorder()
		req := buildUpload(t, uploadPipelinePath, sampleIR, "")
		up.HandleUpload(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "missing configuration")
		assert.Nil(t, captured)
	})

	t.Run("missing file rejected", func(t *testing.T) {
		captured = nil
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		require.NoError(t, writer.WriteField("display_name", "x"))
		require.NoError(t, writer.Close())
		req := httptest.NewRequest(http.MethodPost, uploadPipelinePath, bytes.NewReader(buf.Bytes()))
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req = req.WithContext(region.IntoContext(req.Context(), &region.Result{Name: "vn-north-1"}))

		rec := httptest.NewRecorder()
		up.HandleUpload(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "uploadfile")
		assert.Nil(t, captured)
	})

	t.Run("name field is rejected", func(t *testing.T) {
		captured = nil
		rec := httptest.NewRecorder()
		req := addFormField(t, buildUpload(t, uploadPipelinePath, sampleIR, fullSteps), "name", "my-pipe")
		up.HandleUpload(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "name_not_supported")
		assert.Nil(t, captured)
	})

	t.Run("duplicate pipeline display name rejected with 409 before forward", func(t *testing.T) {
		captured = nil
		up2 := &Uploader{Lookup: okLookup{}, Forward: forward, Lister: fakeLister{"my-pipe": {}}}
		rec := httptest.NewRecorder()
		req := addFormField(t, buildUpload(t, uploadPipelinePath, sampleIR, fullSteps), "display_name", "my-pipe")
		up2.HandleUpload(rec, req)
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Contains(t, rec.Body.String(), "display_name_already_exists")
		assert.Contains(t, rec.Body.String(), "my-pipe")
		assert.Nil(t, captured)
	})

	t.Run("name generated from display name", func(t *testing.T) {
		captured = nil
		up2 := &Uploader{Lookup: okLookup{}, Forward: forward, Lister: fakeLister{}}
		rec := httptest.NewRecorder()
		req := addFormField(t, buildUpload(t, uploadPipelinePath, sampleIR, fullSteps), "display_name", "Xin chào Việt Nam")
		up2.HandleUpload(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		q := captured.URL.Query()
		assert.Equal(t, "Xin chào Việt Nam", q.Get("display_name"))
		assert.Regexp(t, `^xin-chao-viet-nam-[a-z0-9]{6}$`, q.Get("name"))
		assert.NotContains(t, string(capturedBody), "Xin chào", "resolved fields moved out of the body")
	})

	t.Run("duplicate version display name rejected with 409", func(t *testing.T) {
		captured = nil
		up2 := &Uploader{Lookup: okLookup{}, Forward: forward, Lister: fakeLister{"v2": {}}}
		rec := httptest.NewRecorder()
		req := buildUpload(t, uploadVersionPath, sampleIR, fullSteps)
		req = addFormField(t, req, "display_name", "v2")
		req = addFormField(t, req, "pipeline_id", "parent-1")
		up2.HandleUpload(rec, req)
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Contains(t, rec.Body.String(), "display_name_already_exists")
		assert.Nil(t, captured)
	})

	t.Run("version name generated from display name", func(t *testing.T) {
		captured = nil
		up2 := &Uploader{Lookup: okLookup{}, Forward: forward, Lister: fakeLister{}}
		rec := httptest.NewRecorder()
		req := buildUpload(t, uploadVersionPath, sampleIR, fullSteps)
		req = addFormField(t, req, "display_name", "Phiên bản 2")
		req = addFormField(t, req, "pipeline_id", "parent-1")
		up2.HandleUpload(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		q := captured.URL.Query()
		assert.Equal(t, "Phiên bản 2", q.Get("display_name"))
		assert.Regexp(t, `^phien-ban-2-[a-z0-9]{6}$`, q.Get("name"))
		assert.Equal(t, "parent-1", q.Get("pipelineid"))
	})

	t.Run("version without display_name rejected", func(t *testing.T) {
		captured = nil
		up2 := &Uploader{Lookup: okLookup{}, Forward: forward, Lister: fakeLister{}}
		rec := httptest.NewRecorder()
		req := addFormField(t, buildUpload(t, uploadVersionPath, sampleIR, fullSteps), "pipeline_id", "parent-1")
		up2.HandleUpload(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "invalid_display_name")
		assert.Nil(t, captured)
	})

	t.Run("version without pipeline_id rejected", func(t *testing.T) {
		captured = nil
		up2 := &Uploader{Lookup: okLookup{}, Forward: forward, Lister: fakeLister{}}
		rec := httptest.NewRecorder()
		req := addFormField(t, buildUpload(t, uploadVersionPath, sampleIR, fullSteps), "display_name", "v2")
		up2.HandleUpload(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "invalid_pipeline_id")
		assert.Nil(t, captured)
	})

	t.Run("version name field rejected", func(t *testing.T) {
		captured = nil
		up2 := &Uploader{Lookup: okLookup{}, Forward: forward, Lister: fakeLister{}}
		rec := httptest.NewRecorder()
		req := buildUpload(t, uploadVersionPath, sampleIR, fullSteps)
		req = addFormField(t, req, "display_name", "v2")
		req = addFormField(t, req, "pipeline_id", "parent-1")
		req = addFormField(t, req, "name", "my-version")
		up2.HandleUpload(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "name_not_supported")
		assert.Nil(t, captured)
	})
}

// fakeLister is a NameLister stub serving a fixed display-name set.
type fakeLister map[string]struct{}

func (f fakeLister) ListDisplayNames(_ context.Context, _, _ string) (map[string]struct{}, error) {
	return f, nil
}

func (f fakeLister) ListVersionDisplayNames(_ context.Context, _, _ string) (map[string]struct{}, error) {
	return f, nil
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
