package heartbeat

import (
	"context"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/authorization"
)

// Status is what administrators see about the running release: whether a
// newer one exists and which advisories affect this one.
type Status struct {
	InstallationID  domain.ID
	Version         string
	LatestVersion   string
	UpdateAvailable bool
	Advisories      []domain.SecurityAdvisory
	CheckedAt       time.Time
}

// StatusReader answers the installation status from the last heartbeat.
type StatusReader struct {
	installations application.InstallationRepository
	version       string
}

func NewStatusReader(installations application.InstallationRepository, version string) *StatusReader {
	return &StatusReader{installations: installations, version: version}
}

// Read needs organization.settings.write, the administrators who can act
// on an update. An advisory fixed in this version or earlier is left out,
// so an upgrade clears it before the next heartbeat answers.
func (s *StatusReader) Read(ctx context.Context, principal domain.Principal) (Status, error) {
	if !domain.ManagementPrincipal(principal) {
		return Status{}, domain.ErrForbidden
	}
	if err := authorization.Check(principal, domain.PermissionOrganizationSettingsWrite, domain.OrganizationResource()); err != nil {
		return Status{}, err
	}
	installation, err := s.installations.GetInstallation(ctx)
	if err != nil {
		return Status{}, err
	}
	status := Status{
		InstallationID: installation.ID, Version: s.version, LatestVersion: installation.LatestVersion,
		UpdateAvailable: Newer(installation.LatestVersion, s.version),
		Advisories:      []domain.SecurityAdvisory{}, CheckedAt: installation.CheckedAt,
	}
	for _, advisory := range installation.Advisories {
		if advisory.FixedIn == "" || Newer(advisory.FixedIn, s.version) {
			status.Advisories = append(status.Advisories, advisory)
		}
	}
	return status, nil
}
