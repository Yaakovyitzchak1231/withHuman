package toolcatalog

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

type catalogClock struct{ now time.Time }

func (c *catalogClock) Now() time.Time { return c.now }

type catalogRepo struct {
	reports []application.SaveToolCatalogInput
	reads   []application.ReadToolCatalogInput
	fail    bool
}

func (r *catalogRepo) SaveToolCatalog(_ context.Context, in application.SaveToolCatalogInput) error {
	if r.fail {
		return errors.New("database unavailable")
	}
	r.reports = append(r.reports, in)
	return nil
}

func (r *catalogRepo) ReadToolCatalog(_ context.Context, in application.ReadToolCatalogInput) (domain.ToolCatalogPage, error) {
	r.reads = append(r.reads, in)
	return domain.ToolCatalogPage{Tools: []domain.ToolCatalogEntry{}, Sources: []domain.ToolCatalogSource{}}, nil
}

// scriptedSource stands in for an edition's tool source: one owner at a
// version the test moves, answering discovery from a script.
type scriptedSource struct {
	version    string
	calls      int
	fail       bool
	visibility []application.ToolSourceVisibility
}

func (g *scriptedSource) Targets(_ context.Context, scope application.ToolSourceScope) ([]application.ToolSourceTarget, error) {
	return []application.ToolSourceTarget{{OrganizationID: scope.OrganizationID, SourceRef: "server-1"}}, nil
}

func (g *scriptedSource) State(context.Context, application.ToolSourceTarget) (string, error) {
	return g.version, nil
}

func (g *scriptedSource) Discover(_ context.Context, target application.ToolSourceTarget) (application.SaveToolCatalogInput, error) {
	g.calls++
	snapshot := application.SaveToolCatalogInput{OrganizationID: target.OrganizationID, SourceRef: target.SourceRef, SourceVersion: g.version, Report: domain.ToolCatalogReport{Sources: []domain.ToolCatalogSourceReport{{Source: "gateway", Server: "payments", Complete: !g.fail}}}}
	if g.fail {
		snapshot.Report.Sources[0].Error = "Discovery failed"
		return snapshot, errors.New("discovery failed")
	}
	return snapshot, nil
}

func (g *scriptedSource) Visibility(context.Context, application.ToolSourceScope) ([]application.ToolSourceVisibility, error) {
	return g.visibility, nil
}

func TestExternalSourceRefreshCacheAndRetries(t *testing.T) {
	ctx := context.Background()
	clock := &catalogClock{now: time.Now()}
	repo := &catalogRepo{}
	source := &scriptedSource{version: "revision-one"}
	service := NewService(repo, source, clock, slog.New(slog.NewTextHandler(io.Discard, nil)))
	target := application.ToolSourceTarget{OrganizationID: "org", SourceRef: "server-1"}
	refresh := func(force bool, calls int, wantError bool) {
		t.Helper()
		err := service.refresh(ctx, target, force)
		if (err != nil) != wantError || source.calls != calls {
			t.Fatalf("calls=%d want=%d error=%v", source.calls, calls, err)
		}
	}
	refresh(false, 1, false)
	clock.now = clock.now.Add(4 * time.Minute)
	refresh(false, 1, false)
	refresh(true, 2, false)
	clock.now = clock.now.Add(5 * time.Minute)
	refresh(false, 3, false)
	source.version = "revision-two"
	refresh(false, 4, false)
	source.version = "revision-two;credential-2"
	refresh(false, 5, false)
	source.fail = true
	refresh(true, 6, true)
	if saved := repo.reports[len(repo.reports)-1]; saved.Report.Sources[0].Complete || saved.Report.Sources[0].Error == "" || saved.SourceVersion != "revision-two;credential-2" {
		t.Fatalf("failed source must be recorded as incomplete at the owner's version: %+v", saved)
	}
	refresh(false, 6, false)
	source.fail = false
	refresh(true, 7, false)
	repo.fail = true
	refresh(true, 8, true)
	clock.now = clock.now.Add(5 * time.Minute)
	refresh(false, 9, true)
	repo.fail = false
	refresh(false, 10, false) // Failed persistence must never suppress a retry.
	target.OrganizationID = "another-org"
	refresh(false, 11, false)
}

// A read refreshes the edition's owners, then hands the store the
// edition's live answer on who sees what.
func TestListCarriesTheSourcesVisibilityIntoTheRead(t *testing.T) {
	repo := &catalogRepo{}
	source := &scriptedSource{version: "revision-one", visibility: []application.ToolSourceVisibility{{SourceRef: "server-1", AgentSlug: "billing", Version: "revision-one", Available: true, Tools: []string{"refund"}}}}
	service := NewService(repo, source, &catalogClock{now: time.Now()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	principal := domain.Principal{ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org", MembershipID: "membership-1"}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionAgentRead}))
	if _, err := service.List(context.Background(), principal, Query{AgentSlug: "billing"}); err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 || len(repo.reads) != 1 || len(repo.reads[0].External) != 1 || repo.reads[0].External[0].Tools[0] != "refund" {
		t.Fatalf("read did not carry the visibility: calls=%d reads=%+v", source.calls, repo.reads)
	}
	if _, err := service.List(context.Background(), principal, Query{Source: "Gateway!"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a malformed source filter must be refused, got %v", err)
	}
	instance := domain.Principal{ActorType: domain.ActorAgent, ActorID: "agent-1", AgentInstanceID: "instance-1", OrganizationID: "org"}
	err := service.Report(context.Background(), instance, domain.ToolCatalogReport{Runtime: "codex", DiscoveryContext: "project", Complete: true, Sources: []domain.ToolCatalogSourceReport{{Source: "gateway", Server: "payments", Complete: true}}})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("an instance may not report an edition's source, got %v", err)
	}
}
