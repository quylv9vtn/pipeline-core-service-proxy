package spec

import (
	"bytes"
	"io"
	"net/http"

	"github.com/golang/glog"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/region"
)

// Uploader combines what the upload handler needs: resource resolution and
// the dispatcher for the actual forward.
type Uploader struct {
	Lookup ResourceLookup
	// Forward proxies the (rewritten) request to the region's KFP.
	Forward func(w http.ResponseWriter, r *http.Request)
}

// HandleUpload serves POST /pipelines/upload and /pipelines/upload_version:
// the gateway's only body-mutating path (requirements §9.1). Pipeline:
//
//	parse multipart → parse steps → LoadIR + executor walk → resolve
//	flavor/image (region-scoped, enabled) → rewrite YAML → rebuild
//	multipart without steps → forward.
//
// Invalid spec/steps/flavor/image never reach the upstream.
func (u *Uploader) HandleUpload(w http.ResponseWriter, r *http.Request) {
	res, ok := region.FromContext(r.Context())
	if !ok {
		(&region.APIError{Status: http.StatusInternalServerError, Code: "internal_error",
			Message: "No region resolved for request."}).Write(w)
		return
	}

	up, err := ParseUpload(r)
	if err != nil {
		writeSpecError(w, err)
		return
	}

	selections, err := ParseSteps(up.StepsRaw())
	if err != nil {
		writeSpecError(w, err)
		return
	}

	ir, err := LoadIR(up.Spec)
	if err != nil {
		writeSpecError(w, err)
		return
	}
	steps, err := ContainerExecutorsByStep(ir)
	if err != nil {
		writeSpecError(w, err)
		return
	}
	if err := ResolveRewrite(r.Context(), res.Name, ir, steps, selections, u.Lookup); err != nil {
		writeSpecError(w, err)
		return
	}
	rewritten, err := Serialize(ir)
	if err != nil {
		writeSpecError(w, err)
		return
	}

	body, contentType, err := up.BuildUpstream(rewritten)
	if err != nil {
		(&region.APIError{Status: http.StatusInternalServerError, Code: "internal_error",
			Message: "Failed to rebuild the upload."}).Write(w)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body.Bytes()))
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body.Bytes())), nil }
	r.ContentLength = int64(body.Len())
	r.Header.Set("Content-Type", contentType)
	r.Header.Del("Transfer-Encoding")

	glog.V(2).Infof("upload %s rewritten: %d bytes, %d step(s)", r.URL.Path, body.Len(), len(selections))
	u.Forward(w, r)
}

func writeSpecError(w http.ResponseWriter, err error) {
	if ise, ok := err.(*InvalidSpecError); ok {
		(&region.APIError{
			Status:  http.StatusBadRequest,
			Code:    "invalid_" + ise.Field,
			Message: ise.Detail,
		}).Write(w)
		return
	}
	(&region.APIError{
		Status:  http.StatusInternalServerError,
		Code:    "internal_error",
		Message: "Spec processing failed.",
	}).Write(w)
}
