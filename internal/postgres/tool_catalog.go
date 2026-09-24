package postgres

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

var _ application.ToolCatalogRepository = (*Store)(nil)

func (s *Store) SaveToolCatalog(ctx context.Context, in application.SaveToolCatalogInput) error {
	if err := in.Validate(); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	owner := string(in.AgentInstanceID)
	if in.SourceRef != "" {
		owner = in.SourceRef
	}
	scope := string(in.OrganizationID) + ":" + owner + ":" + in.Report.Runtime + ":" + in.Report.DiscoveryContext
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, scope); err != nil {
		return err
	}
	sourceIDs := []string{}
	for _, source := range in.Report.Sources {
		sourceID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(scope+":"+source.Source+":"+source.Server)).String()
		sourceIDs = append(sourceIDs, sourceID)
		_, err = tx.Exec(ctx, `INSERT INTO tool_catalog_sources(id,organization_id,agent_instance_id,source_ref,runtime,discovery_context,source,server,last_attempt_at,error)
 VALUES($1,$2,NULLIF($3,'')::uuid,NULLIF($4,''),$5,$6,$7,$8,$9,$10)
 ON CONFLICT(id) DO UPDATE SET last_attempt_at=EXCLUDED.last_attempt_at,error=EXCLUDED.error`, sourceID, in.OrganizationID, in.AgentInstanceID, in.SourceRef, in.Report.Runtime, in.Report.DiscoveryContext, source.Source, source.Server, in.Now, source.Error)
		if err != nil {
			return err
		}
		if !source.Complete {
			if source.Error == "" {
				if _, err = tx.Exec(ctx, `UPDATE tool_catalog_sources SET error='Tool discovery is incomplete.' WHERE id=$1 AND organization_id=$2`, sourceID, in.OrganizationID); err != nil {
					return err
				}
			}
			continue
		}
		if _, err = tx.Exec(ctx, `UPDATE tool_catalog_sources SET last_success_at=$3,source_version=$4 WHERE id=$1 AND organization_id=$2`, sourceID, in.OrganizationID, in.Now, in.SourceVersion); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE tool_catalog_tools SET available=false WHERE organization_id=$1 AND source_id=$2`, in.OrganizationID, sourceID); err != nil {
			return err
		}
		for _, tool := range source.Tools {
			var annotations []byte
			if tool.Annotations != nil {
				annotations, err = json.Marshal(tool.Annotations)
				if err != nil {
					return err
				}
			}
			toolID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(sourceID+":"+tool.Name)).String()
			_, err = tx.Exec(ctx, `INSERT INTO tool_catalog_tools(id,organization_id,source_id,name,display_name,description,input_schema,annotations,schema_fingerprint,available,last_seen_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,true,$10) ON CONFLICT(organization_id,source_id,name) DO UPDATE SET
 display_name=EXCLUDED.display_name,description=EXCLUDED.description,input_schema=EXCLUDED.input_schema,annotations=EXCLUDED.annotations,schema_fingerprint=EXCLUDED.schema_fingerprint,available=true,last_seen_at=EXCLUDED.last_seen_at`, toolID, in.OrganizationID, sourceID, tool.Name, tool.DisplayName, tool.Description, nullableJSON(tool.InputSchema), nullableJSON(annotations), tool.SchemaFingerprint(), in.Now)
			if err != nil {
				return err
			}
		}
	}
	if in.Report.Complete && in.AgentInstanceID != "" {
		_, err = tx.Exec(ctx, `UPDATE tool_catalog_sources SET error='',last_attempt_at=$6,last_success_at=$6 WHERE organization_id=$1 AND agent_instance_id=$2 AND runtime=$3 AND discovery_context=$4 AND NOT(id=ANY($5::uuid[]))`, in.OrganizationID, in.AgentInstanceID, in.Report.Runtime, in.Report.DiscoveryContext, sourceIDs, in.Now)
		if err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `UPDATE tool_catalog_tools SET available=false WHERE organization_id=$1 AND source_id IN
 (SELECT id FROM tool_catalog_sources WHERE organization_id=$1 AND agent_instance_id=$2 AND runtime=$3 AND discovery_context=$4 AND NOT(id=ANY($5::uuid[])))`, in.OrganizationID, in.AgentInstanceID, in.Report.Runtime, in.Report.DiscoveryContext, sourceIDs)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func nullableJSON(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

// catalogRow is one (source, tool) pair as the catalogue tables hold it;
// a source without tools comes back once with an empty tool.
type catalogRow struct {
	source      domain.ToolCatalogSource
	sourceRef   string
	version     string
	ownerLive   bool
	tool        domain.ToolCatalogEntry
	annotations []byte
	lastSeen    *time.Time
	hasTool     bool
}

const catalogColumns = `c.id::text,COALESCE(c.agent_instance_id::text,''),COALESCE(c.source_ref,''),c.source_version,c.runtime,c.discovery_context,c.source,c.server,c.last_attempt_at,c.last_success_at,c.error,
 COALESCE(t.id::text,''),COALESCE(t.name,''),COALESCE(t.display_name,''),COALESCE(t.description,''),t.input_schema,t.annotations,COALESCE(t.schema_fingerprint,''),COALESCE(t.available,false),t.last_seen_at`

func scanCatalogRow(scan func(dest ...any) error, extra ...any) (catalogRow, error) {
	var row catalogRow
	dest := []any{&row.source.ID, &row.source.AgentInstanceID, &row.sourceRef, &row.version, &row.source.Runtime, &row.source.DiscoveryContext, &row.source.Source, &row.source.Server, &row.source.LastAttemptAt, &row.source.LastSuccessAt, &row.source.Error,
		&row.tool.ID, &row.tool.Name, &row.tool.DisplayName, &row.tool.Description, &row.tool.InputSchema, &row.annotations, &row.tool.SchemaFingerprint, &row.tool.Available, &row.lastSeen}
	if err := scan(append(extra, dest...)...); err != nil {
		return row, err
	}
	row.hasTool = row.tool.ID != ""
	return row, nil
}

// ReadToolCatalog lists the catalogue the reader's agent scope covers.
// Instance-owned sources are the instances' own reports, available while
// the agent and instance are active with a live credential. External
// sources belong to the edition's tool source: which agents see them,
// with which tools, and whether the owner can serve now is the
// edition's live answer (in.External), and a snapshot counts as
// available only while its version is the one the owner reports, so a
// reconfigured owner reads as unavailable until it is discovered again.
// Nothing here is an access decision.
func (s *Store) ReadToolCatalog(ctx context.Context, in application.ReadToolCatalogInput) (domain.ToolCatalogPage, error) {
	out := domain.ToolCatalogPage{Tools: []domain.ToolCatalogEntry{}, Sources: []domain.ToolCatalogSource{}}
	if err := in.Validate(); err != nil {
		return out, err
	}
	rows, err := s.pool.Query(ctx, `SELECT a.slug,i.name,
  (a.status='active' AND i.status='active' AND EXISTS(SELECT 1 FROM agent_credentials k WHERE k.organization_id=c.organization_id AND k.agent_instance_id=i.id AND k.revoked_at IS NULL AND k.expires_at>$5)),
 `+catalogColumns+`
 FROM tool_catalog_sources c JOIN agent_instances i ON i.id=c.agent_instance_id AND i.organization_id=c.organization_id
 JOIN agents a ON a.id=i.agent_id AND a.organization_id=c.organization_id
 LEFT JOIN tool_catalog_tools t ON t.source_id=c.id AND t.organization_id=c.organization_id
 WHERE c.organization_id=$1 AND c.agent_instance_id IS NOT NULL AND ($2 OR a.slug=ANY($3::text[])) AND ($4='' OR a.slug=$4)
 ORDER BY a.slug,c.source,c.server,t.name`, in.OrganizationID, in.Scope.All, in.Scope.IDs(domain.ScopeAgent), in.AgentSlug, in.Now)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var slug domain.AgentSlug
		var instanceName string
		var ownerLive bool
		row, err := scanCatalogRow(rows.Scan, &slug, &instanceName, &ownerLive)
		if err != nil {
			return out, err
		}
		row.source.AgentSlug = slug
		row.tool.InstanceName = instanceName
		row.ownerLive = ownerLive
		if err := appendCatalogRow(&out, seen, row, slug); err != nil {
			return out, err
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if err := s.readExternalCatalog(ctx, in, &out, seen); err != nil {
		return out, err
	}
	out.Total = len(out.Tools)
	return out, nil
}

// readExternalCatalog adds the edition's sources: every snapshot whose
// owner some agent in the read sees, once per seeing agent, with the
// tools that agent may use.
func (s *Store) readExternalCatalog(ctx context.Context, in application.ReadToolCatalogInput, out *domain.ToolCatalogPage, seen map[string]bool) error {
	if len(in.External) == 0 {
		return nil
	}
	refs := make([]string, 0, len(in.External))
	byRef := map[string][]application.ToolSourceVisibility{}
	for _, visibility := range in.External {
		if in.AgentSlug != "" && visibility.AgentSlug != in.AgentSlug {
			continue
		}
		if !in.Scope.All && !slices.Contains(in.Scope.IDs(domain.ScopeAgent), string(visibility.AgentSlug)) {
			continue
		}
		if _, known := byRef[visibility.SourceRef]; !known {
			refs = append(refs, visibility.SourceRef)
		}
		byRef[visibility.SourceRef] = append(byRef[visibility.SourceRef], visibility)
	}
	if len(refs) == 0 {
		return nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+catalogColumns+`
 FROM tool_catalog_sources c LEFT JOIN tool_catalog_tools t ON t.source_id=c.id AND t.organization_id=c.organization_id
 WHERE c.organization_id=$1 AND c.source_ref=ANY($2::text[])
 ORDER BY c.source,c.server,t.name`, in.OrganizationID, refs)
	if err != nil {
		return err
	}
	defer rows.Close()
	var external []catalogRow
	for rows.Next() {
		row, err := scanCatalogRow(rows.Scan)
		if err != nil {
			return err
		}
		external = append(external, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, row := range external {
		visibilities := byRef[row.sourceRef]
		sort.Slice(visibilities, func(i, j int) bool { return visibilities[i].AgentSlug < visibilities[j].AgentSlug })
		for _, visibility := range visibilities {
			if row.hasTool && visibility.Tools != nil && !slices.Contains(visibility.Tools, row.tool.Name) {
				continue
			}
			row.ownerLive = visibility.Available && row.version == visibility.Version
			if err := appendCatalogRow(out, seen, row, visibility.AgentSlug); err != nil {
				return err
			}
		}
	}
	return nil
}

// appendCatalogRow records the row's source once per agent and its tool
// when it has one.
func appendCatalogRow(out *domain.ToolCatalogPage, seen map[string]bool, row catalogRow, slug domain.AgentSlug) error {
	source := row.source
	source.AgentSlug = slug
	key := string(source.ID) + ":" + string(slug)
	if !seen[key] {
		seen[key] = true
		out.Sources = append(out.Sources, source)
	}
	if !row.hasTool {
		return nil
	}
	tool := row.tool
	if len(row.annotations) > 0 {
		if err := json.Unmarshal(row.annotations, &tool.Annotations); err != nil {
			return err
		}
	}
	tool.AgentSlug = slug
	tool.AgentInstanceID = source.AgentInstanceID
	tool.DiscoveryContext = source.DiscoveryContext
	tool.Runtime = source.Runtime
	tool.Source = source.Source
	tool.Server = source.Server
	tool.LastAttemptAt = source.LastAttemptAt
	tool.Error = source.Error
	tool.Stale = source.Error != ""
	tool.Available = tool.Available && row.ownerLive
	if row.lastSeen != nil {
		tool.LastSeenAt = *row.lastSeen
	}
	out.Tools = append(out.Tools, tool)
	return nil
}
