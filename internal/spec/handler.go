package spec

import (
	"bytes"
	"fmt"
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
	// Lister checks display_name uniqueness. Nil disables the checks and
	// name generation.
	Lister NameLister
}

// The upload routes served by HandleUpload. Both share the display_name
// contract; they differ only in the uniqueness scope.
const (
	uploadPipelinePath = "/apis/v2beta1/pipelines/upload"
	uploadVersionPath  = "/apis/v2beta1/pipelines/upload_version"
)

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

	// Name resolution runs last: only valid, fully-resolved uploads spend
	// the KFP list call it may make (display-name uniqueness check).
	switch r.URL.Path {
	case uploadPipelinePath:
		if apiErr := u.resolveUploadNames(r, up); apiErr != nil {
			apiErr.Write(w)
			return
		}
	case uploadVersionPath:
		if apiErr := u.resolveVersionNames(r, up); apiErr != nil {
			apiErr.Write(w)
			return
		}
	}
	up.MoveFieldsToQuery(r)

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

// name contract shared by both upload routes: the client never sends a
// technical name — display_name only, and the gateway generates
// slug(display_name)+random. Rejecting `name` keeps the uniqueness guarantee
// meaningful (a client-chosen name could collide despite the check).
func rejectedName(r *http.Request, up *Upload) *region.APIError {
	if firstField(up.Fields, "name") != "" || r.URL.Query().Get("name") != "" {
		return &region.APIError{
			Status: http.StatusBadRequest,
			Code:   "name_not_supported",
			Message: "The name field is not supported — send display_name only; " +
				"the gateway generates the technical name from it.",
		}
	}
	return nil
}

// resolveUploadNames enforces the project-level display-name contract on
// POST /pipelines/upload: display_name is unique per project namespace — a
// duplicate aborts the upload with a specific 409 before anything reaches
// KFP — and the technical name is generated as slug(display_name)+random.
//
// The resolved values are written back into the form fields, which
// MoveFieldsToQuery then promotes to KFP's query params.
func (u *Uploader) resolveUploadNames(r *http.Request, up *Upload) *region.APIError {
	if apiErr := rejectedName(r, up); apiErr != nil {
		return apiErr
	}
	displayName := firstField(up.Fields, "display_name")
	if displayName == "" {
		displayName = specStem(up.SpecName)
	}

	if u.Lister != nil {
		res, _ := region.FromContext(r.Context())
		existing, err := u.Lister.ListDisplayNames(r.Context(), res.Name, r.URL.Query().Get("namespace"))
		if err != nil {
			glog.Errorf("upload %s: list existing pipelines failed: %v", r.URL.Path, err)
			return region.UpstreamUnavailable(res.Name, err)
		}
		if _, dup := existing[displayName]; dup {
			return &region.APIError{
				Status: http.StatusConflict,
				Code:   "display_name_already_exists",
				Message: fmt.Sprintf("A pipeline with display name %q already exists in this project. "+
					"Display names must be unique — please choose another name, or upload a new version of the existing pipeline.", displayName),
			}
		}
	}

	up.Fields["name"] = []string{Slugify(displayName) + "-" + randSuffix()}
	up.Fields["display_name"] = []string{displayName}
	return nil
}

// resolveVersionNames enforces the display-name contract on POST
// /pipelines/upload_version: display_name is unique among the versions of
// the parent pipeline, and the version name is generated the same way.
func (u *Uploader) resolveVersionNames(r *http.Request, up *Upload) *region.APIError {
	if apiErr := rejectedName(r, up); apiErr != nil {
		return apiErr
	}
	displayName := firstField(up.Fields, "display_name")
	if displayName == "" {
		return &region.APIError{
			Status:  http.StatusBadRequest,
			Code:    "invalid_display_name",
			Message: "The display_name form field is required.",
		}
	}
	pipelineID := firstField(up.Fields, "pipeline_id")
	if pipelineID == "" {
		pipelineID = r.URL.Query().Get("pipelineid")
	}
	if pipelineID == "" {
		return &region.APIError{
			Status:  http.StatusBadRequest,
			Code:    "invalid_pipeline_id",
			Message: "The pipeline_id form field is required.",
		}
	}

	if u.Lister != nil {
		res, _ := region.FromContext(r.Context())
		existing, err := u.Lister.ListVersionDisplayNames(r.Context(), res.Name, pipelineID)
		if err != nil {
			glog.Errorf("upload %s: list pipeline versions failed: %v", r.URL.Path, err)
			return region.UpstreamUnavailable(res.Name, err)
		}
		if _, dup := existing[displayName]; dup {
			return &region.APIError{
				Status: http.StatusConflict,
				Code:   "display_name_already_exists",
				Message: fmt.Sprintf("A version with display name %q already exists on this pipeline. "+
					"Version display names must be unique within the pipeline.", displayName),
			}
		}
	}

	up.Fields["name"] = []string{Slugify(displayName) + "-" + randSuffix()}
	up.Fields["display_name"] = []string{displayName}
	return nil
}
