// Package toolcatalog owns descriptive tool inventory. It never grants access
// or participates in approval execution.
package toolcatalog

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/authorization"
)

type Service struct {
	repo application.ToolCatalogRepository
	// source is the edition's external tool source, nil in an edition
	// without one.
	source    application.ToolSource
	clock     application.Clock
	logger    *slog.Logger
	mu        sync.Mutex
	refreshes map[string]*refreshState
}
type refreshState struct {
	mu      sync.Mutex
	version string
	at      time.Time
}

func NewService(repo application.ToolCatalogRepository, source application.ToolSource, clock application.Clock, logger *slog.Logger) *Service {
	return &Service{repo: repo, source: source, clock: clock, logger: logger, refreshes: map[string]*refreshState{}}
}

// RegisterSource installs the edition's external tool source. The hosted
// edition's gateway registers itself once its own services exist, after
// core's server is composed.
func (s *Service) RegisterSource(source application.ToolSource) { s.source = source }
func (s *Service) Report(ctx context.Context, p domain.Principal, report domain.ToolCatalogReport) error {
	if p.ActorType != domain.ActorAgent || p.AgentInstanceID == "" {
		return domain.ErrForbidden
	}
	if err := report.Validate(); err != nil {
		return err
	}
	for _, source := range report.Sources {
		if !domain.RuntimeToolSource(source.Source) {
			return fmt.Errorf("instances report only the runtime's own sources: %w", domain.ErrInvalid)
		}
	}
	return s.repo.SaveToolCatalog(ctx, application.SaveToolCatalogInput{OrganizationID: p.OrganizationID, AgentInstanceID: p.AgentInstanceID, Report: report, Now: s.clock.Now()})
}

type Query struct {
	AgentSlug              domain.AgentSlug
	Search, Source, Status string
	Limit, Offset          int
	Refresh                bool
	ID                     domain.ID
}

func (s *Service) List(ctx context.Context, p domain.Principal, q Query) (domain.ToolCatalogPage, error) {
	empty := domain.ToolCatalogPage{Tools: []domain.ToolCatalogEntry{}, Sources: []domain.ToolCatalogSource{}}
	if q.Offset < 0 || q.Limit < 0 || q.Limit > 500 || len(q.Search) > 200 || (q.Source != "" && !sourceFilterPattern.MatchString(q.Source)) || (q.Status != "" && q.Status != "available" && q.Status != "unavailable" && q.Status != "stale") {
		return empty, domain.ErrInvalid
	}
	page, err := s.read(ctx, p, q.AgentSlug, q.Refresh)
	if err != nil {
		return empty, err
	}
	filtered := []domain.ToolCatalogEntry{}
	for _, t := range page.Tools {
		if q.ID != "" && t.ID != q.ID {
			continue
		}
		if q.Source != "" && t.Source != q.Source {
			continue
		}
		if q.Status == "available" && !t.Available || q.Status == "unavailable" && t.Available || q.Status == "stale" && !t.Stale {
			continue
		}
		if q.Search != "" && !strings.Contains(strings.ToLower(t.Name+" "+t.DisplayName+" "+t.Description+" "+t.Server), strings.ToLower(q.Search)) {
			continue
		}
		filtered = append(filtered, t)
	}
	sort.Slice(filtered, func(i, j int) bool {
		a, b := filtered[i], filtered[j]
		return a.Name+"\x00"+string(a.AgentSlug)+"\x00"+string(a.ID) < b.Name+"\x00"+string(b.AgentSlug)+"\x00"+string(b.ID)
	})
	page.Total = len(filtered)
	start := min(q.Offset, len(filtered))
	limit := q.Limit
	if limit == 0 {
		limit = 50
	}
	end := min(start+limit, len(filtered))
	page.Tools = filtered[start:end]
	if end < len(filtered) {
		page.NextOffset = &end
	}
	return page, nil
}

// Definitions folds the reader's whole catalogue into its distinct
// definitions, one read for the pipeline editors. An external source's
// tools appear once per agent that sees them in the catalogue; here they
// appear once, with every place that offers them.
func (s *Service) Definitions(ctx context.Context, p domain.Principal) (domain.ToolDefinitionList, error) {
	page, err := s.read(ctx, p, "", false)
	if err != nil {
		return domain.ToolDefinitionList{}, err
	}
	return foldDefinitions(page), nil
}

func foldDefinitions(page domain.ToolCatalogPage) domain.ToolDefinitionList {
	out := domain.ToolDefinitionList{ToolDefinitions: []domain.CatalogToolDefinition{}, Servers: []string{}}
	index := map[string]int{}
	used := map[string]bool{}
	for _, tool := range page.Tools {
		key := tool.Name + "\x00" + tool.Server + "\x00" + tool.SchemaFingerprint + "\x00" + tool.Description
		at, known := index[key]
		if !known {
			at = len(out.ToolDefinitions)
			index[key] = at
			out.ToolDefinitions = append(out.ToolDefinitions, domain.CatalogToolDefinition{Name: tool.Name, Server: tool.Server, Description: tool.Description, InputSchema: tool.InputSchema, Uses: []domain.ToolDefinitionUse{}})
		}
		use := domain.ToolDefinitionUse{Runtime: tool.Runtime, InstanceName: tool.InstanceName, Available: tool.Available, Stale: tool.Stale}
		if useKey := fmt.Sprintf("%d\x00%+v", at, use); !used[useKey] {
			used[useKey] = true
			out.ToolDefinitions[at].Uses = append(out.ToolDefinitions[at].Uses, use)
		}
	}
	sort.SliceStable(out.ToolDefinitions, func(i, j int) bool {
		a, b := out.ToolDefinitions[i], out.ToolDefinitions[j]
		return a.Name+"\x00"+a.Server < b.Name+"\x00"+b.Server
	})
	servers := map[string]bool{}
	for _, source := range page.Sources {
		servers[source.Server] = true
	}
	for _, tool := range page.Tools {
		servers[tool.Server] = true
	}
	delete(servers, "")
	for server := range servers {
		out.Servers = append(out.Servers, server)
	}
	sort.Strings(out.Servers)
	return out
}

// read is the whole catalogue the reader's agent scope covers, or one
// agent's, after refreshing the edition's sources that are due.
func (s *Service) read(ctx context.Context, p domain.Principal, agentSlug domain.AgentSlug, refresh bool) (domain.ToolCatalogPage, error) {
	scope := authorization.Scope(p, domain.PermissionAgentRead)
	if scope.Empty() {
		return domain.ToolCatalogPage{}, domain.ErrForbidden
	}
	if agentSlug != "" {
		if err := authorization.Check(p, domain.PermissionAgentRead, domain.AgentResource(agentSlug)); err != nil {
			return domain.ToolCatalogPage{}, err
		}
	}
	in := application.ReadToolCatalogInput{OrganizationID: p.OrganizationID, Scope: scope, AgentSlug: agentSlug, Now: s.clock.Now()}
	if s.source != nil {
		sourceScope := application.ToolSourceScope{OrganizationID: p.OrganizationID, Scope: scope, AgentSlug: agentSlug}
		targets, err := s.source.Targets(ctx, sourceScope)
		if err != nil {
			return domain.ToolCatalogPage{}, err
		}
		var wg sync.WaitGroup
		slots := make(chan struct{}, 4)
		for _, target := range targets {
			wg.Add(1)
			go func(target application.ToolSourceTarget) {
				defer wg.Done()
				select {
				case slots <- struct{}{}:
					defer func() { <-slots }()
				case <-ctx.Done():
					return
				}
				if err := s.refresh(ctx, target, refresh); err != nil {
					s.logger.WarnContext(ctx, "external tool catalogue refresh failed; last successful definitions retained", "source_ref", target.SourceRef, "error", err)
				}
			}(target)
		}
		wg.Wait()
		if in.External, err = s.source.Visibility(ctx, sourceScope); err != nil {
			return domain.ToolCatalogPage{}, err
		}
	}
	return s.repo.ReadToolCatalog(ctx, in)
}

var sourceFilterPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// refresh discovers one external owner's tools unless its version is the
// one discovered less than five minutes ago. Every owner has its own
// lock so one slow listing never blocks another.
func (s *Service) refresh(ctx context.Context, target application.ToolSourceTarget, force bool) error {
	key := string(target.OrganizationID) + ":" + target.SourceRef
	s.mu.Lock()
	state := s.refreshes[key]
	if state == nil {
		state = &refreshState{}
		s.refreshes[key] = state
	}
	s.mu.Unlock()
	state.mu.Lock()
	defer state.mu.Unlock()
	version, err := s.source.State(ctx, target)
	if err != nil {
		return err
	}
	if !force && state.version == version && s.clock.Now().Sub(state.at) < 5*time.Minute {
		return nil
	}
	snapshot, discoveryErr := s.source.Discover(ctx, target)
	if snapshot.OrganizationID == "" {
		return discoveryErr
	}
	if err := s.repo.SaveToolCatalog(ctx, snapshot); err != nil {
		return err
	}
	state.version = version
	state.at = s.clock.Now()
	return discoveryErr
}
