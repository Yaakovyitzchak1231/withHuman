package discovery

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// loadYAMLMapping decodes a YAML document into a node tree whose root is
// a mapping, so edits keep the person's comments and key order. A missing
// or empty file is an empty mapping.
func loadYAMLMapping(content []byte, existed bool) (*yaml.Node, error) {
	if !existed || len(bytes.TrimSpace(content)) == 0 {
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("parse yaml: root is not a mapping")
	}
	return root, nil
}

// yamlGet returns the value node for key in a mapping.
func yamlGet(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// yamlString reads a string scalar under key, or "".
func yamlString(mapping *yaml.Node, key string) string {
	node := yamlGet(mapping, key)
	if node == nil || node.Kind != yaml.ScalarNode {
		return ""
	}
	return node.Value
}

// yamlStrings reads a sequence of scalars under key.
func yamlStrings(mapping *yaml.Node, key string) []string {
	node := yamlGet(mapping, key)
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		if item.Kind == yaml.ScalarNode {
			out = append(out, item.Value)
		}
	}
	return out
}

// yamlStringMap reads a mapping of scalars under key.
func yamlStringMap(mapping *yaml.Node, key string) map[string]string {
	node := yamlGet(mapping, key)
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	out := map[string]string{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i+1].Kind == yaml.ScalarNode {
			out[node.Content[i].Value] = node.Content[i+1].Value
		}
	}
	return out
}
