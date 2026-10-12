package aapdelivery_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/authn"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/domain"
)

// capturedAudit keeps the events the store hands its audit sink.
type capturedAudit struct{ events []domain.AuditEvent }

func (c *capturedAudit) Append(_ context.Context, _ db.Execer, event domain.AuditEvent) error {
	c.events = append(c.events, event)
	return nil
}

func (f *fixture) owner() domain.Principal {
	return domain.Principal{ActorType: domain.ActorHuman, ActorID: f.user, OrganizationID: f.org, MembershipID: f.member,
		Auth: domain.AuthContext{Method: "local_password", Assurance: "single_factor", AuthenticatedAt: f.clock.Now()}}.
		WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionAgentCredentialIssue}))
}

func (f *fixture) agentID(slug domain.AgentSlug) domain.ID {
	agent, err := f.store.GetAgentBySlug(f.ctx, application.GetAgentBySlugInput{OrganizationID: f.org, AgentSlug: slug})
	if err != nil {
		f.t.Fatal(err)
	}
	return agent.ID
}

// expect configures the harness with a registration the app issued, the
// way a person configures their receiver.
func (r *receiver) expect(registration authn.InstanceRegistration) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(registration.SigningSecret, "whsec_"))
	if err != nil || len(key) != 32 {
		r.t.Fatalf("issued secret %q is not a whsec_ key", registration.SigningSecret)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registrationID, r.key = string(registration.RegistrationID), key
}

// A registration started in the app holds only the sealed secret, fails
// verification until the receiver is configured with it, then creates an
// instance whose approvals get the asynchronous validity window and whose
// decisions reach the receiver. Personal and service agents both work.
func TestAppRegistrationCreatesAnInstanceWithVerifiedDelivery(t *testing.T) {
	f := newFixture(t)
	if _, err := f.store.CreateAgent(f.ctx, application.CreateAgentInput{OrganizationID: f.org, ActorID: f.member, Name: "Coding", AgentSlug: "coding", Kind: domain.AgentPersonal, AgentQuota: application.UnlimitedQuota(), Now: f.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	audit := &capturedAudit{}
	f.store.WithAuditSink(audit)
	connection, err := pgx.Connect(f.ctx, f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(f.ctx)

	for _, tc := range []struct {
		slug  domain.AgentSlug
		owner domain.ID
	}{{"harness", ""}, {"coding", f.member}} {
		t.Run(string(tc.slug), func(t *testing.T) {
			harness, _ := newReceiver(t, 7, false)
			server := httptest.NewServer(harness)
			defer server.Close()
			agentID := f.agentID(tc.slug)

			registration, err := f.auth.CreateAgentInstanceRegistration(f.ctx, f.owner(), agentID)
			if err != nil {
				t.Fatal(err)
			}
			var sealed []byte
			var reservedID string
			if err := connection.QueryRow(f.ctx, `SELECT sealed_signing_secret, agent_instance_id::text FROM agent_instance_registrations WHERE id = $1`,
				registration.RegistrationID).Scan(&sealed, &reservedID); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(sealed), strings.TrimPrefix(registration.SigningSecret, "whsec_")) {
				t.Fatal("the registration's secret is stored in plaintext")
			}

			delivery := authn.InstanceRegistrationDelivery{RegistrationID: registration.RegistrationID, URL: server.URL + "/aap/events"}
			if _, err := f.auth.CreateAgentInstanceWithDelivery(f.ctx, f.owner(), agentID, "receiver-"+string(tc.slug), nil, delivery); !errors.Is(err, domain.ErrWebhookVerificationFailed) {
				t.Fatalf("an unconfigured receiver must fail verification, got %v", err)
			}
			harness.expect(registration)
			created, err := f.auth.CreateAgentInstanceWithDelivery(f.ctx, f.owner(), agentID, "receiver-"+string(tc.slug), nil, delivery)
			if err != nil {
				t.Fatal(err)
			}
			instance := created.AgentInstance
			if string(instance.ID) != reservedID || instance.MembershipID != tc.owner {
				t.Fatalf("instance = %+v, want the reserved id %s and owner %q", instance, reservedID, tc.owner)
			}
			if instance.Delivery == nil || instance.Delivery.URL != delivery.URL || instance.Delivery.RegistrationID != registration.RegistrationID {
				t.Fatalf("instance delivery = %+v", instance.Delivery)
			}
			if len(harness.verifications) != 1 || harness.verifications[0]["instance_id"] != reservedID {
				t.Fatalf("verifications = %+v", harness.verifications)
			}
			var remaining int
			if err := connection.QueryRow(f.ctx, `SELECT count(*) FROM agent_instance_registrations WHERE id = $1`, registration.RegistrationID).Scan(&remaining); err != nil || remaining != 0 {
				t.Fatalf("registration rows after completion = %d, %v", remaining, err)
			}
			var audited struct {
				Delivery domain.AgentInstanceDelivery `json:"delivery"`
			}
			for _, event := range audit.events {
				if event.EventType == "agent_instance.registered" && event.SubjectID == instance.ID {
					json.Unmarshal(event.Data, &audited)
				}
			}
			if audited.Delivery.URL != delivery.URL || audited.Delivery.RegistrationID != registration.RegistrationID {
				t.Fatalf("registration audit delivery = %+v", audited.Delivery)
			}
			if _, err := f.auth.CreateAgentInstanceWithDelivery(f.ctx, f.owner(), agentID, "again-"+string(tc.slug), nil, delivery); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("a used registration = %v, want not found", err)
			}

			approved := f.create(created.Credential, "issue_refund")
			current, err := f.approvals.Get(f.ctx, f.principal(created.Credential), approved.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Decision == nil || current.Decision.ExpiresAt == nil || !current.Decision.ExpiresAt.Equal(current.Decision.DecidedAt.Add(30*time.Minute)) {
				t.Fatalf("approval expiry = %+v, want decided_at + 30m", current.Decision)
			}
			f.dispatch()
			if harness.acknowledged() != 1 {
				t.Fatalf("receiver acknowledged %d events, want 1", harness.acknowledged())
			}
		})
	}
}

// An expired registration refuses completion and is pruned with the AAP
// state.
func TestAppRegistrationExpiresAndIsPruned(t *testing.T) {
	f := newFixture(t)
	harness, _ := newReceiver(t, 9, false)
	server := httptest.NewServer(harness)
	defer server.Close()
	agentID := f.agentID("harness")
	registration, err := f.auth.CreateAgentInstanceRegistration(f.ctx, f.owner(), agentID)
	if err != nil {
		t.Fatal(err)
	}
	harness.expect(registration)
	f.clock.offset.Store(int64(application.InstanceRegistrationTTL))
	delivery := authn.InstanceRegistrationDelivery{RegistrationID: registration.RegistrationID, URL: server.URL}
	if _, err := f.auth.CreateAgentInstanceWithDelivery(f.ctx, f.owner(), agentID, "late", nil, delivery); !errors.Is(err, domain.ErrInstanceRegistrationExpired) {
		t.Fatalf("expired registration = %v", err)
	}
	if len(harness.verifications) != 0 {
		t.Fatal("an expired registration reached the receiver")
	}
	if err := f.store.PruneAAPState(f.ctx, application.PruneAAPStateInput{Now: f.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.GetAgentInstanceRegistration(f.ctx, application.GetAgentInstanceRegistrationInput{
		OrganizationID: f.org, AgentID: agentID, RegistrationID: registration.RegistrationID, ActorType: domain.ActorHuman, ActorID: f.user,
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("pruned registration = %v, want not found", err)
	}
}
