package application

import (
	"context"
	"fmt"
	"time"

	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/toolpresentation"
)

// ToolPresentationRepository owns an organization's own tool
// presentations. Every read and write is organization scoped.
type ToolPresentationRepository interface {
	ListToolPresentations(context.Context, ListToolPresentationsInput) ([]domain.ToolPresentation, error)
	GetToolPresentation(context.Context, GetToolPresentationInput) (domain.ToolPresentation, error)
	CreateToolPresentation(context.Context, CreateToolPresentationInput) (domain.ToolPresentation, error)
	UpdateToolPresentation(context.Context, UpdateToolPresentationInput) (domain.ToolPresentation, error)
	DeleteToolPresentation(context.Context, DeleteToolPresentationInput) error
}

type ListToolPresentationsInput struct {
	OrganizationID domain.ID
}

func (input ListToolPresentationsInput) Validate() error {
	return requiredID("organization ID", input.OrganizationID)
}

type GetToolPresentationInput struct {
	OrganizationID domain.ID
	ID             domain.ID
}

func (input GetToolPresentationInput) Validate() error {
	if err := requiredID("organization ID", input.OrganizationID); err != nil {
		return err
	}
	return requiredID("presentation ID", input.ID)
}

func validEntry(entry toolpresentation.Entry) error {
	if err := entry.Validate(); err != nil {
		return fmt.Errorf("%w: %w", domain.ErrInvalid, err)
	}
	return nil
}

// CreateToolPresentationInput stores a new presentation and audits it as
// tool_presentation.created.
type CreateToolPresentationInput struct {
	ID                domain.ID
	OrganizationID    domain.ID
	ActorMembershipID domain.ID
	Entry             toolpresentation.Entry
	Now               time.Time
}

func (input CreateToolPresentationInput) Validate() error {
	if err := requiredID("presentation ID", input.ID); err != nil {
		return err
	}
	if err := requiredID("organization ID", input.OrganizationID); err != nil {
		return err
	}
	if err := requiredID("actor membership ID", input.ActorMembershipID); err != nil {
		return err
	}
	if err := requiredTime("now", input.Now); err != nil {
		return err
	}
	return validEntry(input.Entry)
}

// UpdateToolPresentationInput replaces a presentation's binding and
// specification and audits it as tool_presentation.updated.
type UpdateToolPresentationInput struct {
	ID                domain.ID
	OrganizationID    domain.ID
	ActorMembershipID domain.ID
	Entry             toolpresentation.Entry
	Now               time.Time
}

func (input UpdateToolPresentationInput) Validate() error {
	return CreateToolPresentationInput(input).Validate()
}

// DeleteToolPresentationInput removes a presentation and audits it as
// tool_presentation.deleted. Deleting one that is already gone is not
// found, so a client can tell a race from success.
type DeleteToolPresentationInput struct {
	ID                domain.ID
	OrganizationID    domain.ID
	ActorMembershipID domain.ID
	Now               time.Time
}

func (input DeleteToolPresentationInput) Validate() error {
	if err := requiredID("presentation ID", input.ID); err != nil {
		return err
	}
	if err := requiredID("organization ID", input.OrganizationID); err != nil {
		return err
	}
	if err := requiredID("actor membership ID", input.ActorMembershipID); err != nil {
		return err
	}
	return requiredTime("now", input.Now)
}
