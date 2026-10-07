package application

import (
	"context"
	"regexp"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

type ToolCatalogRepository interface {
	SaveToolCatalog(context.Context, SaveToolCatalogInput) error
	ReadToolCatalog(context.Context, ReadToolCatalogInput) (domain.ToolCatalogPage, error)
}

// SaveToolCatalogInput is one discovery report: from an agent instance,
// about the tools its runtime sees, or from an edition's tool source
// about the tools an external owner serves.
type SaveToolCatalogInput struct {
	OrganizationID  domain.ID
	AgentInstanceID domain.ID
	// SourceRef names the external owner of the catalogue (an edition's
	// tool source, such as the hosted gateway's server id) when the
	// report did not come from an instance. It is opaque to core.
	SourceRef string
	// SourceVersion is the owner's version the snapshot was taken at (a
	// configuration revision and its credential, say); the read shows the
	// snapshot as available only while the owner still reports it. A
	// source may report a version of any length: the catalogue service
	// stores its digest.
	SourceVersion string
	Report        domain.ToolCatalogReport
	Now           time.Time
}

var toolSourceRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)

func (i SaveToolCatalogInput) Validate() error {
	if (i.AgentInstanceID == "") == (i.SourceRef == "") {
		return invalidField("catalogue owner", "exactly one instance or source reference is required")
	}
	if i.SourceRef != "" && !toolSourceRefPattern.MatchString(i.SourceRef) {
		return invalidField("source reference", "must be a key of at most 256 characters")
	}
	if len(i.SourceVersion) > 256 {
		return invalidField("source version", "must be at most 256 characters")
	}
	for _, source := range i.Report.Sources {
		if domain.RuntimeToolSource(source.Source) != (i.AgentInstanceID != "") {
			return invalidField("catalogue source", "instances report the runtime's own sources and external owners report their own")
		}
	}
	return firstError([]error{requiredID("organization ID", i.OrganizationID), requiredTime("current time", i.Now), i.Report.Validate()})
}

type ReadToolCatalogInput struct {
	OrganizationID domain.ID
	Scope          domain.ScopeSet
	AgentSlug      domain.AgentSlug
	Now            time.Time
	// External lists the edition's sources the read may show: which
	// agents see each external owner, at which version, and with which
	// tools. Grants and availability are the edition's live state, never
	// a stored decision, so the edition resolves them for every read.
	External []ToolSourceVisibility
}

func (i ReadToolCatalogInput) Validate() error {
	for _, visibility := range i.External {
		if err := visibility.Validate(); err != nil {
			return err
		}
	}
	return firstError([]error{requiredID("organization ID", i.OrganizationID), validScopeSet("scope", i.Scope, domain.ScopeAgent), requiredTime("current time", i.Now)})
}

// ToolSourceScope is what a read of the catalogue covers: the reader's
// agent scope and an optional single agent.
type ToolSourceScope struct {
	OrganizationID domain.ID
	Scope          domain.ScopeSet
	AgentSlug      domain.AgentSlug
}

func (s ToolSourceScope) Validate() error {
	return firstError([]error{requiredID("organization ID", s.OrganizationID), validScopeSet("scope", s.Scope, domain.ScopeAgent)})
}

// ToolSourceTarget is one external owner the edition serves tools for.
type ToolSourceTarget struct {
	OrganizationID domain.ID
	SourceRef      string
}

func (t ToolSourceTarget) Validate() error {
	if !toolSourceRefPattern.MatchString(t.SourceRef) {
		return invalidField("source reference", "must be a key of at most 256 characters")
	}
	return requiredID("organization ID", t.OrganizationID)
}

// ToolSourceVisibility says that one agent sees one external owner's
// tools: at the owner's current version, whether the owner can serve
// calls right now, and which tools the agent may use (nil for all).
type ToolSourceVisibility struct {
	SourceRef string
	AgentSlug domain.AgentSlug
	Version   string
	Available bool
	Tools     []string
}

func (v ToolSourceVisibility) Validate() error {
	if !toolSourceRefPattern.MatchString(v.SourceRef) {
		return invalidField("source reference", "must be a key of at most 256 characters")
	}
	if len(v.Version) > 256 {
		return invalidField("source version", "must be at most 256 characters")
	}
	return requiredString("agent slug", string(v.AgentSlug))
}

// ToolSource is an edition's external tool source, the hosted MCP
// gateway being the one that exists: it names the owners a read should
// refresh, versions them so a snapshot is refreshed only when the owner
// changed, discovers their tools, and says which agents see them. Core
// keeps the snapshots and never decides access from them.
type ToolSource interface {
	Targets(ctx context.Context, scope ToolSourceScope) ([]ToolSourceTarget, error)
	// State reports the owner's current version: opaque, of any length,
	// and only ever compared.
	State(ctx context.Context, target ToolSourceTarget) (string, error)
	// Discover lists the owner's tools as a report to save; a failed
	// listing still returns a report marking the source incomplete, with
	// the error beside it.
	Discover(ctx context.Context, target ToolSourceTarget) (SaveToolCatalogInput, error)
	Visibility(ctx context.Context, scope ToolSourceScope) ([]ToolSourceVisibility, error)
}
