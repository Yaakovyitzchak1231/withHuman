// Package toolpresentations owns the organization's tool presentations:
// the list an administrator edits on the settings page, layered over the
// catalog the product ships, and the resolution every product read of a
// request performs to attach the presentation its client renders with.
// Reading the list needs organization.read and changing it
// organization.settings.write; resolution needs nothing, since it only
// runs on a request the caller was already allowed to read.
package toolpresentations

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/authorization"
	"github.com/withHumanAI/withHuman/toolpresentation"
)

// Source says where a resolved presentation came from.
type Source string

const (
	SourceBuiltIn      Source = "built_in"
	SourceOrganization Source = "organization"
)

// Presentation is what a request carries to its client: the resolved
// specification and where it came from, so a page can offer to edit an
// organization entry or to override a shipped one.
type Presentation struct {
	Source Source
	// ID is the organization entry's id; empty for a shipped one.
	ID   domain.ID
	Spec toolpresentation.Spec
}

// cacheTTL bounds how stale a resolution may be after an administrator
// edits the list on another replica. The queue stream re-lists every
// second, so without a cache each open queue would query the table once
// a second for a list that changes a few times a year.
const cacheTTL = 5 * time.Second

type cached struct {
	records []domain.ToolPresentation
	entries []toolpresentation.Entry
	expires time.Time
}

type Service struct {
	repo   application.ToolPresentationRepository
	clock  application.Clock
	logger *slog.Logger

	mu    sync.Mutex
	cache map[domain.ID]cached
}

func NewService(repo application.ToolPresentationRepository, clock application.Clock, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{repo: repo, clock: clock, logger: logger, cache: make(map[domain.ID]cached)}
}

func (s *Service) require(principal domain.Principal, permission domain.Permission) error {
	if principal.ActorType != domain.ActorHuman || principal.OrganizationID == "" || principal.MembershipID == "" {
		return domain.ErrForbidden
	}
	return authorization.Check(principal, permission, domain.OrganizationResource())
}

// List reads the organization's own presentations.
func (s *Service) List(ctx context.Context, principal domain.Principal) ([]domain.ToolPresentation, error) {
	if err := s.require(principal, domain.PermissionOrganizationRead); err != nil {
		return nil, err
	}
	return s.repo.ListToolPresentations(ctx, application.ListToolPresentationsInput{OrganizationID: principal.OrganizationID})
}

// Create stores a new presentation.
func (s *Service) Create(ctx context.Context, principal domain.Principal, entry toolpresentation.Entry) (domain.ToolPresentation, error) {
	if err := s.require(principal, domain.PermissionOrganizationSettingsWrite); err != nil {
		return domain.ToolPresentation{}, err
	}
	record, err := s.repo.CreateToolPresentation(ctx, application.CreateToolPresentationInput{
		ID: domain.ID(uuid.NewString()), OrganizationID: principal.OrganizationID, ActorMembershipID: principal.MembershipID,
		Entry: entry, Now: s.clock.Now(),
	})
	s.forget(principal.OrganizationID)
	return record, err
}

// Update replaces a presentation's binding and specification.
func (s *Service) Update(ctx context.Context, principal domain.Principal, id domain.ID, entry toolpresentation.Entry) (domain.ToolPresentation, error) {
	if err := s.require(principal, domain.PermissionOrganizationSettingsWrite); err != nil {
		return domain.ToolPresentation{}, err
	}
	record, err := s.repo.UpdateToolPresentation(ctx, application.UpdateToolPresentationInput{
		ID: id, OrganizationID: principal.OrganizationID, ActorMembershipID: principal.MembershipID,
		Entry: entry, Now: s.clock.Now(),
	})
	s.forget(principal.OrganizationID)
	return record, err
}

// Delete removes a presentation; the shipped catalog takes over for its
// binding.
func (s *Service) Delete(ctx context.Context, principal domain.Principal, id domain.ID) error {
	if err := s.require(principal, domain.PermissionOrganizationSettingsWrite); err != nil {
		return err
	}
	err := s.repo.DeleteToolPresentation(ctx, application.DeleteToolPresentationInput{
		ID: id, OrganizationID: principal.OrganizationID, ActorMembershipID: principal.MembershipID, Now: s.clock.Now(),
	})
	s.forget(principal.OrganizationID)
	return err
}

func (s *Service) forget(orgID domain.ID) {
	s.mu.Lock()
	delete(s.cache, orgID)
	s.mu.Unlock()
}

// organizationEntries reads the organization's entries through the
// short-lived cache. A read failure logs and answers no entries, so a
// request page still renders with the shipped catalog.
func (s *Service) organizationEntries(ctx context.Context, orgID domain.ID) cached {
	now := s.clock.Now()
	s.mu.Lock()
	entry, ok := s.cache[orgID]
	s.mu.Unlock()
	if ok && now.Before(entry.expires) {
		return entry
	}
	records, err := s.repo.ListToolPresentations(ctx, application.ListToolPresentationsInput{OrganizationID: orgID})
	if err != nil {
		s.logger.WarnContext(ctx, "tool presentations unavailable, using the shipped catalog", "error", err)
		return cached{}
	}
	entries := make([]toolpresentation.Entry, len(records))
	for index, record := range records {
		entries[index] = record.Entry
	}
	entry = cached{records: records, entries: entries, expires: now.Add(cacheTTL)}
	s.mu.Lock()
	s.cache[orgID] = entry
	s.mu.Unlock()
	return entry
}

// Present resolves the presentation for one request: the organization's
// own entries first, then the shipped catalog. It reports false when
// neither names the tool, and the client renders by shape.
func (s *Service) Present(ctx context.Context, request domain.ApprovalRequest) (Presentation, bool) {
	return present(s.organizationEntries(ctx, request.OrganizationID), request)
}

// Presenter resolves many requests of one organization against one read
// of its entries, for list responses.
func (s *Service) Presenter(ctx context.Context, orgID domain.ID) func(domain.ApprovalRequest) (Presentation, bool) {
	organization := s.organizationEntries(ctx, orgID)
	return func(request domain.ApprovalRequest) (Presentation, bool) {
		return present(organization, request)
	}
}

func present(organization cached, request domain.ApprovalRequest) (Presentation, bool) {
	selector := SelectorFor(request)
	if index, ok := toolpresentation.ResolveIndex(organization.entries, selector); ok {
		record := organization.records[index]
		return Presentation{Source: SourceOrganization, ID: record.ID, Spec: record.Entry.Spec.Normalize()}, true
	}
	if entry, ok := toolpresentation.Resolve(toolpresentation.Builtin(), selector); ok {
		return Presentation{Source: SourceBuiltIn, Spec: entry.Spec.Normalize()}, true
	}
	return Presentation{}, false
}

// SelectorFor derives the lookup key from the request: the runtime the
// adapter named in the context, else the agent type; the server that
// defines the tool; and the tool as that server defines it.
func SelectorFor(request domain.ApprovalRequest) toolpresentation.Selector {
	selector := toolpresentation.Selector{Runtime: string(request.AgentSlug), Server: request.Server, Tool: request.Tool}
	if len(request.Context) == 0 {
		return selector
	}
	var context struct {
		Runtime string `json:"runtime"`
	}
	if err := json.Unmarshal(request.Context, &context); err != nil {
		return selector
	}
	if context.Runtime != "" {
		selector.Runtime = context.Runtime
	}
	return selector
}
