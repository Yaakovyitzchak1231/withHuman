// Package entitlements holds the open entitlements adapters. The hosted
// product implements application.Entitlements against its billing system in
// its own repository.
package entitlements

import (
	"context"

	"github.com/withHumanAI/withHuman/application"
)

// Unlimited is the default adapter for the self-hosted distribution: every
// capability is allowed, every limit is unlimited, and usage is discarded.
type Unlimited struct{}

func NewUnlimited() Unlimited { return Unlimited{} }

func (Unlimited) CheckCapability(_ context.Context, input application.CheckCapabilityInput) (application.CapabilityDecision, error) {
	if err := input.Validate(); err != nil {
		return application.CapabilityDecision{}, err
	}
	return application.CapabilityDecision{Allowed: true}, nil
}

func (Unlimited) Limit(_ context.Context, input application.LimitInput) (application.LimitValue, error) {
	if err := input.Validate(); err != nil {
		return application.LimitValue{}, err
	}
	return application.LimitValue{Unlimited: true}, nil
}

func (Unlimited) Usage(_ context.Context, input application.UsageInput) (int64, error) {
	if err := input.Validate(); err != nil {
		return 0, err
	}
	return 0, nil
}

func (Unlimited) RecordUsage(_ context.Context, input application.RecordUsageInput) error {
	return input.Validate()
}

var _ application.Entitlements = Unlimited{}
