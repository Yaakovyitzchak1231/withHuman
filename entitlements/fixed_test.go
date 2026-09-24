package entitlements_test

import (
	"testing"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/entitlements"
	"github.com/withHumanAI/withHuman/entitlements/contracttest"
)

type fixedFixture struct{ adapter *entitlements.Fixed }

func (f fixedFixture) Port() application.Entitlements { return f.adapter }
func (f fixedFixture) SetLimit(organizationID string, dimension application.Dimension, limit application.LimitValue) {
	f.adapter.SetLimit(organizationID, dimension, limit)
}
func (f fixedFixture) SetCapability(organizationID string, capability application.Capability, allowed bool, reason string) {
	f.adapter.SetCapability(organizationID, capability, allowed, reason)
}
func (f fixedFixture) RecordedUsage(organizationID string, dimension application.Dimension) int64 {
	return f.adapter.RecordedUsage(organizationID, dimension)
}

func TestFixedSatisfiesContract(t *testing.T) {
	contracttest.Entitlements(t, func(*testing.T) contracttest.Fixture {
		return fixedFixture{adapter: entitlements.NewFixed()}
	})
}
