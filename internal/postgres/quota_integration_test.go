package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
)

// Allocation quotas are enforced inside the allocating transaction under a
// per-organization lock. These tests throw more concurrent allocations at
// a quota than it allows and check that exactly the quota's worth land.
func TestAllocationQuotasHoldUnderConcurrency(t *testing.T) {
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
	orgID, ownerMembershipID := quotaTestOrganization(t, ctx, store, now, "acme")
	limited := func(value int64) application.AllocationQuota {
		return application.ResolvedQuota(application.LimitValue{Value: value})
	}

	t.Run("invitations reserve seats up to the quota", func(t *testing.T) {
		// The owner holds one seat, so a quota of three leaves two.
		results := runConcurrently(8, func(index int) error {
			_, err := store.CreateInvitation(ctx, application.CreateInvitationInput{
				OrganizationID: orgID, InvitationID: domain.ID(uuid.NewString()),
				Email: fmt.Sprintf("invitee-%d@example.com", index), Grants: []domain.AssignmentSpec{{RoleID: domain.RoleReviewerID, ScopeKind: domain.ScopeOrganization}},
				InvitedByMembershipID: ownerMembershipID, TokenHash: tokenHashOf(fmt.Sprintf("secret-%d", index)),
				ExpiresAt: now.Add(time.Hour), SeatQuota: limited(3), Now: now,
			})
			return err
		})
		succeeded, quotaExceeded := tally(t, results)
		if succeeded != 2 || quotaExceeded != 6 {
			t.Fatalf("succeeded=%d quota_exceeded=%d, want 2 and 6", succeeded, quotaExceeded)
		}
		var quota domain.QuotaExceeded
		for _, err := range results {
			if errors.As(err, &quota) {
				break
			}
		}
		if quota.Dimension != "seats" || quota.Limit != 3 || quota.Used != 3 {
			t.Fatalf("quota detail = %+v", quota)
		}
	})

	t.Run("agents are counted inside the creating transaction", func(t *testing.T) {
		results := runConcurrently(8, func(index int) error {
			_, err := store.CreateAgent(ctx, application.CreateAgentInput{Kind: domain.AgentService,
				OrganizationID: orgID, ActorID: ownerMembershipID, Name: fmt.Sprintf("agent-%d", index),
				AgentSlug:  domain.AgentSlug(fmt.Sprintf("agent-%d", index)),
				AgentQuota: limited(3), Now: now,
			})
			return err
		})
		if succeeded, quotaExceeded := tally(t, results); succeeded != 3 || quotaExceeded != 5 {
			t.Fatalf("succeeded=%d quota_exceeded=%d, want 3 and 5", succeeded, quotaExceeded)
		}
	})

	t.Run("directory provisioning past quota is rejected, not applied", func(t *testing.T) {
		scimOrg, _ := quotaTestOrganization(t, ctx, store, now, "scim-corp")
		commands := make([][]application.DirectoryCommandRecord, 3)
		for index := range 3 {
			data, _ := json.Marshal(map[string]any{
				"email": fmt.Sprintf("scim-%d@example.com", index), "display_name": "SCIM", "active": true, "removed": false,
			})
			commands[index] = []application.DirectoryCommandRecord{{Type: "upsert_user", ExternalID: fmt.Sprintf("scim-%d", index), Data: data}}
		}
		// Owner plus one: exactly one of three concurrent creates fits, and
		// the others answer the quota error the edition records against
		// its event rather than provisioning silently.
		results := runConcurrently(3, func(index int) error {
			return store.ApplyDirectoryCommands(ctx, application.ApplyDirectoryCommandsInput{
				OrganizationID: scimOrg, AdapterKey: "polis", ConnectionExternalID: "dir-1",
				Commands: commands[index], SeatQuota: limited(2), Now: now,
			})
		})
		refused := 0
		for _, err := range results {
			switch {
			case err == nil:
			case errors.Is(err, domain.ErrQuotaExceeded):
				refused++
			default:
				t.Fatalf("apply returned %v; a create past quota answers domain.ErrQuotaExceeded", err)
			}
		}
		if refused != 2 {
			t.Fatalf("refused creates = %d, want 2", refused)
		}
		members, err := store.ListMembers(ctx, application.ListMembersInput{OrganizationID: scimOrg})
		if err != nil {
			t.Fatal(err)
		}
		if len(members) != 2 {
			t.Fatalf("members after provisioning = %d, want owner plus one", len(members))
		}
	})

	t.Run("a shrunken quota blocks acceptance and reviving an expired invitation", func(t *testing.T) {
		shrinkOrg, ownerMembership := quotaTestOrganization(t, ctx, store, now, "shrink-inc")
		invitationIDs := make([]domain.ID, 2)
		for index := range invitationIDs {
			invitationIDs[index] = domain.ID(uuid.NewString())
			if _, err := store.CreateInvitation(ctx, application.CreateInvitationInput{
				OrganizationID: shrinkOrg, InvitationID: invitationIDs[index],
				Email: fmt.Sprintf("late-%d@example.com", index), Grants: []domain.AssignmentSpec{{RoleID: domain.RoleReviewerID, ScopeKind: domain.ScopeOrganization}},
				InvitedByMembershipID: ownerMembership, TokenHash: tokenHashOf(fmt.Sprintf("late-%d", index)),
				ExpiresAt: now.Add(time.Hour), SeatQuota: application.UnlimitedQuota(), Now: now,
			}); err != nil {
				t.Fatalf("create invitation %d: %v", index, err)
			}
		}
		accept := func(index int, quota application.AllocationQuota) error {
			_, err := store.AcceptInvitation(ctx, application.AcceptInvitationInput{
				InvitationID: invitationIDs[index], TokenHash: tokenHashOf(fmt.Sprintf("late-%d", index)),
				AssertedEmail: fmt.Sprintf("late-%d@example.com", index), NewUserID: domain.ID(uuid.NewString()),
				DisplayName: "Late", PasswordHash: "argon2id$test", ProviderKey: "local", Issuer: "withhuman:self-hosted",
				MembershipID: domain.ID(uuid.NewString()), SeatQuota: quota, Now: now.Add(time.Minute),
			})
			return err
		}
		// Owner plus two reservations is three; a quota of two refuses the
		// acceptance because the other reservation still counts.
		if err := accept(0, limited(2)); !errors.Is(err, domain.ErrQuotaExceeded) {
			t.Fatalf("accept under a shrunken quota = %v, want ErrQuotaExceeded", err)
		}
		// The accepted invitation's own reservation is not held against it.
		if err := accept(0, limited(3)); err != nil {
			t.Fatalf("accept within quota: %v", err)
		}

		// A live invitation renews freely; one that expired reserves a seat
		// again and must fit.
		if _, err := store.RotateInvitationToken(ctx, application.RotateInvitationTokenInput{
			OrganizationID: shrinkOrg, InvitationID: invitationIDs[1], TokenHash: tokenHashOf("renewed"),
			ExpiresAt: now.Add(2 * time.Hour), SeatQuota: limited(1), Now: now.Add(time.Minute),
		}); err != nil {
			t.Fatalf("renewing a live invitation should not consult the quota: %v", err)
		}
		if _, err := store.RotateInvitationToken(ctx, application.RotateInvitationTokenInput{
			OrganizationID: shrinkOrg, InvitationID: invitationIDs[1], TokenHash: tokenHashOf("revived"),
			ExpiresAt: now.Add(4 * time.Hour), SeatQuota: limited(2), Now: now.Add(3 * time.Hour),
		}); !errors.Is(err, domain.ErrQuotaExceeded) {
			t.Fatalf("reviving an expired invitation past quota = %v, want ErrQuotaExceeded", err)
		}
		if _, err := store.RotateInvitationToken(ctx, application.RotateInvitationTokenInput{
			OrganizationID: shrinkOrg, InvitationID: invitationIDs[1], TokenHash: tokenHashOf("revived"),
			ExpiresAt: now.Add(4 * time.Hour), SeatQuota: limited(3), Now: now.Add(3 * time.Hour),
		}); err != nil {
			t.Fatalf("reviving within quota: %v", err)
		}
	})
}

func quotaTestOrganization(t *testing.T, ctx context.Context, store *Store, now time.Time, slug string) (domain.ID, domain.ID) {
	t.Helper()
	orgID := domain.ID(uuid.NewString())
	ownerMembershipID := domain.ID(uuid.NewString())
	if _, err := store.ActivateOrganization(ctx, application.ActivateOrganizationInput{
		OrganizationID: orgID, OrganizationSlug: slug, OrganizationName: slug,
		UserID: domain.ID(uuid.NewString()), MembershipID: ownerMembershipID, DisplayName: "Owner",
		Email: "owner@" + slug + ".example.com", PasswordHash: "argon2id$test",
		AdmissionSource: domain.AdmissionManual, Now: now,
	}); err != nil {
		t.Fatalf("ActivateOrganization: %v", err)
	}
	return orgID, ownerMembershipID
}

func runConcurrently(n int, attempt func(index int) error) []error {
	results := make([]error, n)
	var ready, done sync.WaitGroup
	start := make(chan struct{})
	ready.Add(n)
	done.Add(n)
	for index := range n {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			results[index] = attempt(index)
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	return results
}

func tally(t *testing.T, results []error) (succeeded, quotaExceeded int) {
	t.Helper()
	for _, err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, domain.ErrQuotaExceeded):
			quotaExceeded++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	return succeeded, quotaExceeded
}
