package discovery

import (
	"bytes"
	"fmt"

	toml "github.com/pelletier/go-toml/v2"
)

// loadTOML decodes a TOML document into nested maps. A missing or empty
// file is an empty table.
func loadTOML(content []byte, existed bool) (map[string]any, error) {
	table := map[string]any{}
	if !existed || len(bytes.TrimSpace(content)) == 0 {
		return table, nil
	}
	if err := toml.Unmarshal(content, &table); err != nil {
		return nil, fmt.Errorf("parse toml: %w", err)
	}
	return table, nil
}

// tomlTableIfPresent returns a nested table without creating it.
func tomlTableIfPresent(parent map[string]any, key string) (map[string]any, bool) {
	child, ok := parent[key].(map[string]any)
	return child, ok
}

// tomlString reads a string member.
func tomlString(table map[string]any, key string) string {
	value, _ := table[key].(string)
	return value
}

// tomlStrings reads an array of strings.
func tomlStrings(value any) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// tomlStringMap reads a table of strings.
func tomlStringMap(value any) map[string]string {
	table, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for key, item := range table {
		if s, ok := item.(string); ok {
			out[key] = s
		}
	}
	return out
}
