package entitlements

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

func TestUnlimitedAllowsEverything(t *testing.T) {
	adapter := NewUnlimited()
	ctx := context.Background()

	decision, err := adapter.CheckCapability(ctx, application.CheckCapabilityInput{
		OrganizationID: "organization-1",
		Capability:     application.CapabilityEnterpriseSSO,
	})
	if err != nil || !decision.Allowed {
		t.Fatalf("capability check returned (%+v, %v), want allowed", decision, err)
	}

	limit, err := adapter.Limit(ctx, application.LimitInput{
		OrganizationID: "organization-1",
		Dimension:      application.DimensionAgents,
	})
	if err != nil || !limit.Unlimited {
		t.Fatalf("limit returned (%+v, %v), want unlimited", limit, err)
	}

	if err := adapter.RecordUsage(ctx, application.RecordUsageInput{
		OrganizationID: "organization-1",
		Dimension:      application.DimensionApprovalRequests,
		Amount:         1,
		IdempotencyKey: "approval_request:request-1",
		Now:            time.Unix(50_000, 0).UTC(),
	}); err != nil {
		t.Fatalf("usage recording failed: %v", err)
	}
}

func TestUnlimitedRejectsInvalidInput(t *testing.T) {
	adapter := NewUnlimited()
	ctx := context.Background()

	if _, err := adapter.CheckCapability(ctx, application.CheckCapabilityInput{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("capability check returned %v, want domain.ErrInvalid", err)
	}
	if _, err := adapter.Limit(ctx, application.LimitInput{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("limit returned %v, want domain.ErrInvalid", err)
	}
	if err := adapter.RecordUsage(ctx, application.RecordUsageInput{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("usage recording returned %v, want domain.ErrInvalid", err)
	}
}
