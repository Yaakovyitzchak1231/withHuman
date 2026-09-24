// Package mcpreference holds the grammar an agent needs before it writes
// a pipeline or path document: the block envelope and each type's
// config, the condition tree, and the escalation path document. The
// management MCP server serves it through read_reference, and the setup
// assistant carries the pipeline topics in its prompt, so both read one
// copy.
package mcpreference

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed *.md
var files embed.FS

// Topics lists every topic, in the order a reader meets them.
var Topics = []string{"pipeline_blocks", "conditions", "escalation_paths"}

// Read returns one topic's text. An unknown topic is an error naming
// the choices.
func Read(topic string) (string, error) {
	topic = strings.TrimSpace(topic)
	text, err := files.ReadFile(topic + ".md")
	if err != nil {
		return "", fmt.Errorf("unknown topic %q: choose %s", topic, strings.Join(Topics, ", "))
	}
	return string(text), nil
}

// MustRead returns a topic that ships with the binary; a missing one is
// a build error, not a runtime condition.
func MustRead(topic string) string {
	text, err := Read(topic)
	if err != nil {
		panic(err)
	}
	return text
}
