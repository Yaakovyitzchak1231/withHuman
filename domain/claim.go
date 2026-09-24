package domain

import "time"

// ClaimReleaseReason records why a claim stopped being live. A claim row is
// mutable exactly once: from open to released with one of these reasons.
type ClaimReleaseReason string

const (
	// ClaimReleased is a manual release by the claimant.
	ClaimReleased ClaimReleaseReason = "released"
	// ClaimLapsed is the retrigger timeout elapsing without a decision.
	ClaimLapsed ClaimReleaseReason = "lapsed"
	// ClaimDecided is a terminal human decision closing the claim.
	ClaimDecided ClaimReleaseReason = "decided"
	// ClaimExpired is the request's deadline expiring while claimed.
	ClaimExpired ClaimReleaseReason = "expired"
	// ClaimCancelled is the requesting instance withdrawing the request
	// while claimed.
	ClaimCancelled ClaimReleaseReason = "cancelled"
)

// Claim is the advisory "I'm looking at it" marker on a pending approval
// request. It is a queue/product sidecar, never an ApprovalStatus and never
// visible through AAP. A claim stands down further notifications but does
// not restrict who may decide.
type Claim struct {
	ID                  ID                 `json:"id"`
	RequestID           ID                 `json:"request_id"`
	OrganizationID      ID                 `json:"organization_id"`
	MembershipID        ID                 `json:"membership_id"`
	ClaimantDisplayName string             `json:"claimant_display_name,omitempty"`
	ClaimedAt           time.Time          `json:"claimed_at"`
	ExpiresAt           time.Time          `json:"expires_at"`
	ReleasedAt          *time.Time         `json:"released_at,omitempty"`
	ReleaseReason       ClaimReleaseReason `json:"release_reason,omitempty"`
}

// Live reports whether the claim is still holding at the given time.
func (c Claim) Live(now time.Time) bool {
	return c.ReleasedAt == nil && now.Before(c.ExpiresAt)
}
