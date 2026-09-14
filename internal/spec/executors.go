package spec

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// StepRef maps a container step (DAG task name) to its executor name.
type StepRef struct {
	Task     string
	Executor string
}

// ContainerExecutorsByStep walks the component DAG and returns every
// container step in encounter order (deterministic error messages).
// Non-container components are descended into; cycles are rejected.
// Ported from CMP _container_executors_by_step.
func ContainerExecutorsByStep(ir *IR) ([]StepRef, error) {
	components, err := ir.Components()
	if err != nil {
		return nil, invalidIR("components must be an object")
	}
	root, err := ir.RootComponent()
	if err != nil {
		return nil, err
	}
	var out []StepRef
	seen := map[string]bool{}
	if err := walkComponent(ir, components, root, map[string]bool{}, &out, seen); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, invalidIR("the pipeline must contain at least one container step")
	}
	return out, nil
}

func walkComponent(ir *IR, components, component *yaml.Node, ancestors map[string]bool, out *[]StepRef, seen map[string]bool) error {
	dag, err := mapGet(component, "dag")
	if err != nil || dag.Kind != yaml.MappingNode {
		return invalidIR("every DAG component must contain a tasks object")
	}
	tasks, err := mapGet(dag, "tasks")
	if err != nil || tasks.Kind != yaml.MappingNode {
		return invalidIR("every DAG component must contain a tasks object")
	}
	for i := 0; i+1 < len(tasks.Content); i += 2 {
		stepName := tasks.Content[i].Value
		task := tasks.Content[i+1]
		if task.Kind != yaml.MappingNode {
			return invalidIR("DAG tasks must be named objects")
		}
		var componentRefName string
		if ref, err := mapGet(task, "componentRef"); err == nil {
			if n, err := mapGet(ref, "name"); err == nil && n.Kind == yaml.ScalarNode {
				componentRefName = n.Value
			}
		}
		if componentRefName == "" {
			return invalidIR(fmt.Sprintf("step '%s' references an unknown component", stepName))
		}
		referenced, err := mapGet(components, componentRefName)
		if err != nil || referenced.Kind != yaml.MappingNode {
			return invalidIR(fmt.Sprintf("step '%s' references an unknown component", stepName))
		}
		if ancestors[componentRefName] {
			return invalidIR(fmt.Sprintf("component cycle detected at '%s'", componentRefName))
		}

		executorName, err := mapGet(referenced, "executorLabel")
		if err == nil && executorName.Kind == yaml.ScalarNode && executorName.Value != "" {
			executors, err := ir.Executors()
			if err != nil {
				return err
			}
			executor, err := mapGet(executors, executorName.Value)
			if err != nil || executor.Kind != yaml.MappingNode {
				return invalidIR(fmt.Sprintf("step '%s' references an unknown executor", stepName))
			}
			if _, err := mapGet(executor, "container"); err != nil {
				return invalidIR(fmt.Sprintf("step '%s' does not reference a container executor", stepName))
			}
			if seen[stepName] {
				return invalidIR(fmt.Sprintf("container step name '%s' is duplicated", stepName))
			}
			seen[stepName] = true
			*out = append(*out, StepRef{Task: stepName, Executor: executorName.Value})
			continue
		}
		// Sub-DAG component: recurse.
		next := map[string]bool{}
		for k := range ancestors {
			next[k] = true
		}
		next[componentRefName] = true
		if err := walkComponent(ir, components, referenced, next, out, seen); err != nil {
			return err
		}
	}
	return nil
}
