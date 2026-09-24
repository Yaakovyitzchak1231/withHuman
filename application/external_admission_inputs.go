package application

import (
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// AdmitExternalMemberInput is an edition's unattended admission
// transaction, the counterpart of the invitation acceptance: create or
// link the user, bind the asserted identity, and mint an active
// membership with the default role. The hosted edition's just-in-time
// admission under a verified domain is the one caller today.
type AdmitExternalMemberInput struct {
	OrganizationID domain.ID
	MembershipID   domain.ID
	UserID         domain.ID
	NewUserID      domain.ID
	Email          string
	DisplayName    string
	RoleID         domain.ID
	// AdmissionSource records how the person was admitted (the hosted
	// edition's sso_jit); it is the edition's vocabulary.
	AdmissionSource domain.AdmissionSource
	ProviderKey     string
	Issuer          string
	Subject         string
	Now             time.Time
}

func (input AdmitExternalMemberInput) Validate() error {
	errs := []error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("membership ID", input.MembershipID),
		requiredString("email", input.Email),
		requiredID("role ID", input.RoleID),
		requiredString("admission source", string(input.AdmissionSource)),
		requiredString("provider key", input.ProviderKey),
		requiredString("issuer", input.Issuer),
		requiredString("subject", input.Subject),
		requiredTime("current time", input.Now),
	}
	if (input.UserID == "") == (input.NewUserID == "") {
		errs = append(errs, invalidField("user identity", "must set exactly one of user ID and new user ID"))
	}
	return firstError(errs)
}
