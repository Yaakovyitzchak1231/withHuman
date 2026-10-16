package application

import (
	"context"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// HeartbeatInterval is how often a deployment reports itself.
const HeartbeatInterval = 24 * time.Hour

// InstallationRepository keeps the deployment's one installation row: the
// heartbeat claims each send there, so one replica sends per interval, and
// records the answer the product reads to offer an update.
type InstallationRepository interface {
	ClaimHeartbeat(ctx context.Context, input ClaimHeartbeatInput) (domain.ID, bool, error)
	RecordHeartbeatAnswer(ctx context.Context, input RecordHeartbeatAnswerInput) error
	GetInstallation(ctx context.Context) (domain.Installation, error)
}

// ClaimHeartbeatInput claims the next send when the last claim is older
// than the interval or reported another version, so an upgrade reports
// straight away.
type ClaimHeartbeatInput struct {
	Now     time.Time
	Version string
}

func (input ClaimHeartbeatInput) Validate() error {
	return firstError([]error{
		requiredTime("current time", input.Now),
		requiredString("version", input.Version),
	})
}

// RecordHeartbeatAnswerInput stores what the release service answered.
type RecordHeartbeatAnswerInput struct {
	CheckedAt     time.Time
	LatestVersion string
	Advisories    []domain.SecurityAdvisory
}

func (input RecordHeartbeatAnswerInput) Validate() error {
	checks := []error{
		requiredTime("checked at", input.CheckedAt),
		requiredString("latest version", input.LatestVersion),
	}
	for _, advisory := range input.Advisories {
		checks = append(checks,
			requiredString("advisory id", advisory.ID),
			requiredString("advisory summary", advisory.Summary),
			requiredString("advisory url", advisory.URL))
	}
	return firstError(checks)
}
