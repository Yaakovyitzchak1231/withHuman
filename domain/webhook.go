package domain

import "time"

// WebhookEndpointRevision is one immutable revision of an organization
// webhook endpoint: a destination (name and URL) filed under a stable
// endpoint key, exactly like an escalation path revision. Pipeline blocks
// reference the key; deliveries resolve the key's active revision. The
// signing secret belongs to the key, not the revision, so a URL change
// never forces the receiver to reconfigure.
type WebhookEndpointRevision struct {
	ID               ID
	OrganizationID   ID
	EndpointKey      string
	Revision         int64
	IsActive         bool
	Name             string
	URL              string
	CreatedByActorID ID
	CreatedAt        time.Time
}

// WebhookEndpointSummary describes one endpoint key's revision lineage.
// Name and URL come from the latest revision; ActiveRevision is nil when
// no revision is active, which is either a key saved as a draft and never
// activated or an archived one; ArchivedAt tells them apart and is
// cleared when a revision is activated again; SecretRotatedAt is when the
// key's signing secret was last minted.
type WebhookEndpointSummary struct {
	EndpointKey     string
	Name            string
	URL             string
	ActiveRevision  *int64
	ArchivedAt      *time.Time
	LatestRevision  int64
	RevisionCount   int64
	LatestCreatedAt time.Time
	SecretRotatedAt time.Time
}
