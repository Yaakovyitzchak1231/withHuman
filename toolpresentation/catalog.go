package toolpresentation

import (
	"fmt"
	"regexp"
	"strings"
)

// Selector names the call a presentation is looked up for: the runtime
// that made it (the request's agent type, or the runtime its adapter
// reported), the MCP server the tool came from when it is an MCP tool,
// and the canonical tool name.
type Selector struct {
	Runtime string
	Server  string
	Tool    string
}

// Entry binds a specification to the calls it presents. Empty Runtime
// and Server match any; Tool is exact.
type Entry struct {
	Runtime string `json:"runtime,omitempty"`
	Server  string `json:"server,omitempty"`
	Tool    string `json:"tool"`
	Spec    Spec   `json:"spec"`
}

const (
	MaxToolLength   = 200
	MaxServerLength = 200
)

var (
	runtimePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	serverPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// Validate checks the binding and the specification.
func (e Entry) Validate() error {
	if strings.TrimSpace(e.Tool) == "" {
		return invalid("tool is required")
	}
	if len(e.Tool) > MaxToolLength || strings.ContainsAny(e.Tool, " \t\n\r") {
		return invalid("tool must be one word of at most %d characters", MaxToolLength)
	}
	if e.Runtime != "" && !runtimePattern.MatchString(e.Runtime) {
		return invalid("runtime must match ^[a-z0-9][a-z0-9._-]{0,62}$")
	}
	if e.Server != "" && (len(e.Server) > MaxServerLength || !serverPattern.MatchString(e.Server)) {
		return invalid("server must be a short name of letters, digits, dots, dashes and underscores")
	}
	return e.Spec.Validate()
}

// Matches reports whether the entry presents the selected call.
func (e Entry) Matches(selector Selector) bool {
	if e.Tool != selector.Tool {
		return false
	}
	if e.Runtime != "" && e.Runtime != selector.Runtime {
		return false
	}
	if e.Server != "" && e.Server != selector.Server {
		return false
	}
	return true
}

// specificity ranks matches: a server binding is worth more than a
// runtime one, since the server defines the tool and the runtime only
// carries it.
func (e Entry) specificity() int {
	score := 0
	if e.Server != "" {
		score += 2
	}
	if e.Runtime != "" {
		score++
	}
	return score
}

// Key is the entry's binding as one string, for uniqueness checks and
// messages: "runtime/server/tool" with empty parts left empty.
func (e Entry) Key() string {
	return fmt.Sprintf("%s/%s/%s", e.Runtime, e.Server, e.Tool)
}

// Resolve picks the most specific matching entry; among equally specific
// matches the first wins. It reports false when nothing matches.
func Resolve(entries []Entry, selector Selector) (Entry, bool) {
	index, ok := ResolveIndex(entries, selector)
	if !ok {
		return Entry{}, false
	}
	return entries[index], true
}

// ResolveIndex is Resolve answering the position of the winning entry,
// for callers that keep more than the entry at each position.
func ResolveIndex(entries []Entry, selector Selector) (int, bool) {
	best, bestScore := -1, -1
	for index, entry := range entries {
		if !entry.Matches(selector) {
			continue
		}
		if score := entry.specificity(); score > bestScore {
			best, bestScore = index, score
		}
	}
	return best, best >= 0
}
