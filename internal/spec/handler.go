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
	// Lister checks display_name uniqueness in the project namespace. Nil
	// disables the check (name/display_name then pass through verbatim).
	Lister PipelineLister
}

// uploadPipelinePath is the pipeline-upload route served by HandleUpload;
// upload_version shares the handler but skips the name/display_name
// resolution (versions inherit their parent pipeline's identity).
const uploadPipelinePath = "/apis/v2beta1/pipelines/upload"

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
	if r.URL.Path == uploadPipelinePath {
		if apiErr := u.resolveUploadNames(r, up); apiErr != nil {
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

// resolveUploadNames enforces the project-level display-name contract on
// POST /pipelines/upload:
//
//   - display_name is unique per project namespace — a duplicate aborts the
//     upload with a specific 409 before anything reaches KFP;
//   - when the client does not pin a name, it is generated as
//     slug(display_name)+random so re-uploads never collide on the unique
//     name column either.
//
// The resolved values are written back into the form fields, which
// MoveFieldsToQuery then promotes to KFP's query params. An explicit query
// param still wins over everything (MoveFieldsToQuery precedence).
func (u *Uploader) resolveUploadNames(r *http.Request, up *Upload) *region.APIError {
	displayName := firstField(up.Fields, "display_name", "name")
	if displayName == "" {
		displayName = specStem(up.SpecName)
	}
	name := firstField(up.Fields, "name")

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

	if name == "" {
		name = Slugify(displayName) + "-" + randSuffix()
	}
	up.Fields["name"] = []string{name}
	up.Fields["display_name"] = []string{displayName}
	return nil
}
