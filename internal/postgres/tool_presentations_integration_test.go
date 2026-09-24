package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
	"github.com/withHumanAI/withHuman/toolpresentation"
)

func TestToolPresentationsAgainstRealPostgres(t *testing.T) {
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
	now := time.Now().UTC().Truncate(time.Microsecond)
	orgID, userID, membershipID := domain.ID(uuid.NewString()), domain.ID(uuid.NewString()), domain.ID(uuid.NewString())
	if _, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
		OrganizationID: orgID, OrganizationSlug: "acme", OrganizationName: "Acme",
		UserID: userID, MembershipID: membershipID, DisplayName: "Owner", Email: "owner@example.com",
		PasswordHash: "argon2id$test", AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization: %v", err)
	}
	otherOrg, otherUser, otherMembership := domain.ID(uuid.NewString()), domain.ID(uuid.NewString()), domain.ID(uuid.NewString())
	if _, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
		OrganizationID: otherOrg, OrganizationSlug: "other", OrganizationName: "Other",
		UserID: otherUser, MembershipID: otherMembership, DisplayName: "Other", Email: "other@example.com",
		PasswordHash: "argon2id$test", AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization other: %v", err)
	}

	entry := toolpresentation.Entry{Server: "crm", Tool: "update_deal", Spec: toolpresentation.Spec{
		Title:  "Update deal {{/deal_id}}",
		Fields: []toolpresentation.Field{{Path: "/notes", Kind: toolpresentation.KindProse, Label: "Notes"}},
	}}
	id := domain.ID(uuid.NewString())

	t.Run("create stores the normalized spec and audits the binding", func(t *testing.T) {
		record, err := store.CreateToolPresentation(ctx, application.CreateToolPresentationInput{
			ID: id, OrganizationID: orgID, ActorMembershipID: membershipID, Entry: entry, Now: now,
		})
		if err != nil {
			t.Fatalf("CreateToolPresentation: %v", err)
		}
		if record.ID != id || record.Entry.Server != "crm" || record.Entry.Runtime != "" || record.Entry.Spec.Title != entry.Spec.Title {
			t.Fatalf("record = %+v", record)
		}
		if record.Entry.Spec.Fields[0].Role != toolpresentation.RolePrimary {
			t.Fatalf("role not normalized: %+v", record.Entry.Spec.Fields[0])
		}
		var count int
		if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM test_audit_events WHERE organization_id = $1 AND event_type = 'tool_presentation.created' AND subject_id = $2`, orgID, id).Scan(&count); err != nil || count != 1 {
			t.Fatalf("audit count = %d, %v", count, err)
		}
	})

	t.Run("the same binding cannot be stored twice, but NULL parts do not collide across bindings", func(t *testing.T) {
		if _, err := store.CreateToolPresentation(ctx, application.CreateToolPresentationInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: orgID, ActorMembershipID: membershipID, Entry: entry, Now: now,
		}); !errors.Is(err, domain.ErrToolPresentationExists) {
			t.Fatalf("duplicate = %v, want exists", err)
		}
		anyServer := entry
		anyServer.Server = ""
		if _, err := store.CreateToolPresentation(ctx, application.CreateToolPresentationInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: orgID, ActorMembershipID: membershipID, Entry: anyServer, Now: now,
		}); err != nil {
			t.Fatalf("any-server binding: %v", err)
		}
		if _, err := store.CreateToolPresentation(ctx, application.CreateToolPresentationInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: orgID, ActorMembershipID: membershipID, Entry: anyServer, Now: now,
		}); !errors.Is(err, domain.ErrToolPresentationExists) {
			t.Fatalf("duplicate any-server = %v, want exists", err)
		}
		// Another organization may use the same binding.
		if _, err := store.CreateToolPresentation(ctx, application.CreateToolPresentationInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: otherOrg, ActorMembershipID: otherMembership, Entry: entry, Now: now,
		}); err != nil {
			t.Fatalf("other organization: %v", err)
		}
	})

	t.Run("list is organization scoped and ordered by tool", func(t *testing.T) {
		records, err := store.ListToolPresentations(ctx, application.ListToolPresentationsInput{OrganizationID: orgID})
		if err != nil || len(records) != 2 {
			t.Fatalf("ListToolPresentations = %d, %v", len(records), err)
		}
		for _, record := range records {
			if record.OrganizationID != orgID {
				t.Fatalf("foreign record %+v", record)
			}
		}
	})

	t.Run("update replaces binding and spec, refuses collisions, and audits", func(t *testing.T) {
		updated := entry
		updated.Runtime = "claude-code"
		updated.Spec.Title = "Deal update"
		record, err := store.UpdateToolPresentation(ctx, application.UpdateToolPresentationInput{
			ID: id, OrganizationID: orgID, ActorMembershipID: membershipID, Entry: updated, Now: now.Add(time.Minute),
		})
		if err != nil || record.Entry.Runtime != "claude-code" || record.Entry.Spec.Title != "Deal update" || !record.UpdatedAt.After(record.CreatedAt) {
			t.Fatalf("UpdateToolPresentation = %+v, %v", record, err)
		}
		collide := entry
		collide.Server = ""
		if _, err := store.UpdateToolPresentation(ctx, application.UpdateToolPresentationInput{
			ID: id, OrganizationID: orgID, ActorMembershipID: membershipID, Entry: collide, Now: now,
		}); !errors.Is(err, domain.ErrToolPresentationExists) {
			t.Fatalf("collision = %v, want exists", err)
		}
		if _, err := store.UpdateToolPresentation(ctx, application.UpdateToolPresentationInput{
			ID: id, OrganizationID: otherOrg, ActorMembershipID: otherMembership, Entry: updated, Now: now,
		}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("cross-organization update = %v, want not found", err)
		}
		var count int
		if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM test_audit_events WHERE organization_id = $1 AND event_type = 'tool_presentation.updated'`, orgID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("update audit count = %d, %v", count, err)
		}
	})

	t.Run("delete is scoped, audited, and not found the second time", func(t *testing.T) {
		if err := store.DeleteToolPresentation(ctx, application.DeleteToolPresentationInput{
			ID: id, OrganizationID: otherOrg, ActorMembershipID: otherMembership, Now: now,
		}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("cross-organization delete = %v, want not found", err)
		}
		if err := store.DeleteToolPresentation(ctx, application.DeleteToolPresentationInput{
			ID: id, OrganizationID: orgID, ActorMembershipID: membershipID, Now: now,
		}); err != nil {
			t.Fatalf("DeleteToolPresentation: %v", err)
		}
		if err := store.DeleteToolPresentation(ctx, application.DeleteToolPresentationInput{
			ID: id, OrganizationID: orgID, ActorMembershipID: membershipID, Now: now,
		}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("second delete = %v, want not found", err)
		}
		if _, err := store.GetToolPresentation(ctx, application.GetToolPresentationInput{OrganizationID: orgID, ID: id}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("get after delete = %v", err)
		}
		var count int
		if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM test_audit_events WHERE organization_id = $1 AND event_type = 'tool_presentation.deleted'`, orgID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("delete audit count = %d, %v", count, err)
		}
	})

	t.Run("an invalid spec never reaches the table", func(t *testing.T) {
		bad := entry
		bad.Spec.Fields = []toolpresentation.Field{{Path: "notes", Kind: toolpresentation.KindText}}
		if _, err := store.CreateToolPresentation(ctx, application.CreateToolPresentationInput{
			ID: domain.ID(uuid.NewString()), OrganizationID: orgID, ActorMembershipID: membershipID, Entry: bad, Now: now,
		}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("invalid spec = %v", err)
		}
	})
}
