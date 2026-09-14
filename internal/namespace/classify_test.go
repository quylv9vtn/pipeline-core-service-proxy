package namespace

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		want   Match
	}{
		// JSON-body creates.
		{"create experiment", "POST", "/apis/v2beta1/experiments", Match{Scoped: true, Inject: InjectJSONBody}},
		{"create pipeline", "POST", "/apis/v2beta1/pipelines", Match{Scoped: true, Inject: InjectJSONBody}},
		{"create recurring run", "POST", "/apis/v2beta1/recurringruns", Match{Scoped: true, Inject: InjectJSONBody}},
		// Run create: namespace derived from experiment.
		{"create run", "POST", "/apis/v2beta1/runs", Match{Scoped: true, Inject: InjectNone}},
		// Lists: query injection.
		{"list runs", "GET", "/apis/v2beta1/runs", Match{Scoped: true, Inject: InjectQuery}},
		{"list experiments", "GET", "/apis/v2beta1/experiments", Match{Scoped: true, Inject: InjectQuery}},
		{"list pipelines", "GET", "/apis/v2beta1/pipelines", Match{Scoped: true, Inject: InjectQuery}},
		{"list recurring runs", "GET", "/apis/v2beta1/recurringruns", Match{Scoped: true, Inject: InjectQuery}},
		// Multipart uploads.
		{"upload pipeline", "POST", "/apis/v2beta1/pipelines/upload", Match{Scoped: true, Inject: InjectQuery}},
		{"upload version", "POST", "/apis/v2beta1/pipelines/upload_version", Match{Scoped: true, Inject: InjectNone}},
		// Single-resource: response check.
		{"get run", "GET", "/apis/v2beta1/runs/abc", Match{Scoped: true, Inject: InjectNone, CheckResponse: true}},
		{"delete experiment", "DELETE", "/apis/v2beta1/experiments/abc", Match{Scoped: true, Inject: InjectNone, CheckResponse: true}},
		{"get pipeline version", "GET", "/apis/v2beta1/pipelines/p1/versions/v2", Match{Scoped: true, Inject: InjectNone, CheckResponse: true}},
		{"get pipeline", "GET", "/apis/v2beta1/pipelines/p1", Match{Scoped: true, Inject: InjectNone, CheckResponse: true}},
		// Not project-scoped.
		{"tasks list", "GET", "/apis/v2beta1/tasks", Match{}},
		{"quota pass-through", "PUT", "/apis/v2beta1/quota/limits", Match{}},
		{"healthz", "GET", "/apis/v2beta1/healthz", Match{}},
		{"other prefix", "GET", "/apis/v1/runs", Match{}},
		{"root", "GET", "/", Match{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Classify(tt.method, tt.path))
		})
	}
}
