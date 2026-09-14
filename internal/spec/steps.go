package spec

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// StepSelection is one entry of the multipart "steps" form field:
// {task-or-executor-name: {flavor_id, image_id}} — the FE contract carried
// over from CMP.
type StepSelection struct {
	FlavorID string `json:"flavor_id"`
	ImageID  string `json:"image_id"`
}

// ParseSteps decodes the steps JSON payload. An empty payload is valid (a
// pipeline may be uploaded without per-step configuration only if every
// step is covered — Match enforces that).
func ParseSteps(raw []byte) (map[string]StepSelection, error) {
	if len(raw) == 0 {
		return map[string]StepSelection{}, nil
	}
	var m map[string]StepSelection
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, &InvalidSpecError{Field: "steps", Detail: "steps must be a JSON object of {step: {flavor_id, image_id}}"}
	}
	for step, sel := range m {
		if _, err := uuid.Parse(sel.FlavorID); err != nil {
			return nil, &InvalidSpecError{Field: "steps", Detail: fmt.Sprintf("step '%s': flavor_id must be a UUID", step)}
		}
		if _, err := uuid.Parse(sel.ImageID); err != nil {
			return nil, &InvalidSpecError{Field: "steps", Detail: fmt.Sprintf("step '%s': image_id must be a UUID", step)}
		}
	}
	return m, nil
}

// Match binds every container step to a selection, accepting either the
// task name or the executor label as the key (both is ambiguous). Ported
// from CMP rebuild_pipeline_spec. Returns task → selection.
func Match(steps []StepRef, selections map[string]StepSelection) (map[string]StepSelection, error) {
	byTask := map[string]StepSelection{}
	consumed := map[string]bool{}
	var missing, ambiguous []string

	for _, ref := range steps {
		var keys []string
		if _, ok := selections[ref.Task]; ok {
			keys = append(keys, ref.Task)
		}
		if ref.Executor != ref.Task {
			if _, ok := selections[ref.Executor]; ok {
				keys = append(keys, ref.Executor)
			}
		}
		switch len(keys) {
		case 0:
			missing = append(missing, ref.Task)
			continue
		case 1:
			byTask[ref.Task] = selections[keys[0]]
			consumed[keys[0]] = true
		default:
			ambiguous = append(ambiguous, ref.Task)
		}
	}
	var unknown []string
	for name := range selections {
		if !consumed[name] {
			unknown = append(unknown, name)
		}
	}
	if len(missing) > 0 || len(ambiguous) > 0 || len(unknown) > 0 {
		detail := ""
		for _, s := range missing {
			detail += fmt.Sprintf("missing configuration for step '%s'; ", s)
		}
		for _, s := range ambiguous {
			detail += fmt.Sprintf("step '%s' configured via both task name and executor label; ", s)
		}
		for _, s := range unknown {
			detail += fmt.Sprintf("unknown step '%s'; ", s)
		}
		return nil, &InvalidSpecError{Field: "steps", Detail: detail}
	}
	return byTask, nil
}
