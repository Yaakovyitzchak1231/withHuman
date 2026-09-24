package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

// An external owner's snapshot is shown to the agents the edition says
// see it, with the tools it grants them, and reads as available only
// while the owner is live at the version the snapshot was taken at.
func TestExternalCatalogFollowsTheEditionsVisibility(t *testing.T) {
	ctx := context.Background()
	pg := embeddedpgtest.Start(t)
	store, err := Open(ctx, pg.URL("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := db.Apply(ctx, store.Pool(), db.Migrations()); err != nil {
		t.Fatal(err)
	}
	recordAudit(t, store)
	now := time.Now().UTC()
	org, user, actor := domain.ID(uuid.NewString()), domain.ID(uuid.NewString()), domain.ID(uuid.NewString())
	_, err = store.ActivateOrganization(ctx, application.ActivateOrganizationInput{OrganizationID: org, OrganizationSlug: "catalog-external", OrganizationName: "Catalogue", UserID: user, MembershipID: actor, DisplayName: "Owner", Email: "external@catalog.test", PasswordHash: "test", AdmissionSource: domain.AdmissionManual, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	for _, slug := range []domain.AgentSlug{"agent", "other"} {
		if _, err := store.CreateAgent(ctx, application.CreateAgentInput{OrganizationID: org, ActorID: actor, Name: string(slug), AgentSlug: slug, AgentQuota: application.UnlimitedQuota(), Now: now}); err != nil {
			t.Fatal(err)
		}
	}
	server := uuid.NewString()
	report := application.SaveToolCatalogInput{OrganizationID: org, SourceRef: server, SourceVersion: "revision-1", Now: now, Report: domain.ToolCatalogReport{Runtime: "mcp-gateway", DiscoveryContext: "gateway", Complete: true, Sources: []domain.ToolCatalogSourceReport{{Source: "gateway", Server: "payments", Complete: true, Tools: []domain.ToolDefinition{{Name: "refund", InputSchema: json.RawMessage(`{"type":"object"}`)}, {Name: "lookup"}}}}}}
	if err := store.SaveToolCatalog(ctx, report); err != nil {
		t.Fatal(err)
	}
	read := application.ReadToolCatalogInput{OrganizationID: org, Scope: domain.ScopeSet{All: true}, AgentSlug: "agent", Now: now}
	assert := func(count int, available bool) {
		t.Helper()
		page, err := store.ReadToolCatalog(ctx, read)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != count {
			t.Fatalf("count=%d want=%d %+v", page.Total, count, page)
		}
		for _, tool := range page.Tools {
			if tool.Available != available || tool.AgentSlug != "agent" || tool.Source != "gateway" || tool.Server != "payments" {
				t.Fatalf("unexpected tool: %+v", tool)
			}
		}
	}
	// Nobody sees the owner: nothing is listed.
	assert(0, false)
	// The agent sees one tool of it.
	read.External = []application.ToolSourceVisibility{{SourceRef: server, AgentSlug: "agent", Version: "revision-1", Available: true, Tools: []string{"refund"}}}
	assert(1, true)
	// Every tool.
	read.External[0].Tools = nil
	assert(2, true)
	// The owner moved on: the snapshot is stale until discovered again.
	read.External[0].Version = "revision-2"
	assert(2, false)
	report.SourceVersion = "revision-2"
	if err := store.SaveToolCatalog(ctx, report); err != nil {
		t.Fatal(err)
	}
	assert(2, true)
	// The owner cannot serve (its credential is gone): listed, unavailable.
	read.External[0].Available = false
	assert(2, false)
	// Another agent's sight never leaks into this agent's read, and a
	// read of the whole organization lists the source once per agent.
	read.External = []application.ToolSourceVisibility{{SourceRef: server, AgentSlug: "other", Version: "revision-2", Available: true}}
	assert(0, false)
	read.External = append(read.External, application.ToolSourceVisibility{SourceRef: server, AgentSlug: "agent", Version: "revision-2", Available: true})
	read.AgentSlug = ""
	page, err := store.ReadToolCatalog(ctx, read)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 4 || len(page.Sources) != 2 {
		t.Fatalf("organization read = %d tools over %d sources", page.Total, len(page.Sources))
	}
	// An instance may not report an edition's source, and an owner may
	// not report a runtime's.
	if err := store.SaveToolCatalog(ctx, application.SaveToolCatalogInput{OrganizationID: org, SourceRef: server, Now: now, Report: domain.ToolCatalogReport{Runtime: "codex", DiscoveryContext: "x", Sources: []domain.ToolCatalogSourceReport{{Source: "mcp", Server: "s"}}}}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("owner reporting a runtime source: %v", err)
	}
}
