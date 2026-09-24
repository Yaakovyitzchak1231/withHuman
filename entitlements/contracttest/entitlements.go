// Package contracttest is the behavior every entitlements adapter must
// satisfy, open or hosted. An adapter that cannot pass this suite does not
// ship; the hosted ledger-backed adapter runs it from its own repository.
package contracttest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// Fixture is an adapter under test plus the controls the suite needs to
// put it into a known state. Organization identifiers are opaque strings
// the fixture may map onto whatever its storage requires.
type Fixture interface {
	Port() application.Entitlements
	// SetLimit makes Limit answer the given value for the organization and
	// dimension.
	SetLimit(organizationID string, dimension application.Dimension, limit application.LimitValue)
	// SetCapability makes CheckCapability answer allowed or denied with the
	// given reason.
	SetCapability(organizationID string, capability application.Capability, allowed bool, reason string)
	// RecordedUsage reports the total amount ever recorded for the
	// organization and dimension, independent of any window.
	RecordedUsage(organizationID string, dimension application.Dimension) int64
}

// Entitlements runs the suite. The factory returns a fresh fixture for
// every case.
func Entitlements(t *testing.T, factory func(t *testing.T) Fixture) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(90_000, 0).UTC()

	t.Run("rejects invalid input", func(t *testing.T) {
		port := factory(t).Port()
		if _, err := port.CheckCapability(ctx, application.CheckCapabilityInput{}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("capability check returned %v, want domain.ErrInvalid", err)
		}
		if _, err := port.Limit(ctx, application.LimitInput{}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("limit returned %v, want domain.ErrInvalid", err)
		}
		if err := port.RecordUsage(ctx, application.RecordUsageInput{}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("usage recording returned %v, want domain.ErrInvalid", err)
		}
		if _, err := port.Usage(ctx, application.UsageInput{}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("usage read returned %v, want domain.ErrInvalid", err)
		}
	})

	t.Run("answers the configured limit per organization", func(t *testing.T) {
		fixture := factory(t)
		fixture.SetLimit("org-a", application.DimensionSeats, application.LimitValue{Value: 3})
		fixture.SetLimit("org-b", application.DimensionSeats, application.LimitValue{Unlimited: true})
		limit, err := fixture.Port().Limit(ctx, application.LimitInput{OrganizationID: "org-a", Dimension: application.DimensionSeats})
		if err != nil || limit.Unlimited || limit.Value != 3 {
			t.Fatalf("limit for org-a = (%+v, %v), want value 3", limit, err)
		}
		limit, err = fixture.Port().Limit(ctx, application.LimitInput{OrganizationID: "org-b", Dimension: application.DimensionSeats})
		if err != nil || !limit.Unlimited {
			t.Fatalf("limit for org-b = (%+v, %v), want unlimited", limit, err)
		}
	})

	t.Run("denies a capability with a reason", func(t *testing.T) {
		fixture := factory(t)
		fixture.SetCapability("org-a", application.CapabilityEnterpriseSSO, false, "Enterprise SSO is not included in the current plan.")
		fixture.SetCapability("org-b", application.CapabilityEnterpriseSSO, true, "")
		decision, err := fixture.Port().CheckCapability(ctx, application.CheckCapabilityInput{OrganizationID: "org-a", Capability: application.CapabilityEnterpriseSSO})
		if err != nil || decision.Allowed || decision.Reason == "" {
			t.Fatalf("decision for org-a = (%+v, %v), want denied with reason", decision, err)
		}
		decision, err = fixture.Port().CheckCapability(ctx, application.CheckCapabilityInput{OrganizationID: "org-b", Capability: application.CapabilityEnterpriseSSO})
		if err != nil || !decision.Allowed {
			t.Fatalf("decision for org-b = (%+v, %v), want allowed", decision, err)
		}
	})

	t.Run("accumulates recorded usage per organization", func(t *testing.T) {
		fixture := factory(t)
		port := fixture.Port()
		for index, amount := range []int64{1, 2} {
			if err := port.RecordUsage(ctx, application.RecordUsageInput{
				OrganizationID: "org-a", Dimension: application.DimensionApprovalRequests, Amount: amount,
				IdempotencyKey: fmt.Sprintf("approval_request:%d", index), Now: now,
			}); err != nil {
				t.Fatalf("record usage: %v", err)
			}
		}
		if err := port.RecordUsage(ctx, application.RecordUsageInput{
			OrganizationID: "org-b", Dimension: application.DimensionApprovalRequests, Amount: 5,
			IdempotencyKey: "approval_request:0", Now: now,
		}); err != nil {
			t.Fatalf("record usage: %v", err)
		}
		if got := fixture.RecordedUsage("org-a", application.DimensionApprovalRequests); got != 3 {
			t.Fatalf("usage for org-a = %d, want 3", got)
		}
		if got := fixture.RecordedUsage("org-b", application.DimensionApprovalRequests); got != 5 {
			t.Fatalf("usage for org-b = %d, want 5", got)
		}
	})

	t.Run("sums usage inside a window only", func(t *testing.T) {
		fixture := factory(t)
		port := fixture.Port()
		thisMonth, nextMonth := now, now.AddDate(0, 1, 0)
		for index, record := range []struct {
			amount int64
			at     time.Time
		}{{2, thisMonth}, {3, nextMonth}} {
			if err := port.RecordUsage(ctx, application.RecordUsageInput{
				OrganizationID: "org-a", Dimension: application.DimensionPhoneMessages, Amount: record.amount,
				IdempotencyKey: fmt.Sprintf("phone_message:%d", index), Now: record.at,
			}); err != nil {
				t.Fatalf("record usage: %v", err)
			}
		}
		from, until := application.MonthWindow(thisMonth)
		if got, err := port.Usage(ctx, application.UsageInput{OrganizationID: "org-a", Dimension: application.DimensionPhoneMessages, From: from, Until: until}); err != nil || got != 2 {
			t.Fatalf("usage this month = (%d, %v), want 2", got, err)
		}
		from, until = application.MonthWindow(nextMonth)
		if got, err := port.Usage(ctx, application.UsageInput{OrganizationID: "org-a", Dimension: application.DimensionPhoneMessages, From: from, Until: until}); err != nil || got != 3 {
			t.Fatalf("usage next month = (%d, %v), want 3", got, err)
		}
		if got := fixture.RecordedUsage("org-a", application.DimensionPhoneMessages); got != 5 {
			t.Fatalf("recorded usage = %d, want 5", got)
		}
	})

	t.Run("counts a replayed idempotency key once", func(t *testing.T) {
		fixture := factory(t)
		port := fixture.Port()
		for range 3 {
			if err := port.RecordUsage(ctx, application.RecordUsageInput{
				OrganizationID: "org-a", Dimension: application.DimensionGatewayToolCalls, Amount: 1,
				IdempotencyKey: "gateway_tool_call:call-1", Now: now,
			}); err != nil {
				t.Fatalf("record usage: %v", err)
			}
		}
		if got := fixture.RecordedUsage("org-a", application.DimensionGatewayToolCalls); got != 1 {
			t.Fatalf("usage after replays = %d, want 1", got)
		}
	})
}
