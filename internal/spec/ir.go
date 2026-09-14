// Package spec implements the gateway-owned part of spec handling for the
// two multipart upload endpoints: parsing the KFP IR YAML, matching the
// steps selections (flavor/image per step) onto container executors, and
// rewriting the YAML with the resolved resources. Ported from the original
// CMP ai_pipeline.pipeline_spec module (order-preserving via yaml.Node).
package spec

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// IR wraps the parsed KFP IR document (yaml.Node round-trip preserves key
// order and comments, unlike map-based marshal).
type IR struct {
	doc *yaml.Node // document node
}

func (ir *IR) Document() *yaml.Node { return ir.doc }

// LoadIR parses content and validates the KFP IR structural contract
// (schemaVersion 2.x, pipelineInfo.name, root, components,
// deploymentSpec.executors). YAML *syntax* only — semantic validation is
// the regional KFP's job.
func LoadIR(content []byte) (*IR, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, invalidIR(fmt.Sprintf("not valid YAML: %v", err))
	}
	if len(doc.Content) == 0 {
		return nil, invalidIR("empty document")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, invalidIR("the document root must be an object")
	}

	pi, err := mapGet(root, "pipelineInfo")
	if err != nil {
		return nil, invalidIR("pipelineInfo is required")
	}
	name, err := mapGet(pi, "name")
	if err != nil || name.Kind != yaml.ScalarNode || name.Value == "" {
		return nil, invalidIR("pipelineInfo.name is required")
	}

	sv, err := mapGet(root, "schemaVersion")
	if err != nil || sv.Kind != yaml.ScalarNode || len(sv.Value) < 2 || sv.Value[:2] != "2." {
		return nil, invalidIR("schemaVersion must be a KFP IR 2.x version")
	}

	for _, field := range []string{"root", "components"} {
		n, err := mapGet(root, field)
		if err != nil || n.Kind != yaml.MappingNode {
			return nil, invalidIR(field + " must be an object")
		}
	}
	ds, err := mapGet(root, "deploymentSpec")
	if err != nil {
		return nil, invalidIR("deploymentSpec is required")
	}
	executors, err := mapGet(ds, "executors")
	if err != nil || executors.Kind != yaml.MappingNode {
		return nil, invalidIR("deploymentSpec.executors must be an object")
	}
	return &IR{doc: &doc}, nil
}

// RootComponent returns the top-level "root" mapping.
func (ir *IR) RootComponent() (*yaml.Node, error) {
	doc := ir.doc.Content[0]
	root, err := mapGet(doc, "root")
	if err != nil {
		return nil, invalidIR("root must be an object")
	}
	return root, nil
}

// Components returns the "components" mapping.
func (ir *IR) Components() (*yaml.Node, error) {
	doc := ir.doc.Content[0]
	return mapGet(doc, "components")
}

// Executors returns the "deploymentSpec.executors" mapping.
func (ir *IR) Executors() (*yaml.Node, error) {
	doc := ir.doc.Content[0]
	ds, err := mapGet(doc, "deploymentSpec")
	if err != nil {
		return nil, err
	}
	return mapGet(ds, "executors")
}

// mapGet returns the value node of key in a mapping node.
func mapGet(m *yaml.Node, key string) (*yaml.Node, error) {
	if m.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("not a mapping")
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1], nil
		}
	}
	return nil, fmt.Errorf("key %q not found", key)
}

// mapSet sets (or appends) key=value preserving the mapping's key order
// (an existing key keeps its position).
func mapSet(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = value
			return
		}
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	m.Content = append(m.Content, keyNode, value)
}

// strNode builds a scalar string node.
func strNode(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

func invalidIR(detail string) error {
	return &InvalidSpecError{Field: "pipeline_spec", Detail: "Pipeline spec is not valid KFP IR YAML: " + detail}
}

// InvalidSpecError is a gateway-native 4xx spec error (field points at the
// offending part so FE can display it).
type InvalidSpecError struct {
	Field  string
	Detail string
}

func (e *InvalidSpecError) Error() string { return e.Detail }
