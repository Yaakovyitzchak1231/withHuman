package discovery

import (
	"context"

	"github.com/withHumanAI/withHuman/agentruntime"
)

// pi discovers runtime tools for guided onboarding.
type pi struct{}

func (pi) Key() agentruntime.Key { return agentruntime.Pi }

func (p pi) Discover(ctx context.Context, _ Env, _ Detection) (Discovered, error) {
	return discover(ctx, p.Key(), nil), nil
}
