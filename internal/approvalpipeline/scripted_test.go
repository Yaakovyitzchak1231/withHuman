package approvalpipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/blockruntime"
)

// Core's only asynchronous kind is the webhook, so the tests that drive
// the suspend-and-resume machinery register a kind of their own the way
// an edition does: asynchronous, answering from a script, and gated on a
// plan capability so the plan gate stays covered. It is also the proof
// that the registry seam carries an edition kind end to end.
const scriptedType approvalblock.BlockType = "scripted"

type scriptedConfig struct {
	Reason string `json:"reason"`
	Path   string `json:"escalation_path,omitempty"`
}

func (config *scriptedConfig) Validate() error {
	if strings.TrimSpace(config.Reason) == "" {
		return errors.New("block reason is required")
	}
	return nil
}

func (config *scriptedConfig) EscalationPath() string  { return config.Path }
func (config *scriptedConfig) SetReason(reason string) { config.Reason = reason }

func init() {
	if err := approvalblock.RegisterKind(approvalblock.Kind{
		Type:   scriptedType,
		Limits: approvalblock.TypeLimits{DefaultTimeout: approvalblock.DefaultWebhookTimeout, MaximumTimeout: approvalblock.MaximumWebhookTimeout, MinimumAttempts: 1, MaximumAttempts: 3},
		New:    func() approvalblock.Config { return &scriptedConfig{} },
	}); err != nil {
		panic(err)
	}
}

// scriptedReply is one answer the model gives: an outcome, or a failure.
type scriptedReply struct {
	Outcome BlockOutcome
	Err     error
}

func scriptedOutcome(outcome BlockOutcome) scriptedReply { return scriptedReply{Outcome: outcome} }

// scriptedModel answers from its queue and records every input it saw.
type scriptedModel struct {
	mu      sync.Mutex
	replies []scriptedReply
	Inputs  []RequestApprovalBlockInput
}

func newScripted(replies ...scriptedReply) *scriptedModel {
	return &scriptedModel{replies: replies}
}

func (model *scriptedModel) Push(replies ...scriptedReply) {
	model.mu.Lock()
	defer model.mu.Unlock()
	model.replies = append(model.replies, replies...)
}

func (model *scriptedModel) answer(input RequestApprovalBlockInput) (scriptedReply, bool) {
	model.mu.Lock()
	defer model.mu.Unlock()
	model.Inputs = append(model.Inputs, input)
	if len(model.replies) == 0 {
		return scriptedReply{}, false
	}
	reply := model.replies[0]
	model.replies = model.replies[1:]
	return reply, true
}

type scriptedBlock struct {
	model   *scriptedModel
	reason  string
	enabled bool
}

func (block *scriptedBlock) Evaluate(_ context.Context, input RequestApprovalBlockInput) (RequestApprovalBlockResult, error) {
	if !block.enabled {
		return RequestApprovalBlockResult{Outcome: BlockNext}, nil
	}
	if err := input.Budget.Consume(scriptedType, 5); err != nil {
		return RequestApprovalBlockResult{}, executionError(ErrorResourceLimit, err)
	}
	reply, ok := block.model.answer(input)
	if !ok {
		return RequestApprovalBlockResult{}, executionError(ErrorProvider, errors.New("no scripted reply left"))
	}
	if reply.Err != nil {
		return RequestApprovalBlockResult{}, executionError(ErrorProvider, reply.Err)
	}
	return RequestApprovalBlockResult{
		Outcome: reply.Outcome, Reason: block.reason,
		Metadata: map[string]json.RawMessage{"answer": json.RawMessage(`"` + string(reply.Outcome) + `"`)},
	}, nil
}

// scriptedRuntime is the kind's runtime over one model, registered on a
// registry through withScripted.
func scriptedRuntime(model *scriptedModel) blockruntime.Runtime {
	return blockruntime.Runtime{
		Type: scriptedType, ConfigVersion: 1, Fingerprint: "scripted/v1", Asynchronous: true,
		Capability: application.CapabilityLLMJudge,
		Compile: func(definition approvalblock.Definition) (RequestApprovalBlock, error) {
			if err := definition.Validate(); err != nil {
				return nil, err
			}
			config, ok := definition.Config.(*scriptedConfig)
			if !ok {
				return nil, errors.New("scripted block requires its configuration")
			}
			return &scriptedBlock{model: model, reason: config.Reason, enabled: definition.Enabled}, nil
		},
		Grammar: "## scripted\n\nA test kind.\n",
	}
}

func withScripted(model *scriptedModel) BlockRegistryOption {
	return WithRuntimes(scriptedRuntime(model))
}

// scriptedDefinition is the asynchronous block the fixtures place in a
// pipeline; its key is what the assertions on events name.
func scriptedDefinition(options ...approvalblock.Option) approvalblock.Definition {
	base := []approvalblock.Option{
		approvalblock.Name("Check refunds"),
		approvalblock.Reason("Checked against the refund policy."),
	}
	return *approvalblock.DefineKind("refund-check", scriptedType, &scriptedConfig{}, append(base, options...)...)
}

func scriptedInput() RequestApprovalBlockInput {
	return RequestApprovalBlockInput{
		OrganizationID: "organization-1",
		Request: BlockRequest{
			Tool: "stripe.refund", Arguments: json.RawMessage(`{"amount_cents":900,"customer":{"email":"buyer@example.com"}}`),
			AgentReasoning: "The customer was charged twice.", Context: json.RawMessage(`{"run_id":"4821"}`),
		},
		Agent:            BlockAgent{Slug: "billing", Name: "billing-agent"},
		PreviousMetadata: map[string]json.RawMessage{"organization/risk": json.RawMessage(`{"risk":"low"}`)},
		Budget:           &BlockExecutionBudget{},
	}
}

type recordingEntitlements struct {
	usage []application.RecordUsageInput
}

func (r *recordingEntitlements) CheckCapability(context.Context, application.CheckCapabilityInput) (application.CapabilityDecision, error) {
	return application.CapabilityDecision{Allowed: true}, nil
}

func (r *recordingEntitlements) Limit(context.Context, application.LimitInput) (application.LimitValue, error) {
	return application.LimitValue{Unlimited: true}, nil
}

func (r *recordingEntitlements) RecordUsage(_ context.Context, input application.RecordUsageInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	r.usage = append(r.usage, input)
	return nil
}

func (r *recordingEntitlements) Usage(_ context.Context, input application.UsageInput) (int64, error) {
	var total int64
	for _, record := range r.usage {
		if record.OrganizationID == input.OrganizationID && record.Dimension == input.Dimension {
			total += record.Amount
		}
	}
	return total, nil
}

type denyingEntitlements struct{ recordingEntitlements }

func (denyingEntitlements) CheckCapability(context.Context, application.CheckCapabilityInput) (application.CapabilityDecision, error) {
	return application.CapabilityDecision{Allowed: false, Reason: "not on this plan"}, nil
}

// An edition kind's grammar follows core's in the pipeline_blocks
// reference the management MCP server serves.
func TestRegistryAppendsEditionGrammar(t *testing.T) {
	registry := NewBlockRegistry(withScripted(newScripted()))
	compiled := NewCompiledPipelineState(newMemoryPipelineRepository(), registry)
	if grammar := compiled.Grammar(); !strings.Contains(grammar, "## scripted") {
		t.Fatalf("grammar = %q, want the scripted kind's section", grammar)
	}
	if grammar := NewCompiledPipelineState(newMemoryPipelineRepository(), NewBlockRegistry()).Grammar(); grammar != "" {
		t.Fatalf("a registry without edition kinds appends %q", grammar)
	}
}

// An edition kind reports what stands in the way of running a definition
// on this deployment through its runtime's Problem: a deployment-wide gap
// refuses the revision as unconfigured, a dependency the block names
// refuses it as invalid, and the references list carries the code either
// way. A stored revision naming a dropped dependency still loads, since
// only Problem changed, so readiness never closes for every organization.
func TestManagementRefusesWhatAnEditionKindCannotRun(t *testing.T) {
	ctx := context.Background()
	repository := newMemoryPipelineRepository()
	clock := fixedPipelineClock{now: time.Unix(100, 0).UTC()}
	principal := pipelineManagerPrincipal("organization-1")
	definitions := []approvalblock.Definition{scriptedDefinition()}

	unconfigured := scriptedRuntime(newScripted())
	unconfigured.Problem = func(approvalblock.Definition) (string, error) {
		return "scripted_unconfigured", &blockruntime.Unconfigured{Code: "scripted_unconfigured"}
	}
	var missing *blockruntime.Unconfigured
	service := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry(WithRuntimes(unconfigured))), clock)
	if _, err := service.CreateRevision(ctx, principal, "billing", definitions, Route{}); !errors.As(err, &missing) || missing.Code != "scripted_unconfigured" {
		t.Fatalf("creation returned %v, want unconfigured", err)
	}

	full := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry(withScripted(newScripted()))), clock)
	created, err := full.CreateRevision(ctx, principal, "billing", definitions, Route{})
	if err != nil {
		t.Fatal(err)
	}

	// The deployment drops what the stored block names.
	refusing := scriptedRuntime(newScripted(scriptedOutcome(BlockApprove)))
	refusing.Problem = func(definition approvalblock.Definition) (string, error) {
		return "scripted_model_unavailable", &blockruntime.Refused{Code: "scripted_model_unavailable", Message: "names a model this deployment does not offer"}
	}
	reduced := NewManagementService(repository, NewCompiledPipelineState(repository, NewBlockRegistry(WithRuntimes(refusing))), clock)
	var refused *blockruntime.Refused
	if _, err := reduced.CreateRevision(ctx, principal, "billing", definitions, Route{}); !errors.As(err, &refused) || refused.Code != "scripted_model_unavailable" {
		t.Fatalf("creation returned %v, want the kind's refusal", err)
	}
	sample := PreviewSample{Request: BlockRequest{Tool: "stripe.refund", Arguments: json.RawMessage(`{}`)}, Agent: BlockAgent{Slug: "billing", Name: "billing-agent"}}
	if _, err := reduced.Preview(ctx, principal, "billing", definitions, Route{}, sample); !errors.As(err, &refused) {
		t.Fatalf("preview returned %v, want the kind's refusal", err)
	}
	if _, err := reduced.Activate(ctx, principal, "billing", created.Revision.Revision, 1); !errors.As(err, &refused) {
		t.Fatalf("activation returned %v, want the kind's refusal", err)
	}
	references, err := reduced.References(ctx, principal, "billing", created.Revision.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if references.Activatable || len(references.Blocks) != 1 || references.Blocks[0].Problem != "scripted_model_unavailable" || references.Blocks[0].Type != scriptedType {
		t.Fatalf("references must report the kind's problem: %+v", references)
	}
	if _, err := full.Activate(ctx, principal, "billing", created.Revision.Revision, 1); err != nil {
		t.Fatal(err)
	}
	compiled := NewCompiledPipelineState(repository, NewBlockRegistry(WithRuntimes(refusing)))
	if err := compiled.Initialize(ctx); err != nil {
		t.Fatalf("a refused dependency must not stop stored pipelines from loading: %v", err)
	}
}
