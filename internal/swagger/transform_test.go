package swagger

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixture is a minimal Swagger 2.0 doc: one operation with existing
// parameters, one without.
const fixture = `{
  "swagger": "2.0",
  "info": {"title": "Kubeflow Pipelines API", "version": "2.16.1", "description": "API."},
  "paths": {
    "/apis/v2beta1/pipelines": {
      "get": {
        "operationId": "PipelineService_ListPipelines",
        "parameters": [{"name": "page_size", "in": "query", "type": "integer"}],
        "responses": {"200": {"description": "ok"}}
      },
      "post": {
        "operationId": "PipelineService_CreatePipeline",
        "parameters": [{"name": "pipeline", "in": "body", "schema": {"$ref": "#/definitions/v2beta1Pipeline"}}],
        "responses": {"200": {"description": "ok"}}
      }
    },
    "/apis/v2beta1/pipelines/{pipeline_id}": {
      "get": {
        "operationId": "PipelineService_GetPipeline",
        "responses": {"200": {"description": "ok"}}
      }
    }
  },
  "definitions": {"v2beta1Pipeline": {"type": "object"}}
}`

func countOperations(t *testing.T, spec []byte) int {
	t.Helper()
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(spec, &doc))
	n := 0
	for _, item := range doc.Paths {
		for method := range item {
			if httpMethods[method] {
				n++
			}
		}
	}
	return n
}

func TestInjectRegionParam(t *testing.T) {
	out, err := InjectRegionParam([]byte(fixture), []string{"vn-north-1"})
	require.NoError(t, err)

	var doc struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name     string `json:"name"`
				In       string `json:"in"`
				Type     string `json:"type"`
				Required bool   `json:"required"`
			} `json:"parameters"`
		} `json:"paths"`
		Info struct {
			Title       string `json:"title"`
			Description string `json:"description"`
		} `json:"info"`
		Definitions json.RawMessage `json:"definitions"`
		Precedence  []string        `json:"x-region-precedence"`
		Valid       []string        `json:"x-region-valid"`
	}
	require.NoError(t, json.Unmarshal(out, &doc))

	total := 0
	for path, item := range doc.Paths {
		for method, op := range item {
			if !httpMethods[method] {
				continue
			}
			total++
			found := false
			for _, p := range op.Parameters {
				if p.Name == "region" && p.In == "query" {
					found = true
					assert.True(t, p.Required, "%s %s", method, path)
					assert.Equal(t, "string", p.Type, "%s %s", method, path)
				}
			}
			assert.True(t, found, "%s %s missing region param", method, path)
		}
	}
	assert.Equal(t, 3, total)

	assert.Equal(t, "Kubeflow Pipelines API (core gateway)", doc.Info.Title)
	assert.Contains(t, doc.Info.Description, "pipeline-core-service")

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(out, &raw))
	assert.Contains(t, string(raw["x-region-precedence"]), "header:X-Region")
	assert.Equal(t, `["vn-north-1"]`, string(raw["x-region-valid"]))

	// Definitions untouched.
	var defs map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw["definitions"], &defs))
	assert.Contains(t, defs, "v2beta1Pipeline")
}

func TestInjectRegionParamIdempotent(t *testing.T) {
	once, err := InjectRegionParam([]byte(fixture), []string{"vn-north-1"})
	require.NoError(t, err)
	twice, err := InjectRegionParam(once, []string{"vn-north-1"})
	require.NoError(t, err)
	assert.Equal(t, countOperations(t, once), countOperations(t, twice))

	var doc struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name string `json:"name"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(twice, &doc))
	for _, item := range doc.Paths {
		for _, op := range item {
			n := 0
			for _, p := range op.Parameters {
				if p.Name == "region" {
					n++
				}
			}
			assert.LessOrEqual(t, n, 1)
		}
	}
}

// TestFallbackSpecTransforms proves the embedded fallback (the checked-in
// copy of the real upstream spec) transforms cleanly and that every real
// operation gains the region parameter.
func TestFallbackSpecTransforms(t *testing.T) {
	out, err := InjectRegionParam(fallbackSpec, []string{"vn-north-1"})
	require.NoError(t, err)
	assert.Greater(t, countOperations(t, out), 40)

	var doc struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name     string `json:"name"`
				In       string `json:"in"`
				Required bool   `json:"required"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(out, &doc))
	for path, item := range doc.Paths {
		for method, op := range item {
			if !httpMethods[method] {
				continue
			}
			found := false
			for _, p := range op.Parameters {
				if p.Name == "region" && p.In == "query" && p.Required {
					found = true
				}
			}
			assert.True(t, found, "%s %s", method, path)
		}
	}
}
