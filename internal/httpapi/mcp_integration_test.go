package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/authn"
	"github.com/withHumanAI/withHuman/db/dbtest"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/entitlements"
	"github.com/withHumanAI/withHuman/internal/approvalpipeline"
	"github.com/withHumanAI/withHuman/internal/postgres"
	"github.com/withHumanAI/withHuman/internal/reviewrouting"
	"github.com/withHumanAI/withHuman/internal/teams"
)

// The whole chain an agent walks through the management MCP surface,
// against the real store and a real personal API key: a team, an
// escalation path targeting it, the entry pipeline naming the
// path, a preview through the active blocks, and the in-use refusal when
// the path is archived underneath the pipeline.
func TestMCPSetsUpAnApprovalFlowEndToEnd(t *testing.T) {
	ctx := context.Background()
	store, err := postgres.Open(ctx, dbtest.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := application.RealClock{}
	now := clock.Now()
	if _, err := store.ReconcileBuiltInRoles(ctx, application.ReconcileBuiltInRolesInput{Roles: domain.BuiltInRoles(), Permissions: domain.PermissionKeys(), Now: now}); err != nil {
		t.Fatal(err)
	}
	org, user, member := domain.ID(uuid.NewString()), domain.ID(uuid.NewString()), domain.ID(uuid.NewString())
	if _, err = store.ActivateOrganization(ctx, application.ActivateOrganizationInput{OrganizationID: org, OrganizationSlug: "mcp", OrganizationName: "MCP", UserID: user, MembershipID: member, DisplayName: "Owner", Email: "owner@example.com", PasswordHash: "test", AdmissionSource: domain.AdmissionManual, Now: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAgent(ctx, application.CreateAgentInput{Kind: domain.AgentService, OrganizationID: org, ActorID: member, Name: "Support assistant", AgentSlug: "support", AgentQuota: application.UnlimitedQuota(), Now: now}); err != nil {
		t.Fatal(err)
	}
	auth := authn.NewService(store, store, store, nil, entitlements.NewUnlimited(), nil, clock, authn.ServiceConfig{SessionTTL: time.Hour, APIKeyMaxTTL: 90 * 24 * time.Hour, APIKeyScopingEnabled: true})
	auth.RegisterRoles(store)
	auth.RegisterAPIKeys(store)
	owner := domain.Principal{
		ActorType: domain.ActorHuman, ActorID: user, OrganizationID: org, MembershipID: member,
		Auth: domain.AuthContext{Method: "password", Assurance: "strong", AuthenticatedAt: now, SessionID: "session-1"},
	}.WithGrants(domain.GrantsForRoles([]domain.Role{domain.RoleOwner}))
	_, token, err := auth.CreateAPIKey(ctx, owner, authn.CreateAPIKeyRequest{Name: "setup agent", Permissions: []domain.Permission{
		domain.PermissionPipelineRead, domain.PermissionPipelineWrite, domain.PermissionPipelineActivate,
		domain.PermissionEscalationPathRead, domain.PermissionEscalationPathWrite, domain.PermissionEscalationPathActivate,
		domain.PermissionTeamRead, domain.PermissionTeamWrite, domain.PermissionTeamMemberWrite, domain.PermissionAgentRead,
	}})
	if err != nil {
		t.Fatal(err)
	}
	registry := approvalpipeline.NewBlockRegistry(approvalpipeline.WithClock(clock))
	compiled := approvalpipeline.NewCompiledPipelineState(store, registry)
	if err := compiled.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := NewRouter(Dependencies{
		Auth: auth, Identities: store, Roles: store, ApprovalRepo: store, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Teams:     teams.NewService(store, clock),
		Pipelines: approvalpipeline.NewManagementService(store, compiled, clock, approvalpipeline.WithEscalationPathResolver(store), approvalpipeline.WithPipelineKeyResolver(store)),
		Routing:   reviewrouting.NewManagementService(store, store, clock),
		Config:    Config{PublicOrigin: "http://localhost:8080", Version: "test"},
	})
	server := httptest.NewServer(router)
	defer server.Close()
	h := &mcpHarness{server: server, token: token}
	session := h.connect(t, token)

	agents := mcpStructured(t, h.call(t, session, "list_agents", nil))
	if listed := agents["agents"].([]any); len(listed) != 1 || listed[0].(map[string]any)["slug"] != "support" {
		t.Fatalf("list_agents = %v", agents)
	}

	created := mcpStructured(t, h.call(t, session, "create_team", map[string]any{"name": "Payments", "membership_ids": []any{string(member)}}))
	teamID := created["team"].(map[string]any)["id"].(string)
	if members := created["members"].([]any); len(members) != 1 {
		t.Fatalf("create_team members = %v", created)
	}

	path := mcpStructured(t, h.call(t, session, "create_escalation_path", map[string]any{
		"path_key": "payments-review", "name": "Payments review",
		"nodes": []any{map[string]any{
			"id": uuid.NewString(), "type": "level", "escalate_after": "15m",
			"targets": []any{map[string]any{"type": "team", "id": teamID}},
		}},
		"repeat": map[string]any{"times": 1, "after": "30m"},
	}))
	if path["activated"] != true || path["revision"].(map[string]any)["is_active"] != true {
		t.Fatalf("create_escalation_path = %v", path)
	}

	// The pipeline names the path in a block and as its default; the
	// composition activates it, which the store only allows because the
	// path is active.
	condition := map[string]any{"id": uuid.NewString(), "field": "/request/tool", "operator": "equals", "value": "stripe__refund"}
	pipeline := mcpStructured(t, h.call(t, session, "update_pipeline", map[string]any{
		"pipeline_key": "entry",
		"blocks": []any{
			map[string]any{
				"id": "refunds", "name": "Refunds need Payments", "type": "branch", "enabled": true, "timeout": "50ms", "max_attempts": 1,
				"config": map[string]any{"when": condition, "reason": "", "blocks": []any{map[string]any{
					"id": "ask-payments", "name": "Ask Payments", "type": "outcome", "enabled": true, "timeout": "50ms", "max_attempts": 1,
					"config": map[string]any{"outcome": "human", "reason": "Payments checks every refund.", "escalation_path": "payments-review"},
				}}},
			},
			map[string]any{
				"id": "rest", "name": "Approve the rest", "type": "outcome", "enabled": true, "timeout": "50ms", "max_attempts": 1,
				"config": map[string]any{"outcome": "approve", "reason": "Everything else is routine."},
			},
		},
		"default_escalation_path": "payments-review",
	}))
	revision := pipeline["revision"].(map[string]any)
	if pipeline["activated"] != true || revision["revision"] != float64(2) || revision["is_active"] != true || revision["default_escalation_path"] != "payments-review" {
		t.Fatalf("update_pipeline = %v", pipeline)
	}

	preview := mcpStructured(t, h.call(t, session, "preview_pipeline", map[string]any{
		"pipeline_key": "entry", "blocks": revision["blocks"], "default_escalation_path": "payments-review",
		"sample": map[string]any{"tool": "stripe__refund", "arguments": map[string]any{"amount": 75}},
	}))
	if preview["outcome"] != "human" || preview["escalation_path"] != "payments-review" || preview["terminal_block_id"] != "ask-payments" {
		t.Fatalf("preview = %v", preview)
	}
	passthrough := mcpStructured(t, h.call(t, session, "preview_pipeline", map[string]any{
		"pipeline_key": "entry", "blocks": revision["blocks"], "sample": map[string]any{"tool": "Read", "arguments": map[string]any{}},
	}))
	if passthrough["outcome"] != "approve" || passthrough["terminal_block_id"] != "rest" {
		t.Fatalf("an unmatched request should reach the outcome block: %v", passthrough)
	}

	current := mcpStructured(t, h.call(t, session, "get_pipeline", map[string]any{"pipeline_key": "entry"}))
	if current["revision"] != float64(2) {
		t.Fatalf("get_pipeline = %v", current)
	}
	paths := mcpStructured(t, h.call(t, session, "list_escalation_paths", nil))
	if listed := paths["escalation_paths"].([]any); len(listed) != 1 || listed[0].(map[string]any)["active_revision"] != float64(1) {
		t.Fatalf("list_escalation_paths = %v", paths)
	}

	// Archiving is refused while the active pipeline names the path, and
	// the agent hears the same code a browser would.
	refused := h.call(t, session, "archive_escalation_path", map[string]any{"path_key": "payments-review"})
	if !refused.IsError || !strings.HasPrefix(mcpText(refused), "escalation_path_in_use: ") {
		t.Fatalf("archive while in use = %q", mcpText(refused))
	}
	// Rolling the pipeline back to its empty revision frees the path.
	if rolled := h.call(t, session, "activate_pipeline_revision", map[string]any{"pipeline_key": "entry", "revision": 1}); rolled.IsError {
		t.Fatalf("rollback: %s", mcpText(rolled))
	}
	if archived := h.call(t, session, "archive_escalation_path", map[string]any{"path_key": "payments-review"}); archived.IsError {
		t.Fatalf("archive after rollback: %s", mcpText(archived))
	}
}
