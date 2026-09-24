package domain

import (
	"errors"
	"time"

	"github.com/withHumanAI/withHuman/toolpresentation"
)

// ToolPresentation is an organization's own presentation of a tool
// call, layered over the shipped catalog: an organization entry wins over
// a shipped one whenever both match, however specific the shipped one is,
// because the organization knows its own tools better than the product.
type ToolPresentation struct {
	ID             ID                     `json:"id"`
	OrganizationID ID                     `json:"organization_id"`
	Entry          toolpresentation.Entry `json:"entry"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
}

// ErrToolPresentationExists refuses a second presentation for the same
// binding (runtime, server, tool) in one organization.
var ErrToolPresentationExists = errors.New("tool presentation already exists")
