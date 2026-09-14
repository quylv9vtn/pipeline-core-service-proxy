package spec

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleIR = `# KFP IR v2
schemaVersion: 2.1.0
pipelineInfo:
  name: training-pipeline
root:
  dag:
    tasks:
      preprocess:
        componentRef:
          name: comp-preprocess
      fan-out:
        componentRef:
          name: comp-dag-inner
components:
  comp-preprocess:
    executorLabel: exec-preprocess
  comp-dag-inner:
    dag:
      tasks:
        train:
          componentRef:
            name: comp-train
  comp-train:
    executorLabel: exec-train
deploymentSpec:
  executors:
    exec-preprocess:
      container:
        image: python:3.9
        command:
        - python
    exec-train:
      container:
        image: trainer:old
`

func TestLoadIR(t *testing.T) {
	ir, err := LoadIR([]byte(sampleIR))
	require.NoError(t, err)

	t.Run("rejects non-IR documents", func(t *testing.T) {
		bad := [][]byte{
			[]byte(`just a string`),
			[]byte(`schemaVersion: 1.0.0`),
			[]byte(`schemaVersion: 2.0.0`),
			[]byte(`schemaVersion: 2.0.0
pipelineInfo:
  name: x`),
			[]byte(`schemaVersion: 2.0.0
pipelineInfo:
  name: x
root: {}
deploymentSpec: {}`),
		}
		for _, b := range bad {
			_, err := LoadIR(b)
			assert.Error(t, err)
		}
	})

	t.Run("container executors by step walks nested dags", func(t *testing.T) {
		steps, err := ContainerExecutorsByStep(ir)
		require.NoError(t, err)
		require.Len(t, steps, 2)
		assert.Equal(t, StepRef{Task: "preprocess", Executor: "exec-preprocess"}, steps[0])
		assert.Equal(t, StepRef{Task: "train", Executor: "exec-train"}, steps[1])
	})

}


type fakeLookup struct {
	res   Resources
	calls int
}

func (f *fakeLookup) Resolve(_ context.Context, region, flavorID, imageID string) (Resources, error) {
	f.calls++
	return f.res, nil
}

func TestResolveRewrite(t *testing.T) {
	ir, err := LoadIR([]byte(sampleIR))
	require.NoError(t, err)
	steps, err := ContainerExecutorsByStep(ir)
	require.NoError(t, err)

	gpu := "ai_h200"
	lookup := &fakeLookup{res: Resources{
		ImageURL: "registry/train:2.0", CPU: 8, MemoryGiB: 32, GPUModel: gpu, GPUCount: 2,
	}}
	selections := map[string]StepSelection{
		"preprocess": {FlavorID: "f1", ImageID: "i1"},
		"exec-train": {FlavorID: "f2", ImageID: "i2"}, // by executor label
	}
	require.NoError(t, ResolveRewrite(context.Background(), "vn-north-1", ir, steps, selections, lookup))
	assert.Equal(t, 2, lookup.calls)

	rewritten, err := Serialize(ir)
	require.NoError(t, err)
	text := string(rewritten)
	assert.Contains(t, text, "image: registry/train:2.0")
	assert.Contains(t, text, "resourceCpuRequest: \"8\"")
	assert.Contains(t, text, "resourceMemoryLimit: 32Gi")
	assert.Contains(t, text, "resourceType: NVIDIA-H200")
	assert.Contains(t, text, "resourceCount: \"2\"")
	assert.Contains(t, text, "pipelineInfo:", "key order preserved")
	assert.NotContains(t, text, "trainer:old")

	// Re-parse the rewritten doc: still valid IR.
	ir2, err := LoadIR(rewritten)
	require.NoError(t, err)
	_, err = ContainerExecutorsByStep(ir2)
	require.NoError(t, err)
}

func TestMatch(t *testing.T) {
	steps := []StepRef{
		{Task: "preprocess", Executor: "exec-pre"},
		{Task: "train", Executor: "exec-train"},
	}
	sels := map[string]StepSelection{
		"preprocess": {FlavorID: "f", ImageID: "i"},
		"exec-train": {FlavorID: "f2", ImageID: "i2"},
	}

	t.Run("full coverage", func(t *testing.T) {
		byTask, err := Match(steps, sels)
		require.NoError(t, err)
		assert.Len(t, byTask, 2)
	})

	t.Run("missing step", func(t *testing.T) {
		_, err := Match(steps, map[string]StepSelection{"preprocess": {FlavorID: "f", ImageID: "i"}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing configuration for step 'train'")
	})

	t.Run("unknown step", func(t *testing.T) {
		extra := map[string]StepSelection{
			"preprocess": {FlavorID: "f", ImageID: "i"},
			"exec-train": {FlavorID: "f2", ImageID: "i2"},
			"ghost":      {FlavorID: "f3", ImageID: "i3"},
		}
		_, err := Match(steps, extra)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown step 'ghost'")
	})

	t.Run("both names configured is ambiguous", func(t *testing.T) {
		both := map[string]StepSelection{
			"preprocess": {FlavorID: "f", ImageID: "i"},
			"train":      {FlavorID: "f2", ImageID: "i2"},
			"exec-train": {FlavorID: "f2", ImageID: "i2"},
		}
		_, err := Match(steps, both)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "both task name and executor label")
	})
}

func TestParseSteps(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		m, err := ParseSteps(nil)
		require.NoError(t, err)
		assert.Empty(t, m)
	})
	t.Run("valid", func(t *testing.T) {
		m, err := ParseSteps([]byte(`{"train":{"flavor_id":"0b9e6c7a-0000-4000-8000-000000000001","image_id":"0b9e6c7a-0000-4000-8000-000000000002"}}`))
		require.NoError(t, err)
		require.Len(t, m, 1)
	})
	t.Run("invalid json", func(t *testing.T) {
		_, err := ParseSteps([]byte(`{`))
		assert.Error(t, err)
	})
	t.Run("non-uuid flavor", func(t *testing.T) {
		_, err := ParseSteps([]byte(`{"train":{"flavor_id":"abc","image_id":"0b9e6c7a-0000-4000-8000-000000000002"}}`))
		assert.Error(t, err)
	})
}
