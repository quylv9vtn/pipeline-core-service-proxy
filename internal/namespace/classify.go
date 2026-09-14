// Package namespace resolves the Project-ID header to a KFP namespace and
// injects it into proxied requests per the regional KFP contract.
package namespace

import "strings"

// Inject describes how the namespace reaches the upstream request.
type Inject int

const (
	// InjectNone: project-scoped endpoint that takes no namespace on this
	// operation (e.g. run create — KFP derives it from experiment_id).
	InjectNone Inject = iota
	// InjectJSONBody: set/overwrite the top-level "namespace" JSON body field.
	InjectJSONBody
	// InjectQuery: set the ?namespace= query parameter.
	InjectQuery
)

// Match is the per-endpoint namespace behavior decided by Classify.
type Match struct {
	Scoped bool // endpoint requires Project-ID + provisioned namespace
	Inject Inject
	// CheckResponse buffers a JSON response and replaces it with 404 when
	// the resource's namespace differs from the request namespace
	// (single-resource fetch — no existence leak across projects).
	CheckResponse bool
}

// kfpPrefix is the only API prefix with project-scoped endpoints.
const kfpPrefix = "/apis/v2beta1/"

// Classify maps (method, path) to the namespace behavior. Endpoints outside
// the KFP v2 API tree (or entity types the gateway does not scope) pass
// through untouched.
//
// Contract verified against the live KFP 2.16.1 swagger:
//   - POST experiments / pipelines / recurringruns: body field "namespace"
//   - POST runs: no namespace (derived from experiment_id)
//   - GET lists (runs, experiments, pipelines, recurringruns): ?namespace=
//   - POST pipelines/upload (multipart): ?namespace=
//   - POST pipelines/upload_version: no namespace (inherits from pipeline)
func Classify(method, path string) Match {
	rest, ok := strings.CutPrefix(path, kfpPrefix)
	if !ok {
		return Match{}
	}
	segments := strings.Split(rest, "/")

	switch segments[0] {
	case "pipelines":
		if len(segments) == 2 && segments[1] == "upload" && method == "POST" {
			return Match{Scoped: true, Inject: InjectQuery}
		}
		if len(segments) == 2 && segments[1] == "upload_version" && method == "POST" {
			return Match{Scoped: true, Inject: InjectNone}
		}
		return classifyEntity("pipelines", segments[1:], method)
	case "experiments", "runs", "recurringruns":
		return classifyEntity(segments[0], segments[1:], method)
	default:
		return Match{} // artifacts, tasks, healthz, ...: not project-scoped
	}
}

// classifyEntity handles the entity subtree after the entity name:
//   - collection (no id): POST create / GET list / other
//   - /{id} or /{id}/...: single-resource operations
func classifyEntity(entity string, idSegments []string, method string) Match {
	if len(idSegments) == 0 {
		switch method {
		case "POST":
			if entity == "experiments" || entity == "pipelines" || entity == "recurringruns" {
				return Match{Scoped: true, Inject: InjectJSONBody}
			}
			return Match{Scoped: true, Inject: InjectNone} // runs
		case "GET":
			return Match{Scoped: true, Inject: InjectQuery}
		default:
			return Match{Scoped: true, Inject: InjectNone}
		}
	}
	// Single resource (/{id} or /{id}/versions/...): verify the returned
	// resource belongs to the request namespace.
	return Match{Scoped: true, Inject: InjectNone, CheckResponse: true}
}
