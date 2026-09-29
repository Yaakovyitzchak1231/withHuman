package approvalpipeline

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/domain"
)

// delegateDefinition is a delegate block to pipelineKey for the requests
// condition matches.
func delegateDefinition(id, pipelineKey string, condition approvalblock.Condition) approvalblock.Definition {
	condition.ID = uuid.NewString()
	return *approvalblock.DefineDelegate(id, pipelineKey, approvalblock.Name(id), approvalblock.When(condition))
}

func alwaysDefinition(id string, outcome approvalblock.Outcome) approvalblock.Definition {
	return *approvalblock.DefineAlways(id, outcome, approvalblock.Name(id), approvalblock.Reason("Every remaining request."))
}

func TestAlwaysExecutionAndPreview(t *testing.T) {
	for _, outcome := range []approvalblock.Outcome{approvalblock.Approve, approvalblock.Deny, approvalblock.Human} {
		t.Run(string(outcome), func(t *testing.T) {
			now := time.Unix(1000, 0).UTC()
			always := alwaysDefinition("remaining", outcome)
			if outcome == approvalblock.Human {
				always.Always.EscalationPath = "payments"
			}
			definitions := []approvalblock.Definition{
				pipelineDefinition("skip", approvalblock.Deny, approvalblock.Field("/request/tool").Equals("other")),
				always, alwaysDefinition("unreachable", approvalblock.Deny),
			}
			repository := activeExecutionPipeline(t, now, "entry", definitions)
			service := initializedExecutionService(t, repository, executionAgentRepositoryStub{agent: domain.Agent{ID: "agent-1", OrganizationID: "organization-1", Name: "Billing", AgentSlug: "billing"}}, fixedPipelineClock{now: now})
			result, err := service.Evaluate(context.Background(), executionRequest(now, "request-1", "key-1", "billing", json.RawMessage(`{}`)))
			if err != nil {
				t.Fatal(err)
			}
			var evaluations []map[string]any
			for _, event := range result.AuditEvents {
				if event.EventType == blockEvaluatedEvent {
					evaluations = append(evaluations, auditData(t, event))
				}
				if event.EventType == pipelineCompletedEvent {
					data := auditData(t, event)
					if data["terminal_block_key"] != "remaining" || data["outcome"] != string(outcome) {
						t.Fatalf("wrong terminal block: %+v", data)
					}
				}
			}
			if len(evaluations) != 2 || evaluations[1]["block_type"] != "always" || evaluations[1]["reason"] != "Every remaining request." {
				t.Fatalf("wrong execution evidence: %+v", evaluations)
			}
			if outcome == approvalblock.Human {
				if result.Decision != nil || result.HumanReview == nil || result.HumanReview.PathKey != "payments" || result.HumanReview.BlockID == "" || result.HumanReview.Reason != domain.RoutingReasonBlockEscalation {
					t.Fatalf("wrong human route: %+v", result)
				}
			} else if result.Decision == nil || result.Decision.Source != domain.DecisionSourcePipeline || (outcome == approvalblock.Approve && result.Request.Status != domain.ApprovalApproved) || (outcome == approvalblock.Deny && result.Request.Status != domain.ApprovalDenied) {
				t.Fatalf("wrong decision: %+v", result)
			}
			management := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: now})
			preview, err := management.Preview(context.Background(), pipelineManagerPrincipal("organization-1"), "billing", definitions, Route{}, PreviewSample{Request: BlockRequest{Tool: "deploy", Arguments: json.RawMessage(`{}`)}, Agent: BlockAgent{Slug: "billing", Name: "Billing"}})
			if err != nil {
				t.Fatal(err)
			}
			if preview.Outcome != blockOutcome(outcome) || preview.TerminalBlockKey != "remaining" || len(preview.Blocks) != 2 || len(preview.Blocks[1].Evidence.Trace) != 0 {
				t.Fatalf("wrong preview: %+v", preview)
			}
		})
	}
}

func TestEarlierDecisionOrErrorStopsBeforeAlways(t *testing.T) {
	for _, first := range []approvalblock.Definition{
		pipelineDefinition("deny", approvalblock.Deny, approvalblock.Field("/request/tool").Equals("deploy")),
		pipelineDefinition("error", approvalblock.Approve, approvalblock.Field("/request/arguments/amount").LessThan(20)),
	} {
		t.Run(first.ID, func(t *testing.T) {
			repository := newMemoryPipelineRepository()
			service := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: time.Unix(1000, 0).UTC()})
			result, err := service.Preview(context.Background(), pipelineManagerPrincipal("organization-1"), "billing", []approvalblock.Definition{first, alwaysDefinition("remaining", approvalblock.Approve)}, Route{}, PreviewSample{Request: BlockRequest{Tool: "deploy", Arguments: json.RawMessage(`{"amount":"invalid"}`)}, Agent: BlockAgent{Slug: "billing", Name: "Billing"}})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Blocks) != 1 || result.TerminalBlockKey != first.ID || result.Outcome == BlockApprove {
				t.Fatalf("continued past terminal result: %+v", result)
			}
		})
	}
}

func TestAlwaysInTheEntryPipelinePreemptsADelegate(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	repository := activeExecutionPipeline(t, now, "billing", []approvalblock.Definition{alwaysDefinition("billing-denies", approvalblock.Deny)})
	management := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: now})
	created, err := management.CreateRevision(context.Background(), pipelineManagerPrincipal("organization-1"), "entry", pipelineName("entry"), []approvalblock.Definition{
		alwaysDefinition("entry-approves", approvalblock.Approve),
		delegateDefinition("to-billing", "billing", approvalblock.Field("/agent/slug").Equals("billing")),
	}, Route{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := management.Activate(context.Background(), pipelineManagerPrincipal("organization-1"), "entry", created.Revision.Revision, 1); err != nil {
		t.Fatal(err)
	}
	service := initializedExecutionService(t, repository, executionAgentRepositoryStub{agent: domain.Agent{ID: "agent-1", OrganizationID: "organization-1", Name: "Billing", AgentSlug: "billing"}}, fixedPipelineClock{now: now})
	result, err := service.Evaluate(context.Background(), executionRequest(now, "request-1", "key-1", "billing", json.RawMessage(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	if result.Request.Status != domain.ApprovalApproved {
		t.Fatalf("the entry pipeline did not decide: %+v", result)
	}
	for _, event := range result.AuditEvents {
		if event.EventType == blockEvaluatedEvent && auditData(t, event)["block_key"] != "entry-approves" {
			t.Fatalf("a block after the always ran: %s", event.Data)
		}
	}
}

func TestAlwaysAfterAsyncResume(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	check := newScripted(scriptedOutcome(BlockNext))
	repository := newMemoryPipelineRepository()
	registry := NewBlockRegistry(withScripted(check))
	management := NewManagementService(repository, NewCompiledPipelineState(repository, registry), fixedPipelineClock{now: now})
	created, err := management.CreateRevision(context.Background(), pipelineManagerPrincipal("organization-1"), "entry", pipelineName("entry"), []approvalblock.Definition{
		scriptedDefinition(),
		alwaysDefinition("remaining", approvalblock.Deny), alwaysDefinition("unreachable", approvalblock.Approve),
	}, Route{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := management.Activate(context.Background(), pipelineManagerPrincipal("organization-1"), "entry", created.Revision.Revision, 1); err != nil {
		t.Fatal(err)
	}
	compiled := NewCompiledPipelineState(repository, registry)
	if err := compiled.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	service := NewExecutionService(repository, executionAgentRepositoryStub{agent: domain.Agent{ID: "agent-1", OrganizationID: "organization-1", Name: "Billing", AgentSlug: "billing"}}, compiled, fixedPipelineClock{now: now})
	initial, err := service.Evaluate(context.Background(), executionRequest(now, "request-1", "key-1", "billing", json.RawMessage(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	if initial.Suspension == nil {
		t.Fatal("the asynchronous block did not suspend")
	}
	resumed, err := service.Resume(context.Background(), initial.Request, *initial.Suspension)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Decision == nil || resumed.Decision.Status != domain.ApprovalDenied || len(check.Inputs) != 1 {
		t.Fatalf("Always did not decide after resume: %+v", resumed)
	}
	if len(resumed.AuditEvents) != 4 || auditData(t, resumed.AuditEvents[1])["block_key"] != "remaining" {
		t.Fatalf("wrong resumed events: %+v", resumed.AuditEvents)
	}
}

func TestAlwaysReferencesAndUnreachableValidation(t *testing.T) {
	repository := newMemoryPipelineRepository()
	resolver := &stubEscalationPathResolver{resolved: application.ResolvedEscalationPaths{Missing: []string{"payments"}}}
	service := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: time.Unix(1000, 0).UTC()}, WithEscalationPathResolver(resolver))
	block := alwaysDefinition("remaining", approvalblock.Human)
	block.Always.EscalationPath = "payments"
	principal := pipelineManagerPrincipal("organization-1")
	if _, err := service.CreateRevision(context.Background(), principal, "billing", pipelineName("billing"), []approvalblock.Definition{block}, Route{}); err == nil {
		t.Fatal("missing path accepted")
	}
	resolver.resolved = application.ResolvedEscalationPaths{}
	created, err := service.CreateRevision(context.Background(), principal, "billing", pipelineName("billing"), []approvalblock.Definition{block}, Route{})
	if err != nil {
		t.Fatal(err)
	}
	resolver.resolved.Inactive = []string{"payments"}
	refs, err := service.References(context.Background(), principal, "billing", created.Revision.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if refs.Activatable || len(refs.EscalationPaths) != 1 || refs.EscalationPaths[0].BlockKeys[0] != "remaining" {
		t.Fatalf("path dependency lost: %+v", refs)
	}
	invalid := alwaysDefinition("unreachable", approvalblock.Next)
	if _, err := service.CreateRevision(context.Background(), principal, "billing", pipelineName("billing"), []approvalblock.Definition{block, invalid}, Route{}); err == nil {
		t.Fatal("invalid unreachable block accepted")
	}
}

func TestAlwaysFailsClosedOnCancelledContext(t *testing.T) {
	block, err := compileAlwaysBlock(alwaysDefinition("remaining", approvalblock.Approve))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := ExecuteRequestApprovalBlock(ctx, block, RequestApprovalBlockInput{})
	if result.Outcome != BlockHuman || result.ErrorCode != ErrorTimeout {
		t.Fatalf("cancelled request approved: %+v", result)
	}
}
