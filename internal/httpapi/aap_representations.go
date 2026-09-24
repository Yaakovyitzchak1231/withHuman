package httpapi

import (
	"encoding/json"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// These DTOs are the AAP wire contract, deliberately separate from product views.
type aapApprovalResponse struct {
	ID             domain.ID             `json:"id"`
	Tool           string                `json:"tool"`
	Server         string                `json:"server,omitempty"`
	Arguments      json.RawMessage       `json:"arguments"`
	Timeout        string                `json:"timeout"`
	AgentReasoning string                `json:"agent_reasoning,omitempty"`
	Context        json.RawMessage       `json:"context,omitempty"`
	Status         domain.ApprovalStatus `json:"status"`
	CreatedAt      time.Time             `json:"created_at"`
	DeadlineAt     time.Time             `json:"deadline_at"`
	Decision       *aapDecisionResponse  `json:"decision,omitempty"`
}
type aapDecisionResponse struct {
	Status    domain.ApprovalStatus `json:"status"`
	Note      string                `json:"note,omitempty"`
	DecidedAt time.Time             `json:"decided_at"`
	ExpiresAt *time.Time            `json:"expires_at,omitempty"`
}

func newAAPApprovalResponse(r domain.ApprovalRequest) aapApprovalResponse {
	out := aapApprovalResponse{ID: r.ID, Tool: r.Tool, Server: r.Server, Arguments: r.Arguments, Timeout: r.Timeout, AgentReasoning: r.AgentReasoning, Context: r.Context, Status: r.Status, CreatedAt: r.CreatedAt.UTC(), DeadlineAt: r.DeadlineAt.UTC()}
	if d := r.Decision; d != nil {
		out.Decision = &aapDecisionResponse{Status: d.Status, Note: d.Note, DecidedAt: d.DecidedAt.UTC()}
		if d.ExpiresAt != nil {
			expiry := d.ExpiresAt.UTC()
			out.Decision.ExpiresAt = &expiry
		}
	}
	return out
}
