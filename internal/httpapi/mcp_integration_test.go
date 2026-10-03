package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/authn"
	"github.com/withHumanAI/withHuman/db/dbtest"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/entitlements"
	"github.com/withHumanAI/withHuman/internal/approvalpipeline"
	"github.com/withHumanAI/withHuman/internal/postgres"
	"github.com/withHumanAI/withHuman/internal/reviewrouting"
	"github.com/withHumanAI/withHuman/internal/secrets"
	"github.com/withHumanAI/withHuman/internal/teamescalation"
	"github.com/withHumanAI/withHuman/internal/teams"
	"github.com/withHumanAI/withHuman/internal/webhookdelivery"
	"github.com/withHumanAI/withHuman/internal/webhookendpoints"
)

// newMCPStoreSession composes the management MCP surface over the real
// store: an organization whose owner holds a personal API key with the
// permissions given, a service agent named support, and a connected
// session. It answers the session and the owner's membership id.
func newMCPStoreSession(t *testing.T, permissions ...domain.Permission) (*mcpHarness, *mcp.ClientSession, domain.ID) {
	t.Helper()
	ctx := context.Background()
	store, err := postgres.Open(ctx, dbtest.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
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
	auth := authn.NewService(store, store, store, nil, entitlements.NewUnlimited(), nil, clock, authn.ServiceConfig{SessionTTL: time.Hour, CredentialTTL: 24 * time.Hour, ProvisionerTTL: 24 * time.Hour, APIKeyMaxTTL: 90 * 24 * time.Hour, APIKeyScopingEnabled: true})
	auth.RegisterRoles(store)
	auth.RegisterAPIKeys(store)
	owner := domain.Principal{
		ActorType: domain.ActorHuman, ActorID: user, OrganizationID: org, MembershipID: member,
		Auth: domain.AuthContext{Method: "password", Assurance: "strong", AuthenticatedAt: now, SessionID: "session-1"},
	}.WithGrants(domain.GrantsForRoles([]domain.Role{domain.RoleOwner}))
	_, token, err := auth.CreateAPIKey(ctx, owner, authn.CreateAPIKeyRequest{Name: "setup agent", Permissions: permissions})
	if err != nil {
		t.Fatal(err)
	}
	registry := approvalpipeline.NewBlockRegistry(approvalpipeline.WithClock(clock))
	compiled := approvalpipeline.NewCompiledPipelineState(store, registry)
	if err := compiled.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	sealer, err := secrets.NewSealer("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	store.WithSealer(sealer)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	gin.SetMode(gin.TestMode)
	router := NewRouter(Dependencies{
		Auth: auth, Identities: store, Roles: store, ApprovalRepo: store, Logger: logger,
		Teams:            teams.NewService(store, clock),
		TeamEscalation:   teamescalation.NewService(store, store, clock),
		Pipelines:        approvalpipeline.NewManagementService(store, compiled, clock, approvalpipeline.WithEscalationPathResolver(store), approvalpipeline.WithPipelineKeyResolver(store)),
		Routing:          reviewrouting.NewManagementService(store, store, clock),
		WebhookEndpoints: webhookendpoints.NewService(store, sealer, webhookdelivery.Policy{}, webhookHTTPDeliverer{}, clock, logger),
		Config:           Config{PublicOrigin: "http://localhost:8080", Version: "test"},
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	h := &mcpHarness{server: server, token: token}
	session := h.connect(t, token)
	return h, session, member
}

// The whole chain an agent walks through the management MCP surface,
// against the real store and a real personal API key: a team, an
// escalation path targeting it, the entry pipeline naming the
// path, a preview through the active blocks, and the in-use refusal when
// the path is archived underneath the pipeline.
func TestMCPSetsUpAnApprovalFlowEndToEnd(t *testing.T) {
	h, session, member := newMCPStoreSession(t,
		domain.PermissionPipelineRead, domain.PermissionPipelineWrite, domain.PermissionPipelineActivate,
		domain.PermissionEscalationPathRead, domain.PermissionEscalationPathWrite, domain.PermissionEscalationPathActivate,
		domain.PermissionTeamRead, domain.PermissionTeamWrite, domain.PermissionTeamMemberWrite, domain.PermissionAgentRead,
		domain.PermissionTeamEscalationWrite, domain.PermissionTeamEscalationActivate,
	)

	agents := mcpStructured(t, h.call(t, session, "list_agents", nil))
	if listed := agents["agents"].([]any); len(listed) != 1 || listed[0].(map[string]any)["slug"] != "support" {
		t.Fatalf("list_agents = %v", agents)
	}

	created := mcpStructured(t, h.call(t, session, "create_team", map[string]any{"name": "Payments", "membership_ids": []any{string(member)}}))
	teamID := created["team"].(map[string]any)["id"].(string)
	if members := created["members"].([]any); len(members) != 1 {
		t.Fatalf("create_team members = %v", created)
	}

	renamed := mcpStructured(t, h.call(t, session, "rename_team", map[string]any{"team_id": teamID, "name": "Payments ops"}))
	if renamed["team"].(map[string]any)["name"] != "Payments ops" {
		t.Fatalf("rename_team = %v", renamed)
	}

	// The team's own policy: saved and activated against "0", then a
	// draft rolled forward and the policy archived, every If-Match read
	// from the current state.
	level := func() map[string]any {
		return map[string]any{"id": uuid.NewString(), "type": "level", "escalate_after": "15m", "targets": []any{map[string]any{"type": "user", "id": string(member)}}}
	}
	policy := mcpStructured(t, h.call(t, session, "update_team_escalation_policy", map[string]any{"team_id": teamID, "nodes": []any{level()}}))
	if policy["activated"] != true || policy["revision"].(map[string]any)["revision"] != float64(1) {
		t.Fatalf("update_team_escalation_policy = %v", policy)
	}
	draft := mcpStructured(t, h.call(t, session, "update_team_escalation_policy", map[string]any{"team_id": teamID, "nodes": []any{map[string]any{
		"id": uuid.NewString(), "type": "level", "escalate_after": "5m", "targets": []any{map[string]any{"type": "broadcast"}},
	}}, "activate": false}))
	if draft["activated"] != false || draft["revision"].(map[string]any)["revision"] != float64(2) {
		t.Fatalf("draft policy = %v", draft)
	}
	if active := mcpStructured(t, h.call(t, session, "get_team_escalation_policy", map[string]any{"team_id": teamID})); active["revision"] != float64(1) {
		t.Fatalf("a draft must not activate: %v", active)
	}
	if rolled := mcpStructured(t, h.call(t, session, "activate_team_escalation_policy_revision", map[string]any{"team_id": teamID, "revision": 2})); rolled["revision"] != float64(2) {
		t.Fatalf("activate_team_escalation_policy_revision = %v", rolled)
	}
	if historical := mcpStructured(t, h.call(t, session, "get_team_escalation_policy_revision", map[string]any{"team_id": teamID, "revision": 1})); historical["revision"] != float64(1) {
		t.Fatalf("get_team_escalation_policy_revision = %v", historical)
	}
	mcpStructured(t, h.call(t, session, "archive_team_escalation_policy", map[string]any{"team_id": teamID}))
	if gone := h.call(t, session, "get_team_escalation_policy", map[string]any{"team_id": teamID}); !gone.IsError || !strings.HasPrefix(mcpText(gone), "not_found: ") {
		t.Fatalf("archived policy still reads: %q", mcpText(gone))
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

	// The uses and references reads explain the refusal before it happens.
	uses := mcpStructured(t, h.call(t, session, "list_escalation_path_uses", map[string]any{"path_key": "payments-review"}))
	if encoded, _ := json.Marshal(uses); !strings.Contains(string(encoded), `"entry"`) {
		t.Fatalf("list_escalation_path_uses = %s", encoded)
	}
	references := mcpStructured(t, h.call(t, session, "get_pipeline_revision_references", map[string]any{"pipeline_key": "entry", "revision": 2}))
	if encoded, _ := json.Marshal(references); !strings.Contains(string(encoded), "payments-review") {
		t.Fatalf("get_pipeline_revision_references = %s", encoded)
	}
	if first := mcpStructured(t, h.call(t, session, "get_escalation_path_revision", map[string]any{"path_key": "payments-review", "revision": 1})); first["revision"] != float64(1) {
		t.Fatalf("get_escalation_path_revision = %v", first)
	}
	if delegators := mcpStructured(t, h.call(t, session, "list_pipeline_uses", map[string]any{"pipeline_key": "entry"})); delegators == nil {
		t.Fatal("list_pipeline_uses answered nothing")
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

// Agents and webhook endpoints through the catalog: an agent renamed,
// disabled and given a credential, archived and restored; an endpoint
// saved, rolled back, tested, archived, brought back and its secret
// rotated, each If-Match read from the current state.
func TestMCPManagesAgentsAndWebhookEndpointsEndToEnd(t *testing.T) {
	h, session, _ := newMCPStoreSession(t,
		domain.PermissionAgentRead, domain.PermissionAgentWrite, domain.PermissionAgentCredentialIssue, domain.PermissionAgentCredentialRevoke,
		domain.PermissionWebhookRead, domain.PermissionWebhookWrite,
	)

	updated := mcpStructured(t, h.call(t, session, "update_agent", map[string]any{"agent_slug": "support", "name": "Support bot", "status": "disabled"}))
	if encoded, _ := json.Marshal(updated); !strings.Contains(string(encoded), `"Support bot"`) || !strings.Contains(string(encoded), `"disabled"`) {
		t.Fatalf("update_agent = %s", encoded)
	}
	mcpStructured(t, h.call(t, session, "update_agent", map[string]any{"agent_slug": "support", "status": "active"}))
	instance := mcpStructured(t, h.call(t, session, "create_agent_instance", map[string]any{"agent_slug": "support", "name": "eu-west"}))
	if encoded, _ := json.Marshal(instance); !strings.Contains(string(encoded), `"eu-west"`) {
		t.Fatalf("create_agent_instance = %s", encoded)
	}
	agent := mcpStructured(t, h.call(t, session, "get_agent", map[string]any{"agent_slug": "support", "instance_q": "eu"}))
	if encoded, _ := json.Marshal(agent); !strings.Contains(string(encoded), `"eu-west"`) {
		t.Fatalf("get_agent = %s", encoded)
	}
	created := mcpStructured(t, h.call(t, session, "create_agent", map[string]any{"slug": "laptop-assistant", "name": "Laptop assistant", "kind": "personal"}))
	if encoded, _ := json.Marshal(created); !strings.Contains(string(encoded), `"personal"`) {
		t.Fatalf("create_agent with a kind = %s", encoded)
	}
	mcpStructured(t, h.call(t, session, "archive_agent", map[string]any{"agent_slug": "laptop-assistant"}))
	if refused := h.call(t, session, "update_agent", map[string]any{"agent_slug": "laptop-assistant", "status": "disabled"}); !refused.IsError || !strings.HasPrefix(mcpText(refused), "agent_archived: ") {
		t.Fatalf("switching an archived agent = %q", mcpText(refused))
	}
	mcpStructured(t, h.call(t, session, "restore_agent", map[string]any{"agent_slug": "laptop-assistant"}))

	saved := mcpStructured(t, h.call(t, session, "save_webhook_endpoint", map[string]any{"endpoint_key": "fraud", "name": "Fraud check", "url": "https://fraud.example.com/v1"}))
	if saved["activated"] != true || saved["signing_secret"] == nil {
		t.Fatalf("save_webhook_endpoint = %v", saved)
	}
	moved := mcpStructured(t, h.call(t, session, "save_webhook_endpoint", map[string]any{"endpoint_key": "fraud", "name": "Fraud check", "url": "https://fraud.example.com/v2"}))
	if moved["signing_secret"] != nil || moved["revision"].(map[string]any)["revision"] != float64(2) {
		t.Fatalf("a new revision must keep the secret: %v", moved)
	}
	if revisions := mcpStructured(t, h.call(t, session, "list_webhook_endpoint_revisions", map[string]any{"endpoint_key": "fraud"})); len(revisions["revisions"].([]any)) != 2 {
		t.Fatalf("list_webhook_endpoint_revisions = %v", revisions)
	}
	if rolled := mcpStructured(t, h.call(t, session, "activate_webhook_endpoint_revision", map[string]any{"endpoint_key": "fraud", "revision": 1})); rolled["url"] != "https://fraud.example.com/v1" {
		t.Fatalf("rollback = %v", rolled)
	}
	tested := mcpStructured(t, h.call(t, session, "test_webhook_endpoint", map[string]any{"endpoint_key": "fraud"}))
	if tested["test"] == nil {
		t.Fatalf("test_webhook_endpoint = %v", tested)
	}
	if uses := mcpStructured(t, h.call(t, session, "list_webhook_endpoint_uses", map[string]any{"endpoint_key": "fraud"})); uses == nil {
		t.Fatal("list_webhook_endpoint_uses answered nothing")
	}
	mcpStructured(t, h.call(t, session, "archive_webhook_endpoint", map[string]any{"endpoint_key": "fraud"}))
	if gone := h.call(t, session, "get_webhook_endpoint", map[string]any{"endpoint_key": "fraud"}); !gone.IsError {
		t.Fatal("an archived endpoint has no active revision to read")
	}
	if untested := h.call(t, session, "test_webhook_endpoint", map[string]any{"endpoint_key": "fraud"}); !untested.IsError || !strings.Contains(mcpText(untested), "no active revision") {
		t.Fatalf("testing an archived endpoint without a revision = %q", mcpText(untested))
	}
	if back := mcpStructured(t, h.call(t, session, "activate_webhook_endpoint_revision", map[string]any{"endpoint_key": "fraud", "revision": 2})); back["is_active"] != true {
		t.Fatalf("activating after archive = %v", back)
	}
	rotated := mcpStructured(t, h.call(t, session, "rotate_webhook_endpoint_secret", map[string]any{"endpoint_key": "fraud"}))
	if rotated["signing_secret"] == nil || rotated["signing_secret"] == saved["signing_secret"] {
		t.Fatalf("rotate_webhook_endpoint_secret = %v", rotated)
	}
}
