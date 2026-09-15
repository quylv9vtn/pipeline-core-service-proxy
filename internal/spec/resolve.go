package spec

import (
	"context"
	"fmt"

	"gopkg.in/yaml.v3"
)

// GPU accelerator types (the labels CMP's PipelineGPUModel enum exposed).
const (
	AcceleratorRTX2080 = "NVIDIA-GeForce-RTX-2080"
	AcceleratorH200    = "NVIDIA-H200"
)

// Resources are the resolved flavor/image values for one step.
type Resources struct {
	ImageURL  string
	CPUMilli  int
	MemoryMiB int
	GPUModel  string // empty = none ("ai_rtx2080" | "ai_h200")
	GPUCount  int
}

// AcceleratorType maps the flavor gpu_model value to the KFP accelerator
// resource type (same labels CMP used).
func (r Resources) AcceleratorType() string {
	switch r.GPUModel {
	case "ai_rtx2080":
		return AcceleratorRTX2080
	case "ai_h200":
		return AcceleratorH200
	default:
		return r.GPUModel
	}
}

// ResourceLookup resolves a selection's flavor/image IDs to concrete
// resources (implemented by the catalog store wrapper in the wiring; must
// only return enabled, in-region rows).
type ResourceLookup interface {
	Resolve(ctx context.Context, region, flavorID, imageID string) (Resources, error)
}

// ResolveRewrite matches the step selections, resolves resources and
// rewrites every container executor in place. The document keeps its key
// order (yaml.Node round-trip).
func ResolveRewrite(ctx context.Context, regionName string, ir *IR, steps []StepRef, selections map[string]StepSelection, lookup ResourceLookup) error {
	byTask, err := Match(steps, selections)
	if err != nil {
		return err
	}
	executors, err := ir.Executors()
	if err != nil {
		return err
	}

	// Steps sharing an executor must use the same flavor/image.
	byExecutor := map[string]StepSelection{}
	for _, ref := range steps {
		sel, ok := byTask[ref.Task]
		if !ok {
			continue
		}
		if prev, seen := byExecutor[ref.Executor]; seen {
			if prev != sel {
				return &InvalidSpecError{Field: "steps", Detail: fmt.Sprintf(
					"steps sharing executor '%s' must use the same flavor and image", ref.Executor)}
			}
			continue
		}
		byExecutor[ref.Executor] = sel
	}

	for executorName, sel := range byExecutor {
		executor, err := mapGet(executors, executorName)
		if err != nil || executor.Kind != yaml.MappingNode {
			return invalidIR(fmt.Sprintf("executor '%s' not found", executorName))
		}
		container, err := mapGet(executor, "container")
		if err != nil || container.Kind != yaml.MappingNode {
			return invalidIR(fmt.Sprintf("executor '%s' has no container", executorName))
		}
		res, err := lookup.Resolve(ctx, regionName, sel.FlavorID, sel.ImageID)
		if err != nil {
			return &InvalidSpecError{Field: "steps", Detail: err.Error()}
		}
		applyResources(container, res)
	}
	return nil
}

// applyResources sets container.image and container.resources exactly as
// CMP's rebuild_pipeline_spec did.
func applyResources(container *yaml.Node, res Resources) {
	mapSet(container, "image", strNode(res.ImageURL))

	resMap := &yaml.Node{Kind: yaml.MappingNode}
	cpu := fmt.Sprintf("%dm", res.CPUMilli)
	mem := fmt.Sprintf("%dMi", res.MemoryMiB)
	mapSet(resMap, "resourceCpuRequest", strNode(cpu))
	mapSet(resMap, "resourceCpuLimit", strNode(cpu))
	mapSet(resMap, "resourceMemoryRequest", strNode(mem))
	mapSet(resMap, "resourceMemoryLimit", strNode(mem))
	if res.GPUCount > 0 {
		acc := &yaml.Node{Kind: yaml.MappingNode}
		mapSet(acc, "resourceType", strNode(res.AcceleratorType()))
		mapSet(acc, "resourceCount", strNode(fmt.Sprintf("%d", res.GPUCount)))
		mapSet(resMap, "accelerator", acc)
	}
	mapSet(container, "resources", resMap)
}

// Serialize renders the document back to YAML bytes.
func Serialize(ir *IR) ([]byte, error) {
	out, err := yaml.Marshal(ir.doc)
	if err != nil {
		return nil, fmt.Errorf("serialize spec: %w", err)
	}
	return out, nil
}
