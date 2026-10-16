package onboarding

import (
	"context"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/approvalpipeline"
)

// Checklist reports what the organization has set up, whoever set it up
// and however: the guided setup, the CLI, the app or the MCP server.
func (s *Service) Checklist(ctx context.Context, principal domain.Principal) (domain.SetupChecklist, error) {
	checklist, err := s.sessions.GetSetupProgress(ctx, application.GetSetupProgressInput{
		OrganizationID: principal.OrganizationID, Now: s.clock.Now(),
	})
	if err != nil {
		return domain.SetupChecklist{}, err
	}
	approvesAll, err := s.approvesEveryCall(ctx, principal)
	if err != nil {
		return domain.SetupChecklist{}, err
	}
	checklist.RulesWritten = !approvesAll
	return checklist, nil
}

// approvesEveryCall reports whether the active entry pipeline approves
// every request, as the one it is seeded with and the one the guided
// setup provisions both do.
func (s *Service) approvesEveryCall(ctx context.Context, principal domain.Principal) (bool, error) {
	memo := map[string]bool{}
	var pipeline func(key string) (bool, error)
	pipeline = func(key string) (bool, error) {
		if approves, done := memo[key]; done {
			return approves, nil
		}
		// Activation refuses a delegation cycle; should one exist anyway,
		// the key in progress reads as holding calls, which ends the walk.
		memo[key] = false
		revision, err := s.pipelines.GetActive(ctx, principal, key)
		if err != nil {
			return false, err
		}
		approves, err := approvesAll(storedDefinitions(revision.Blocks), pipeline)
		if err != nil {
			return false, err
		}
		memo[key] = approves
		return approves, nil
	}
	return pipeline(EntryPipelineKey)
}

// approvesAll reports whether blocks approve every request that reaches
// them. A branch or delegate a request may enter must approve everything
// it takes, since running out of blocks there is human, and the first
// outcome block outside them must approve. Any other block (a human or
// deny outcome, a webhook, an edition's kind) can hold or deny a call, and
// so does running out of blocks.
func approvesAll(blocks []approvalblock.Definition, pipeline func(key string) (bool, error)) (bool, error) {
	for _, block := range blocks {
		switch {
		case block.Type == approvalblock.TypeOutcome && block.Outcome != nil:
			return block.Outcome.Outcome == approvalblock.Approve, nil
		case block.Type == approvalblock.TypeBranch && block.Branch != nil:
			approves, err := approvesAll(block.Branch.Blocks, pipeline)
			if err != nil || !approves {
				return false, err
			}
		case block.Type == approvalblock.TypeDelegate && block.Delegate != nil:
			approves, err := pipeline(block.Delegate.PipelineKey)
			if err != nil || !approves {
				return false, err
			}
		default:
			return false, nil
		}
	}
	return false, nil
}

func storedDefinitions(blocks []approvalpipeline.PipelineBlock) []approvalblock.Definition {
	definitions := make([]approvalblock.Definition, len(blocks))
	for index, block := range blocks {
		definitions[index] = block.Definition
	}
	return definitions
}
