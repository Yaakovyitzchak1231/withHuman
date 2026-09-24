package application

import (
	"fmt"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// Capability names a plan-gated feature. Capability checks answer whether an
// organization may use the feature at all.
type Capability string

const (
	CapabilityEnterpriseSSO Capability = "enterprise_sso"
	CapabilityDirectorySync Capability = "directory_sync"
	CapabilityMobilePush    Capability = "mobile_push"
	CapabilitySIEMExport    Capability = "siem_export"
	CapabilityLLMJudge      Capability = "llm_judge"
	CapabilityWebhookBlock  Capability = "webhook_block"
	// CapabilityPhoneNotifications gates approval prompts over SMS and
	// WhatsApp. Verification codes are not gated by it: proving a number
	// is the person's business, not the organization's.
	CapabilityPhoneNotifications Capability = "phone_notifications"
	// CapabilityAPIKeyScoping gates narrowing a personal API key to a
	// permission list, which the hosted edition's plans carry. Custom
	// role authoring is not built in either edition; runtime.Server.Roles
	// is the seam it will use.
	CapabilityAPIKeyScoping Capability = "api_key_scoping"
)

// Dimension names a countable resource. Limits and usage metering share one
// dimension vocabulary.
type Dimension string

const (
	DimensionSeats            Dimension = "seats"
	DimensionAgents           Dimension = "agents"
	DimensionPendingApprovals Dimension = "pending_approvals"
	DimensionApprovalRequests Dimension = "approval_requests"
	// DimensionLLMJudgeEvaluations counts model calls made by judge blocks;
	// DimensionLLMJudgeTokens counts the input and output tokens they used.
	DimensionLLMJudgeEvaluations Dimension = "llm_judge_evaluations"
	DimensionLLMJudgeTokens      Dimension = "llm_judge_tokens"
	// DimensionWebhookDeliveries counts every delivery attempt a webhook
	// block makes.
	DimensionWebhookDeliveries Dimension = "webhook_deliveries"
	// DimensionGatewayToolCalls counts every call proxied through the MCP
	// gateway, gated or not: the gateway holds the downstream credential
	// either way, and that is the metered unit.
	DimensionGatewayToolCalls Dimension = "gateway_tool_calls"
	// DimensionPhoneMessages counts SMS and WhatsApp approval prompts the
	// provider accepted, pooled across both channels per organization per
	// calendar month. One pool because the two cost about the same and a
	// customer should not have to reason about two numbers.
	DimensionPhoneMessages Dimension = "phone_messages"
)

// maxIdempotencyKeyLength bounds the caller-chosen usage key so it fits a
// ledger index comfortably.
const maxIdempotencyKeyLength = 200

type CheckCapabilityInput struct {
	OrganizationID domain.ID
	Capability     Capability
}

func (input CheckCapabilityInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredString("capability", string(input.Capability)),
	})
}

type LimitInput struct {
	OrganizationID domain.ID
	Dimension      Dimension
}

func (input LimitInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredString("dimension", string(input.Dimension)),
	})
}

// UsageInput asks how much of a dimension an organization consumed in the
// half-open window [From, Until). Consumption quotas check it before
// spending a unit; the window is the caller's, usually MonthWindow.
type UsageInput struct {
	OrganizationID domain.ID
	Dimension      Dimension
	From           time.Time
	Until          time.Time
}

func (input UsageInput) Validate() error {
	if err := firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredString("dimension", string(input.Dimension)),
		requiredTime("usage window start", input.From),
		requiredTime("usage window end", input.Until),
	}); err != nil {
		return err
	}
	if !input.From.Before(input.Until) {
		return invalidField("usage window", "must start before it ends")
	}
	return nil
}

// MonthWindow is the UTC calendar month containing now, as [from, until).
// Monthly consumption quotas reset on this boundary everywhere, so the
// product's "per month" and the ledger's rollup mean the same thing.
func MonthWindow(now time.Time) (from, until time.Time) {
	now = now.UTC()
	from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return from, from.AddDate(0, 1, 0)
}

// RecordUsageInput meters one unit of work. Recording is at-least-once
// from retrying activities, so every record carries a caller-chosen
// IdempotencyKey, unique per organization, and an adapter counts a key
// exactly once no matter how many times it arrives.
type RecordUsageInput struct {
	OrganizationID domain.ID
	Dimension      Dimension
	Amount         int64
	IdempotencyKey string
	Now            time.Time
}

func (input RecordUsageInput) Validate() error {
	if err := firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredString("dimension", string(input.Dimension)),
		requiredString("idempotency key", input.IdempotencyKey),
		requiredTime("current time", input.Now),
	}); err != nil {
		return err
	}
	if len(input.IdempotencyKey) > maxIdempotencyKeyLength {
		return invalidField("idempotency key", fmt.Sprintf("must be at most %d bytes", maxIdempotencyKeyLength))
	}
	if input.Amount <= 0 {
		return invalidField("amount", "must be positive")
	}
	return nil
}

// AllocationQuota carries a resolved limit into the transaction that
// allocates against it, so the count and the insert happen under one
// lock. The service resolves the limit through the entitlements port;
// the repository enforces it. The zero value is deliberately invalid:
// a caller that forgot to resolve the quota fails validation instead of
// silently allocating without a limit or silently refusing everything.
type AllocationQuota struct {
	limit    LimitValue
	resolved bool
}

// ResolvedQuota wraps a limit answered by the entitlements port.
func ResolvedQuota(limit LimitValue) AllocationQuota {
	return AllocationQuota{limit: limit, resolved: true}
}

// UnlimitedQuota is the quota for paths that enforce nothing, such as
// tests and the open edition's adapters.
func UnlimitedQuota() AllocationQuota {
	return ResolvedQuota(LimitValue{Unlimited: true})
}

func (q AllocationQuota) Validate() error {
	if !q.resolved {
		return invalidField("allocation quota", "must be resolved through the entitlements port")
	}
	if !q.limit.Unlimited && q.limit.Value < 0 {
		return invalidField("allocation quota", "must not be negative")
	}
	return nil
}

func (q AllocationQuota) Unlimited() bool { return q.limit.Unlimited }

// Check refuses an allocation when used already meets the limit,
// returning the structured quota error for the dimension.
func (q AllocationQuota) Check(dimension Dimension, used int64) error {
	if q.limit.Unlimited || used < q.limit.Value {
		return nil
	}
	return domain.QuotaExceeded{Dimension: string(dimension), Limit: q.limit.Value, Used: used}
}

type CountPendingApprovalsInput struct {
	OrganizationID domain.ID
}

func (input CountPendingApprovalsInput) Validate() error {
	return requiredID("organization ID", input.OrganizationID)
}
