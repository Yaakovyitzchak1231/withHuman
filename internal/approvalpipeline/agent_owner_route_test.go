package approvalpipeline

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/domain"
)

// A branch that asks the agent's owner hands its end to the owner with its
// own path as the fallback, and the flag survives storage: the fixture
// compiles the revision from its stored snapshots.
func TestABranchEndCanAskTheAgentOwner(t *testing.T) {
	now := time.Unix(7_000, 0).UTC()
	branch := refundsBranch(pipelineDefinition("never", approvalblock.Approve, approvalblock.Field("/request/tool").Equals("other")))
	branch.Branch.AskAgentOwner = true
	service, _ := branchFixture(t, now, NewBlockRegistry(), branchScope{slug: "billing", definitions: []approvalblock.Definition{branch}})
	input, err := service.Evaluate(context.Background(), branchRequest(now, "stripe.refund", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if route := input.HumanReview; route == nil || !route.AskAgentOwner || route.PathKey != "payments-review" ||
		route.Reason != domain.RoutingReasonBlockEscalation {
		t.Fatalf("the branch's owner route must take its end: %#v", route)
	}
}

// The flag travels with the path beside it: a block naming its own path
// never inherits the default's owner route, and a block naming none
// inherits the default route whole.
func TestTheOwnerRouteTravelsWithItsPath(t *testing.T) {
	now := time.Unix(7_000, 0).UTC()
	ownPath := pipelineDefinition("own-path", approvalblock.Human, approvalblock.Field("/request/tool").Equals("deploy"))
	ownPath.CEL.EscalationPath = "release-managers"
	inherits := pipelineDefinition("inherits", approvalblock.Human, approvalblock.Field("/request/tool").Equals("push"))
	service, _ := branchFixture(t, now, NewBlockRegistry(), branchScope{
		slug: "billing", defaultPath: "on-call", askOwner: true,
		definitions: []approvalblock.Definition{ownPath, inherits},
	})

	deploy, err := service.Evaluate(context.Background(), branchRequest(now, "deploy", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if route := deploy.HumanReview; route == nil || route.AskAgentOwner || route.PathKey != "release-managers" {
		t.Fatalf("a block's own path must not ask the owner: %#v", route)
	}
	push, err := service.Evaluate(context.Background(), branchRequest(now, "push", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if route := push.HumanReview; route == nil || !route.AskAgentOwner || route.PathKey != "on-call" ||
		route.Reason != domain.RoutingReasonPipelineDefault {
		t.Fatalf("a block without a path must inherit the default owner route: %#v", route)
	}
	fallThrough, err := service.Evaluate(context.Background(), branchRequest(now, "read", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if route := fallThrough.HumanReview; route == nil || !route.AskAgentOwner || route.PathKey != "on-call" {
		t.Fatalf("a request that falls through must take the default owner route: %#v", route)
	}
}

func TestPreviewReportsTheOwnerRoute(t *testing.T) {
	repository := newMemoryPipelineRepository()
	management := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: time.Unix(7_000, 0).UTC()})
	sample := PreviewSample{Request: BlockRequest{Tool: "push", Arguments: json.RawMessage(`{}`)}, Agent: BlockAgent{Slug: "billing", Name: "Billing"}}
	asks := pipelineDefinition("asks", approvalblock.Human, approvalblock.Field("/request/tool").Equals("push"))
	asks.CEL.EscalationPath, asks.CEL.AskAgentOwner = "platform", true

	result, err := management.Preview(context.Background(), pipelineManagerPrincipal("organization-1"), "billing",
		[]approvalblock.Definition{asks}, Route{EscalationPath: "on-call"}, sample)
	if err != nil {
		t.Fatal(err)
	}
	if !result.AskAgentOwner || result.EscalationPath != "platform" || result.EscalationPathReason != domain.RoutingReasonBlockEscalation {
		t.Fatalf("preview = %#v", result)
	}

	result, err = management.Preview(context.Background(), pipelineManagerPrincipal("organization-1"), "billing",
		nil, Route{EscalationPath: "on-call", AskAgentOwner: true}, sample)
	if err != nil {
		t.Fatal(err)
	}
	if !result.AskAgentOwner || result.EscalationPath != "on-call" || result.EscalationPathReason != domain.RoutingReasonPipelineDefault {
		t.Fatalf("preview of the default = %#v", result)
	}
}

func TestAnOwnerRouteRequiresItsFallback(t *testing.T) {
	repository := newMemoryPipelineRepository()
	management := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry()), fixedPipelineClock{now: time.Unix(7_000, 0).UTC()})
	principal := pipelineManagerPrincipal("organization-1")

	if _, err := management.CreateRevision(context.Background(), principal, "billing", nil, Route{AskAgentOwner: true}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a default owner route without a path must be invalid: %v", err)
	}
	block := pipelineDefinition("asks", approvalblock.Human, approvalblock.Field("/request/tool").Equals("push"))
	block.CEL.AskAgentOwner = true
	if _, err := management.CreateRevision(context.Background(), principal, "billing", []approvalblock.Definition{block}, Route{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a block owner route without a path must be invalid: %v", err)
	}
	created, err := management.CreateRevision(context.Background(), principal, "billing", nil, Route{EscalationPath: "on-call", AskAgentOwner: true})
	if err != nil {
		t.Fatal(err)
	}
	if !created.Revision.DefaultAskAgentOwner || created.Revision.DefaultEscalationPath != "on-call" {
		t.Fatalf("revision = %#v", created.Revision)
	}
}
