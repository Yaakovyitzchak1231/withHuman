package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
	"github.com/withHumanAI/withHuman/internal/secrets"
)

func TestWebhookEndpointsAgainstRealPostgres(t *testing.T) {
	server := embeddedpgtest.Start(t)
	ctx := context.Background()
	store, err := Open(ctx, server.URL("postgres"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if err := db.Apply(ctx, store.Pool(), db.Migrations()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	recordAudit(t, store)
	sealer, err := secrets.NewSealer("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	store.WithSealer(sealer)

	now := time.Now().UTC().Truncate(time.Microsecond)
	orgID := domain.ID(uuid.NewString())
	ownerID := domain.ID(uuid.NewString())
	ownerMembershipID := domain.ID(uuid.NewString())
	if _, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
		OrganizationID: orgID, OrganizationSlug: "acme", OrganizationName: "Acme",
		UserID: ownerID, MembershipID: ownerMembershipID, DisplayName: "Owner",
		Email: "owner@example.com", PasswordHash: "argon2id$test",
		AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization: %v", err)
	}
	const key = "fraud-service"
	seal := func(secret string) []byte {
		sealed, err := sealer.Seal(application.WebhookSigningSecretLabel(orgID, key), secret)
		if err != nil {
			t.Fatal(err)
		}
		return sealed
	}
	createRevision := func(name, url, secret string, at time.Time) application.CreatedWebhookEndpointRevision {
		t.Helper()
		created, err := store.CreateWebhookEndpointRevision(ctx, application.CreateWebhookEndpointRevisionInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: orgID, EndpointKey: key, Name: name, URL: url,
			SealedSigningSecret: seal(secret), CreatedByActorID: ownerMembershipID, Now: at,
		})
		if err != nil {
			t.Fatalf("CreateWebhookEndpointRevision(%s): %v", name, err)
		}
		return created
	}
	openSecret := func() string {
		t.Helper()
		secret, err := store.OpenWebhookSigningSecret(ctx, application.OpenWebhookSigningSecretInput{OrganizationID: orgID, EndpointKey: key})
		if err != nil {
			t.Fatalf("OpenWebhookSigningSecret: %v", err)
		}
		return secret
	}

	// The first revision of a key mints the secret; later ones keep it.
	first := createRevision("fraud-service", "https://fraud.example.com/hooks/approval", "whsec_first", now)
	if !first.SecretCreated || first.Revision.Revision != 1 || first.Revision.IsActive || first.Revision.EndpointKey != key {
		t.Fatalf("unexpected first revision %+v", first)
	}
	second := createRevision("fraud-service-v2", "https://fraud-v2.example.com/hooks", "whsec_ignored", now.Add(time.Minute))
	if second.SecretCreated || second.Revision.Revision != 2 {
		t.Fatalf("unexpected second revision %+v", second)
	}
	if secret := openSecret(); secret != "whsec_first" {
		t.Fatalf("second revision replaced the key's secret: %q", secret)
	}
	// The sealed value is bound to its key: under another key's label it
	// does not open.
	if _, err := sealer.Open(application.WebhookSigningSecretLabel(orgID, "other"), seal("whsec_first")); err == nil {
		t.Fatal("sealed secret opened under another key's label")
	}

	summaries, err := store.ListWebhookEndpointSummaries(ctx, application.ListWebhookEndpointSummariesInput{OrganizationID: orgID})
	if err != nil || len(summaries) != 1 {
		t.Fatalf("ListWebhookEndpointSummaries: %+v %v", summaries, err)
	}
	if summary := summaries[0]; summary.EndpointKey != key || summary.Name != "fraud-service-v2" || summary.URL != "https://fraud-v2.example.com/hooks" ||
		summary.ActiveRevision != nil || summary.ArchivedAt != nil || summary.LatestRevision != 2 || summary.RevisionCount != 2 || !summary.SecretRotatedAt.Equal(now) {
		t.Fatalf("summary before activation = %+v", summary)
	}
	if _, err := store.GetActiveWebhookEndpointRevision(ctx, application.GetActiveWebhookEndpointRevisionInput{OrganizationID: orgID, EndpointKey: key}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("active revision before activation: %v", err)
	}
	if _, err := store.GetWebhookEndpointRevision(ctx, application.GetWebhookEndpointRevisionInput{OrganizationID: domain.ID(uuid.NewString()), EndpointKey: key, Revision: 1}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-organization read: got %v", err)
	}
	resolved, err := store.ResolveWebhookEndpoints(ctx, application.ResolveWebhookEndpointsInput{OrganizationID: orgID, EndpointKeys: []string{key, "gone"}})
	if err != nil || len(resolved.Missing) != 1 || resolved.Missing[0] != "gone" || len(resolved.Inactive) != 1 || resolved.Inactive[0] != key {
		t.Fatalf("ResolveWebhookEndpoints before activation = %+v %v", resolved, err)
	}

	// Activation requires the expected active revision: none at first.
	expected := int64(1)
	if _, err := store.ActivateWebhookEndpointRevision(ctx, application.ActivateWebhookEndpointRevisionInput{
		OrganizationID: orgID, EndpointKey: key, Revision: 2, ExpectedActiveRevision: &expected, ActorID: ownerMembershipID, Now: now,
	}); !errors.Is(err, domain.ErrPreconditionFailed) {
		t.Fatalf("activation with a wrong If-Match: %v", err)
	}
	activated, err := store.ActivateWebhookEndpointRevision(ctx, application.ActivateWebhookEndpointRevisionInput{
		OrganizationID: orgID, EndpointKey: key, Revision: 2, ActorID: ownerMembershipID, Now: now.Add(2 * time.Minute),
	})
	if err != nil || !activated.IsActive || activated.Revision != 2 {
		t.Fatalf("ActivateWebhookEndpointRevision: %+v %v", activated, err)
	}
	active, err := store.GetActiveWebhookEndpointRevision(ctx, application.GetActiveWebhookEndpointRevisionInput{OrganizationID: orgID, EndpointKey: key})
	if err != nil || active.Revision != 2 || active.URL != "https://fraud-v2.example.com/hooks" {
		t.Fatalf("GetActiveWebhookEndpointRevision: %+v %v", active, err)
	}
	// Rolling back to revision 1 flips the active row in one transaction.
	expected = 2
	rolledBack, err := store.ActivateWebhookEndpointRevision(ctx, application.ActivateWebhookEndpointRevisionInput{
		OrganizationID: orgID, EndpointKey: key, Revision: 1, ExpectedActiveRevision: &expected, ActorID: ownerMembershipID, Now: now.Add(3 * time.Minute),
	})
	if err != nil || !rolledBack.IsActive || rolledBack.Revision != 1 {
		t.Fatalf("rollback: %+v %v", rolledBack, err)
	}
	page, err := store.ListWebhookEndpointRevisions(ctx, application.ListWebhookEndpointRevisionsInput{OrganizationID: orgID, EndpointKey: key})
	revisions := page.Revisions
	if err != nil || len(revisions) != 2 || page.TotalCount != 2 || page.NextCursor != "" || revisions[0].Revision != 2 || revisions[0].IsActive || revisions[1].Revision != 1 || !revisions[1].IsActive {
		t.Fatalf("ListWebhookEndpointRevisions: %+v %v", page, err)
	}

	// Rotation replaces the key's secret for every revision at once.
	state, err := store.RotateWebhookSigningSecret(ctx, application.RotateWebhookSigningSecretInput{
		OrganizationID: orgID, EndpointKey: key, SealedSigningSecret: seal("whsec_second"),
		ActorID: ownerMembershipID, Now: now.Add(4 * time.Minute),
	})
	if err != nil || state.EndpointKey != key || !state.RotatedAt.Equal(now.Add(4*time.Minute)) {
		t.Fatalf("RotateWebhookSigningSecret: %+v %v", state, err)
	}
	if secret := openSecret(); secret != "whsec_second" {
		t.Fatalf("rotated secret did not replace the old one: %q", secret)
	}
	if _, err := store.RotateWebhookSigningSecret(ctx, application.RotateWebhookSigningSecretInput{
		OrganizationID: orgID, EndpointKey: "unknown", SealedSigningSecret: seal("x"), ActorID: ownerMembershipID, Now: now,
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("rotating an unknown key: %v", err)
	}

	// A webhook block in an active pipeline revision is a use; archiving
	// is refused until a revision without the block replaces it.
	if _, err := store.CreateAgent(ctx, application.CreateAgentInput{Kind: domain.AgentService,
		OrganizationID: orgID, ActorID: ownerMembershipID, Name: "billing-agent", AgentSlug: "billing",
		AgentQuota: application.UnlimitedQuota(), Now: now,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	config, _ := json.Marshal(map[string]any{
		"endpoint_key": key, "allowed_outcomes": []string{"next", "human"},
		"include": map[string]bool{"agent_reasoning": false, "context": false, "previous_metadata": true},
		"reason":  "Fraud service reviewed the request.",
	})
	revision, err := store.CreateRequestApprovalPipelineRevision(ctx, application.CreateRequestApprovalPipelineRevisionInput{
		ID: domain.ID(uuid.NewString()), OrganizationID: orgID, PipelineKey: domain.EntryPipelineKey, Name: entryPipelineName,
		Blocks: []application.RequestApprovalBlockSnapshotInput{{
			ID: domain.ID(uuid.NewString()), BlockKey: "fraud-check", Name: "Fraud service review", BlockType: "webhook",
			ConfigVersion: 1, Config: config, TimeoutMS: 5000, MaxAttempts: 3,
		}},
		CreatedByActorID: ownerMembershipID, Now: now,
	})
	if err != nil {
		t.Fatalf("CreateRequestApprovalPipelineRevision: %v", err)
	}
	if uses, err := store.ListWebhookEndpointUses(ctx, application.ListWebhookEndpointUsesInput{OrganizationID: orgID, EndpointKeys: []string{key}}); err != nil || len(uses[key]) != 0 {
		t.Fatalf("inactive revision counted as a use: %+v %v", uses, err)
	}
	if _, err := store.ActivateRequestApprovalPipelineRevision(ctx, application.ActivateRequestApprovalPipelineRevisionInput{
		OrganizationID: orgID, PipelineKey: domain.EntryPipelineKey, Revision: revision.Revision, ExpectedActiveRevision: 1, ActorID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("ActivateRequestApprovalPipelineRevision: %v", err)
	}
	// The batched lookup answers per key and omits keys nothing references.
	usesByKey, err := store.ListWebhookEndpointUses(ctx, application.ListWebhookEndpointUsesInput{OrganizationID: orgID, EndpointKeys: []string{key, "other"}})
	uses := usesByKey[key]
	if err != nil || len(usesByKey) != 1 || len(uses) != 1 || uses[0].PipelineKey != domain.EntryPipelineKey || uses[0].BlockKey != "fraud-check" || uses[0].PipelineID != revision.ID {
		t.Fatalf("ListWebhookEndpointUses: %+v %v", usesByKey, err)
	}
	archive := application.DeactivateWebhookEndpointInput{OrganizationID: orgID, EndpointKey: key, ExpectedActiveRevision: 1, ActorID: ownerMembershipID, Now: now.Add(5 * time.Minute)}
	var inUse domain.WebhookEndpointInUseError
	if _, err := store.DeactivateWebhookEndpoint(ctx, archive); !errors.As(err, &inUse) || inUse.EndpointKey != key || len(inUse.Uses) != 1 {
		t.Fatalf("archive while in use: got %v", err)
	}
	passThrough, err := store.CreateRequestApprovalPipelineRevision(ctx, application.CreateRequestApprovalPipelineRevisionInput{
		ID: domain.ID(uuid.NewString()), OrganizationID: orgID, PipelineKey: domain.EntryPipelineKey, Name: entryPipelineName, CreatedByActorID: ownerMembershipID, Now: now,
	})
	if err != nil {
		t.Fatalf("CreateRequestApprovalPipelineRevision (empty): %v", err)
	}
	if _, err := store.ActivateRequestApprovalPipelineRevision(ctx, application.ActivateRequestApprovalPipelineRevisionInput{
		OrganizationID: orgID, PipelineKey: domain.EntryPipelineKey, Revision: passThrough.Revision, ExpectedActiveRevision: revision.Revision, ActorID: ownerMembershipID, Now: now,
	}); err != nil {
		t.Fatalf("activate empty revision: %v", err)
	}
	archived, err := store.DeactivateWebhookEndpoint(ctx, archive)
	if err != nil || archived.IsActive || archived.Revision != 1 {
		t.Fatalf("archive after the block left the active revision: %+v %v", archived, err)
	}
	if _, err := store.GetActiveWebhookEndpointRevision(ctx, application.GetActiveWebhookEndpointRevisionInput{OrganizationID: orgID, EndpointKey: key}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("archived endpoint still active: %v", err)
	}
	// The pipeline revision that posts to the archived key cannot come back.
	if _, err := store.ActivateRequestApprovalPipelineRevision(ctx, application.ActivateRequestApprovalPipelineRevisionInput{
		OrganizationID: orgID, PipelineKey: domain.EntryPipelineKey, Revision: revision.Revision, ExpectedActiveRevision: passThrough.Revision, ActorID: ownerMembershipID, Now: now,
	}); !errors.Is(err, domain.ErrWebhookEndpointInactive) {
		t.Fatalf("rollback onto an archived endpoint = %v", err)
	}
	// Revisions are append-only: history survives an archive.
	if page, err := store.ListWebhookEndpointRevisions(ctx, application.ListWebhookEndpointRevisionsInput{OrganizationID: orgID, EndpointKey: key}); err != nil || len(page.Revisions) != 2 {
		t.Fatalf("history after archive: %+v %v", page, err)
	}
	// The archive stamps the key, which is what tells an archived endpoint
	// from a draft nobody activated; a fresh draft leaves the stamp alone.
	createRevision("fraud-service-v3", "https://fraud-v3.example.com/hooks", "whsec_ignored", now.Add(5*time.Minute+time.Second))
	summaries, err = store.ListWebhookEndpointSummaries(ctx, application.ListWebhookEndpointSummariesInput{OrganizationID: orgID})
	if err != nil || len(summaries) != 1 || summaries[0].ActiveRevision != nil || summaries[0].ArchivedAt == nil || !summaries[0].ArchivedAt.Equal(archive.Now) || summaries[0].LatestRevision != 3 {
		t.Fatalf("summary after archive = %+v %v", summaries, err)
	}
	// Activating any revision again ends the archive.
	if _, err := store.ActivateWebhookEndpointRevision(ctx, application.ActivateWebhookEndpointRevisionInput{
		OrganizationID: orgID, EndpointKey: key, Revision: 3, ActorID: ownerMembershipID, Now: now.Add(5*time.Minute + 2*time.Second),
	}); err != nil {
		t.Fatalf("activate after archive: %v", err)
	}
	summaries, err = store.ListWebhookEndpointSummaries(ctx, application.ListWebhookEndpointSummariesInput{OrganizationID: orgID})
	if err != nil || len(summaries) != 1 || summaries[0].ActiveRevision == nil || *summaries[0].ActiveRevision != 3 || summaries[0].ArchivedAt != nil {
		t.Fatalf("summary after reactivation = %+v %v", summaries, err)
	}
	if err := store.RecordWebhookEndpointTest(ctx, application.RecordWebhookEndpointTestInput{
		OrganizationID: orgID, RevisionID: first.Revision.ID, ActorID: ownerMembershipID,
		Data: json.RawMessage(`{"http_status":200}`), Now: now.Add(6 * time.Minute),
	}); err != nil {
		t.Fatalf("RecordWebhookEndpointTest: %v", err)
	}

	var eventTypes []string
	rows, err := store.Pool().Query(ctx, `SELECT event_type FROM test_audit_events WHERE organization_id = $1 AND subject_type = 'webhook_endpoint' ORDER BY sequence`, orgID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var eventType string
		_ = rows.Scan(&eventType)
		eventTypes = append(eventTypes, eventType)
	}
	rows.Close()
	want := []string{
		"webhook_endpoint.revision_created", "webhook_endpoint.revision_created",
		"webhook_endpoint.activated", "webhook_endpoint.activated",
		"webhook_endpoint.secret_rotated", "webhook_endpoint.deactivated",
		"webhook_endpoint.revision_created", "webhook_endpoint.activated", "webhook_endpoint.tested",
	}
	if len(eventTypes) != len(want) {
		t.Fatalf("audit events %v, want %v", eventTypes, want)
	}
	for index := range want {
		if eventTypes[index] != want[index] {
			t.Fatalf("audit events %v, want %v", eventTypes, want)
		}
	}

	t.Run("history pages by cursor", func(t *testing.T) {
		const pagedKey = "paged"
		for index := 0; index < 7; index++ {
			sealed, err := sealer.Seal(application.WebhookSigningSecretLabel(orgID, pagedKey), "whsec_paged")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateWebhookEndpointRevision(ctx, application.CreateWebhookEndpointRevisionInput{
				ID: domain.ID(uuid.NewString()), OrganizationID: orgID, EndpointKey: pagedKey,
				Name: "Paged", URL: "https://hooks.example.com/paged", SealedSigningSecret: sealed,
				CreatedByActorID: ownerMembershipID, Now: now.Add(time.Duration(10+index) * time.Minute),
			}); err != nil {
				t.Fatalf("CreateWebhookEndpointRevision(paged %d): %v", index, err)
			}
		}
		assertRevisionPaging(t, func(cursor string, limit int) (revisionPageOf, error) {
			page, err := store.ListWebhookEndpointRevisions(ctx, application.ListWebhookEndpointRevisionsInput{
				OrganizationID: orgID, EndpointKey: pagedKey, Limit: limit, Cursor: cursor,
			})
			result := revisionPageOf{nextCursor: page.NextCursor, totalCount: page.TotalCount}
			for _, revision := range page.Revisions {
				result.numbers = append(result.numbers, revision.Revision)
			}
			return result, err
		}, descending(7), 3)
	})
}
