package toolpresentations

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/toolpresentation"
)

type fakeRepo struct {
	records []domain.ToolPresentation
	lists   int
	fail    error
}

func (r *fakeRepo) ListToolPresentations(_ context.Context, input application.ListToolPresentationsInput) ([]domain.ToolPresentation, error) {
	r.lists++
	if r.fail != nil {
		return nil, r.fail
	}
	var out []domain.ToolPresentation
	for _, record := range r.records {
		if record.OrganizationID == input.OrganizationID {
			out = append(out, record)
		}
	}
	return out, nil
}

func (r *fakeRepo) GetToolPresentation(context.Context, application.GetToolPresentationInput) (domain.ToolPresentation, error) {
	return domain.ToolPresentation{}, domain.ErrNotFound
}

func (r *fakeRepo) CreateToolPresentation(_ context.Context, input application.CreateToolPresentationInput) (domain.ToolPresentation, error) {
	if err := input.Validate(); err != nil {
		return domain.ToolPresentation{}, err
	}
	record := domain.ToolPresentation{ID: input.ID, OrganizationID: input.OrganizationID, Entry: input.Entry, CreatedAt: input.Now, UpdatedAt: input.Now}
	r.records = append(r.records, record)
	return record, nil
}

func (r *fakeRepo) UpdateToolPresentation(_ context.Context, input application.UpdateToolPresentationInput) (domain.ToolPresentation, error) {
	if err := input.Validate(); err != nil {
		return domain.ToolPresentation{}, err
	}
	for index, record := range r.records {
		if record.ID == input.ID && record.OrganizationID == input.OrganizationID {
			r.records[index].Entry = input.Entry
			return r.records[index], nil
		}
	}
	return domain.ToolPresentation{}, domain.ErrNotFound
}

func (r *fakeRepo) DeleteToolPresentation(_ context.Context, input application.DeleteToolPresentationInput) error {
	for index, record := range r.records {
		if record.ID == input.ID && record.OrganizationID == input.OrganizationID {
			r.records = append(r.records[:index], r.records[index+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func principal(permissions ...domain.Permission) domain.Principal {
	return domain.Principal{
		ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1",
	}.WithGrants(domain.OrganizationGrants(permissions))
}

func request(agentSlug, tool string, context map[string]any) domain.ApprovalRequest {
	return serverRequest(agentSlug, "", tool, context)
}

func serverRequest(agentSlug, server, tool string, context map[string]any) domain.ApprovalRequest {
	raw, _ := json.Marshal(context)
	if context == nil {
		raw = nil
	}
	return domain.ApprovalRequest{OrganizationID: "org-1", AgentSlug: domain.AgentSlug(agentSlug), Tool: tool, Server: server, Context: raw}
}

func TestSelectorForReadsTheAdapterContext(t *testing.T) {
	got := SelectorFor(serverRequest("support-bot", "github", "create_issue", map[string]any{"runtime": "claude-code"}))
	want := toolpresentation.Selector{Runtime: "claude-code", Server: "github", Tool: "create_issue"}
	if got != want {
		t.Fatalf("selector = %+v, want %+v", got, want)
	}
	if got := SelectorFor(request("codex", "Bash", nil)); got != (toolpresentation.Selector{Runtime: "codex", Tool: "Bash"}) {
		t.Fatalf("selector without context = %+v", got)
	}
	if got := SelectorFor(domain.ApprovalRequest{AgentSlug: "pi", Tool: "bash", Context: json.RawMessage(`"not an object"`)}); got != (toolpresentation.Selector{Runtime: "pi", Tool: "bash"}) {
		t.Fatalf("selector with odd context = %+v", got)
	}
}

func TestPresentLayersOrganizationOverTheCatalog(t *testing.T) {
	repo := &fakeRepo{}
	service := NewService(repo, &clock{now: time.Unix(1_000, 0)}, nil)
	ctx := context.Background()

	presentation, ok := service.Present(ctx, request("claude-code", "Bash", nil))
	if !ok || presentation.Source != SourceBuiltIn || presentation.ID != "" || len(presentation.Spec.Fields) == 0 {
		t.Fatalf("built-in Bash = %+v, %v", presentation, ok)
	}
	if presentation.Spec.Fields[0].Role != toolpresentation.RolePrimary {
		t.Fatalf("built-in spec not normalized: %+v", presentation.Spec.Fields[0])
	}
	if _, ok := service.Present(ctx, request("claude-code", "unknown_tool", nil)); ok {
		t.Fatalf("unknown tool presented")
	}

	record, err := service.Create(ctx, principal(domain.PermissionOrganizationSettingsWrite), toolpresentation.Entry{
		Tool: "Bash", Spec: toolpresentation.Spec{Title: "Shell", Fields: []toolpresentation.Field{{Path: "/command", Kind: toolpresentation.KindShell}}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// A tool-only organization entry beats the runtime-bound shipped one.
	presentation, ok = service.Present(ctx, request("claude-code", "Bash", nil))
	if !ok || presentation.Source != SourceOrganization || presentation.ID != record.ID || presentation.Spec.Title != "Shell" {
		t.Fatalf("organization Bash = %+v, %v", presentation, ok)
	}
	if err := service.Delete(ctx, principal(domain.PermissionOrganizationSettingsWrite), record.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if presentation, _ := service.Present(ctx, request("claude-code", "Bash", nil)); presentation.Source != SourceBuiltIn {
		t.Fatalf("after delete = %+v", presentation)
	}
}

func TestPresentCachesReadsAndForgetsOnWrite(t *testing.T) {
	repo := &fakeRepo{}
	now := &clock{now: time.Unix(1_000, 0)}
	service := NewService(repo, now, nil)
	ctx := context.Background()
	present := service.Presenter(ctx, "org-1")
	present(request("claude-code", "Bash", nil))
	present(request("claude-code", "Edit", nil))
	service.Present(ctx, request("claude-code", "Bash", nil))
	if repo.lists != 1 {
		t.Fatalf("lists within the TTL = %d, want 1", repo.lists)
	}
	now.now = now.now.Add(cacheTTL + time.Second)
	service.Present(ctx, request("claude-code", "Bash", nil))
	if repo.lists != 2 {
		t.Fatalf("lists after the TTL = %d, want 2", repo.lists)
	}
	if _, err := service.Create(ctx, principal(domain.PermissionOrganizationSettingsWrite), toolpresentation.Entry{Tool: "x", Spec: toolpresentation.Spec{}}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	service.Present(ctx, request("claude-code", "Bash", nil))
	if repo.lists != 3 {
		t.Fatalf("lists after a write = %d, want 3", repo.lists)
	}
}

func TestPresentSurvivesARepositoryFailure(t *testing.T) {
	repo := &fakeRepo{fail: errors.New("down")}
	service := NewService(repo, &clock{now: time.Unix(1_000, 0)}, nil)
	presentation, ok := service.Present(context.Background(), request("claude-code", "Bash", nil))
	if !ok || presentation.Source != SourceBuiltIn {
		t.Fatalf("presentation with the repository down = %+v, %v", presentation, ok)
	}
}

func TestWritesRequireSettingsWriteAndReadsRequireRead(t *testing.T) {
	service := NewService(&fakeRepo{}, &clock{now: time.Unix(1_000, 0)}, nil)
	ctx := context.Background()
	entry := toolpresentation.Entry{Tool: "x", Spec: toolpresentation.Spec{}}
	if _, err := service.Create(ctx, principal(domain.PermissionOrganizationRead), entry); !errors.Is(err, domain.ErrForbidden) && err == nil {
		t.Fatalf("create with read only = %v", err)
	}
	if _, err := service.List(ctx, principal()); err == nil {
		t.Fatalf("list without read succeeded")
	}
	if _, err := service.List(ctx, domain.Principal{ActorType: domain.ActorAgent, OrganizationID: "org-1"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("agent list = %v", err)
	}
	if _, err := service.List(ctx, principal(domain.PermissionOrganizationRead)); err != nil {
		t.Fatalf("list with read: %v", err)
	}
}
