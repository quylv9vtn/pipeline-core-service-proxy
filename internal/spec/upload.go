package spec

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
)

// SpecFieldName is the multipart file field carrying the pipeline spec.
const SpecFieldName = "uploadfile"

// StepsFieldName is the gateway-owned multipart field with per-step
// flavor/image selections. It is consumed by the gateway and stripped
// before the request is forwarded.
const StepsFieldName = "steps"

// MaxSpecBytes caps the pipeline spec YAML size (10 MiB — KFP IR files are
// well under this; oversized uploads are rejected before any parse).
const MaxSpecBytes = 10 << 20

// Upload is a parsed multipart upload request.
type Upload struct {
	// Fields are the non-file, non-steps form fields, forwarded verbatim
	// (name, description, pipeline_id, ...).
	Fields map[string][]string
	// Spec is the raw YAML from the file field.
	Spec        []byte
	SpecName    string
	ContentType string
	// stepsRaw is the raw steps field (nil when absent).
	stepsRaw []byte
}

// ParseUpload reads the multipart form of POST /pipelines/upload[-version].
// Requires the spec file field; steps is optional.
func ParseUpload(r *http.Request) (*Upload, error) {
	if err := r.ParseMultipartForm(MaxSpecBytes); err != nil {
		return nil, &InvalidSpecError{Field: "form", Detail: "invalid multipart form: " + err.Error()}
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	up := &Upload{Fields: map[string][]string{}}
	for name, values := range r.MultipartForm.Value {
		if name == StepsFieldName {
			if len(values) > 0 {
				up.stepsRaw = []byte(values[0])
			}
			continue
		}
		up.Fields[name] = append(up.Fields[name], values...)
	}

	file, header, err := r.FormFile(SpecFieldName)
	if err != nil {
		return nil, &InvalidSpecError{Field: SpecFieldName, Detail: fmt.Sprintf("the %q file field is required", SpecFieldName)}
	}
	defer func() { _ = file.Close() }()
	spec, err := io.ReadAll(io.LimitReader(file, MaxSpecBytes+1))
	if err != nil {
		return nil, &InvalidSpecError{Field: SpecFieldName, Detail: "failed to read the spec: " + err.Error()}
	}
	if int64(len(spec)) > MaxSpecBytes {
		return nil, &InvalidSpecError{Field: SpecFieldName, Detail: "spec exceeds the size limit"}
	}
	up.Spec = spec
	up.SpecName = header.Filename
	return up, nil
}

// StepsRaw returns the raw steps field bytes (nil when absent).
func (u *Upload) StepsRaw() []byte { return u.stepsRaw }

// BuildUpstream re-serializes the multipart body: every original field
// except steps, with the spec file replaced by rewritten YAML. Returns the
// body and its Content-Type (with the fresh multipart boundary).
func (u *Upload) BuildUpstream(spec []byte) (*bytes.Buffer, string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for name, values := range u.Fields {
		for _, v := range values {
			if err := writer.WriteField(name, v); err != nil {
				return nil, "", err
			}
		}
	}
	part, err := writer.CreateFormFile(SpecFieldName, u.SpecName)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(spec); err != nil {
		return nil, "", err
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return &buf, writer.FormDataContentType(), nil
}
