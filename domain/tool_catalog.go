package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ToolDefinition preserves the advertised schema. Name is the spelling in
// approval requests, which can differ from the runtime's exposed name.
type ToolDefinition struct {
	Name        string                     `json:"name"`
	DisplayName string                     `json:"display_name,omitempty"`
	Description string                     `json:"description,omitempty"`
	InputSchema json.RawMessage            `json:"input_schema,omitempty"`
	Annotations *OnboardingToolAnnotations `json:"annotations,omitempty"`
}

func (t ToolDefinition) Validate() error {
	if strings.TrimSpace(t.Name) == "" || len(t.Name) > 256 || len(t.Description) > 65536 || len(t.DisplayName) > 512 {
		return fmt.Errorf("invalid tool definition: %w", ErrInvalid)
	}
	if len(t.InputSchema) > 262144 {
		return fmt.Errorf("tool schema exceeds 256 KiB: %w", ErrInvalid)
	}
	if len(t.InputSchema) > 0 {
		var schema map[string]json.RawMessage
		if json.Unmarshal(t.InputSchema, &schema) != nil || schema == nil {
			return fmt.Errorf("input_schema must be an object: %w", ErrInvalid)
		}
	}
	return nil
}
func (t ToolDefinition) SchemaFingerprint() string {
	if len(t.InputSchema) == 0 {
		return ""
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(t.InputSchema)))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return ""
	}
	canonical, _ := json.Marshal(value)
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:])
}

type ToolCatalogSourceReport struct {
	Source   string           `json:"source"`
	Server   string           `json:"server,omitempty"`
	Complete bool             `json:"complete"`
	Error    string           `json:"error,omitempty"`
	Tools    []ToolDefinition `json:"tools"`
}

// RuntimeToolSource reports whether a source name is one an agent
// runtime reports about itself: its built-in tools or the MCP servers it
// is configured with. Every other name belongs to an edition's external
// tool source (the hosted gateway reports "gateway"), which instances
// may never claim.
func RuntimeToolSource(source string) bool { return source == "builtin" || source == "mcp" }

var toolSourcePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// Complete describes enumeration of sources, independently of each source's
// listing. A failed source must remain in Sources with Complete=false.
type ToolCatalogReport struct {
	Runtime          string                    `json:"runtime"`
	DiscoveryContext string                    `json:"discovery_context"`
	Complete         bool                      `json:"complete"`
	Sources          []ToolCatalogSourceReport `json:"sources"`
}

func (r ToolCatalogReport) Validate() error {
	if r.Runtime == "" || len(r.Runtime) > 128 || r.DiscoveryContext == "" || len(r.DiscoveryContext) > 256 || len(r.Sources) > 200 {
		return fmt.Errorf("invalid catalogue report: %w", ErrInvalid)
	}
	seen := map[string]bool{}
	count := 0
	bytes := 0
	for _, s := range r.Sources {
		if !toolSourcePattern.MatchString(s.Source) || len(s.Server) > 256 || (s.Source != "builtin" && s.Server == "") || len(s.Error) > 1024 || (s.Complete && s.Error != "") {
			return fmt.Errorf("invalid catalogue source: %w", ErrInvalid)
		}
		key := s.Source + "/" + s.Server
		if seen[key] {
			return fmt.Errorf("duplicate catalogue source: %w", ErrInvalid)
		}
		seen[key] = true
		names := map[string]bool{}
		for _, t := range s.Tools {
			if err := t.Validate(); err != nil {
				return err
			}
			if names[t.Name] {
				return fmt.Errorf("duplicate tool in source: %w", ErrInvalid)
			}
			names[t.Name] = true
			count++
			bytes += len(t.InputSchema) + len(t.Description) + len(t.Name) + len(t.DisplayName)
		}
	}
	if bytes > 8<<20 {
		return fmt.Errorf("catalogue report exceeds 8 MiB: %w", ErrInvalid)
	}
	if count > 5000 {
		return fmt.Errorf("at most 5000 tools per report: %w", ErrInvalid)
	}
	return nil
}

type ToolCatalogEntry struct {
	ToolDefinition
	ID                ID        `json:"id"`
	AgentSlug         AgentSlug `json:"agent_slug"`
	AgentInstanceID   ID        `json:"agent_instance_id,omitempty"`
	InstanceName      string    `json:"instance_name,omitempty"`
	DiscoveryContext  string    `json:"discovery_context"`
	Runtime           string    `json:"runtime"`
	Source            string    `json:"source"`
	Server            string    `json:"server,omitempty"`
	Available         bool      `json:"available"`
	Stale             bool      `json:"stale"`
	Error             string    `json:"error,omitempty"`
	SchemaFingerprint string    `json:"schema_fingerprint"`
	LastSeenAt        time.Time `json:"last_seen_at"`
	LastAttemptAt     time.Time `json:"last_attempt_at"`
}
// ToolCatalogSource is one discovery source as the reader's agent sees
// it. Available says whether its owner can serve now: the reporting
// instance is active with a live credential, or the edition reports the
// external source live at the snapshot's version.
type ToolCatalogSource struct {
	ID               ID         `json:"id"`
	AgentSlug        AgentSlug  `json:"agent_slug"`
	AgentInstanceID  ID         `json:"agent_instance_id,omitempty"`
	InstanceName     string     `json:"instance_name,omitempty"`
	Available        bool       `json:"available"`
	Runtime          string     `json:"runtime"`
	DiscoveryContext string     `json:"discovery_context"`
	Source           string     `json:"source"`
	Server           string     `json:"server,omitempty"`
	LastAttemptAt    time.Time  `json:"last_attempt_at"`
	LastSuccessAt    *time.Time `json:"last_success_at"`
	Error            string     `json:"error,omitempty"`
}
type ToolCatalogPage struct {
	Tools      []ToolCatalogEntry  `json:"tools"`
	Sources    []ToolCatalogSource `json:"sources"`
	Total      int                 `json:"total"`
	NextOffset *int                `json:"next_offset"`
}

// ToolDefinitionUse is one place a tool definition is offered: a runtime,
// the instance that reported it when an instance did, and whether it can
// serve there now.
type ToolDefinitionUse struct {
	Runtime      string `json:"runtime"`
	InstanceName string `json:"instance_name,omitempty"`
	Available    bool   `json:"available"`
	Stale        bool   `json:"stale"`
}

// CatalogToolDefinition is one distinct definition in the catalogue: a
// tool name on a server with one schema and description, however many
// agents and instances offer it.
type CatalogToolDefinition struct {
	Name        string              `json:"name"`
	Server      string              `json:"server,omitempty"`
	Description string              `json:"description,omitempty"`
	InputSchema json.RawMessage     `json:"input_schema,omitempty"`
	Uses        []ToolDefinitionUse `json:"uses"`
}

// ToolDefinitionList is the catalogue folded for the pipeline editors:
// every distinct definition, and every server a source names, including
// sources where discovery found no tools.
type ToolDefinitionList struct {
	ToolDefinitions []CatalogToolDefinition `json:"tool_definitions"`
	Servers         []string                `json:"servers"`
}
