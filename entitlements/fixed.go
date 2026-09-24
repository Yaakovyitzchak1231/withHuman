package entitlements

import (
	"context"
	"sync"

	"github.com/withHumanAI/withHuman/application"
)

// Fixed is the deterministic entitlements adapter for tests and the
// contract suite: limits and capability denials are set per organization
// and dimension, and every recorded usage is kept for assertions, counted
// once per idempotency key. A dimension or capability with no setting
// behaves like Unlimited.
type Fixed struct {
	mu           sync.Mutex
	limits       map[fixedLimitKey]application.LimitValue
	capabilities map[fixedCapabilityKey]application.CapabilityDecision
	recorded     []application.RecordUsageInput
	seen         map[fixedUsageKey]struct{}
}

type fixedUsageKey struct {
	organizationID string
	idempotencyKey string
}

type fixedLimitKey struct {
	organizationID string
	dimension      application.Dimension
}

type fixedCapabilityKey struct {
	organizationID string
	capability     application.Capability
}

func NewFixed() *Fixed {
	return &Fixed{
		limits:       map[fixedLimitKey]application.LimitValue{},
		capabilities: map[fixedCapabilityKey]application.CapabilityDecision{},
		seen:         map[fixedUsageKey]struct{}{},
	}
}

// SetLimit fixes the answer for one organization and dimension.
func (f *Fixed) SetLimit(organizationID string, dimension application.Dimension, limit application.LimitValue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limits[fixedLimitKey{organizationID, dimension}] = limit
}

// SetCapability fixes the answer for one organization and capability.
func (f *Fixed) SetCapability(organizationID string, capability application.Capability, allowed bool, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.capabilities[fixedCapabilityKey{organizationID, capability}] = application.CapabilityDecision{Allowed: allowed, Reason: reason}
}

// Recorded returns a copy of every usage recorded so far, in order.
func (f *Fixed) Recorded() []application.RecordUsageInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]application.RecordUsageInput, len(f.recorded))
	copy(out, f.recorded)
	return out
}

// RecordedUsage sums everything ever recorded for one organization and
// dimension, regardless of when. It is the test accessor; Usage is the
// port's windowed read.
func (f *Fixed) RecordedUsage(organizationID string, dimension application.Dimension) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var total int64
	for _, record := range f.recorded {
		if string(record.OrganizationID) == organizationID && record.Dimension == dimension {
			total += record.Amount
		}
	}
	return total
}

func (f *Fixed) Usage(_ context.Context, input application.UsageInput) (int64, error) {
	if err := input.Validate(); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var total int64
	for _, record := range f.recorded {
		if record.OrganizationID != input.OrganizationID || record.Dimension != input.Dimension {
			continue
		}
		if record.Now.Before(input.From) || !record.Now.Before(input.Until) {
			continue
		}
		total += record.Amount
	}
	return total, nil
}

func (f *Fixed) CheckCapability(_ context.Context, input application.CheckCapabilityInput) (application.CapabilityDecision, error) {
	if err := input.Validate(); err != nil {
		return application.CapabilityDecision{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if decision, ok := f.capabilities[fixedCapabilityKey{string(input.OrganizationID), input.Capability}]; ok {
		return decision, nil
	}
	return application.CapabilityDecision{Allowed: true}, nil
}

func (f *Fixed) Limit(_ context.Context, input application.LimitInput) (application.LimitValue, error) {
	if err := input.Validate(); err != nil {
		return application.LimitValue{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit, ok := f.limits[fixedLimitKey{string(input.OrganizationID), input.Dimension}]; ok {
		return limit, nil
	}
	return application.LimitValue{Unlimited: true}, nil
}

func (f *Fixed) RecordUsage(_ context.Context, input application.RecordUsageInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := fixedUsageKey{string(input.OrganizationID), input.IdempotencyKey}
	if _, replay := f.seen[key]; replay {
		return nil
	}
	f.seen[key] = struct{}{}
	f.recorded = append(f.recorded, input)
	return nil
}

var _ application.Entitlements = (*Fixed)(nil)
