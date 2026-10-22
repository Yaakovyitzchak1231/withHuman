package application

import (
	"context"
	"fmt"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// MaximumRequestSummaryReads bounds one summary read, the size of the
// largest page a product read returns.
const MaximumRequestSummaryReads = 500

// HumanReviewHook is told once a request is waiting for a person, after
// its pipeline settled on human review. The worker runs hooks beside
// notification planning, never in front of it: a slow or failing hook
// never delays a prompt or a decision. Core composes none; an edition
// registers its own (the hosted request summarizer).
type HumanReviewHook interface {
	HumanReviewRequested(ctx context.Context, input HumanReviewRequestedInput) error
}

// HumanReviewRequestedInput carries the request as the workflow loaded it.
type HumanReviewRequestedInput struct {
	Request domain.ApprovalRequest
}

func (input HumanReviewRequestedInput) Validate() error {
	if err := requiredID("organization ID", input.Request.OrganizationID); err != nil {
		return err
	}
	return requiredID("request ID", input.Request.ID)
}

// RequestSummary is a one-line description of a request an edition wrote
// for its reviewers. Product reads carry it beside the presentation; it is
// a reading aid, never part of the action and never sent to the agent.
type RequestSummary struct {
	Text        string
	GeneratedAt time.Time
}

// RequestSummaryReader returns the summaries stored for some requests of
// one organization, keyed by request ID; a request without one is absent.
// Core composes none.
type RequestSummaryReader interface {
	RequestSummaries(ctx context.Context, input ReadRequestSummariesInput) (map[domain.ID]RequestSummary, error)
}

type ReadRequestSummariesInput struct {
	OrganizationID domain.ID
	RequestIDs     []domain.ID
}

func (input ReadRequestSummariesInput) Validate() error {
	if err := requiredID("organization ID", input.OrganizationID); err != nil {
		return err
	}
	if len(input.RequestIDs) > MaximumRequestSummaryReads {
		return fmt.Errorf("read at most %d request summaries at once: %w", MaximumRequestSummaryReads, domain.ErrInvalid)
	}
	return nil
}
