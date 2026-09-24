package webhookendpoints

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/secrets"
	"github.com/withHumanAI/withHuman/internal/webhookdelivery"
)

// memoryEndpoints is an in-memory revision store keyed by endpoint key,
// with one sealed secret per key.
type memoryEndpoints struct {
	revisions map[string][]domain.WebhookEndpointRevision
	sealed    map[string][]byte
	rotatedAt map[string]time.Time
	uses      map[string][]domain.PipelineUse
	tests     []application.RecordWebhookEndpointTestInput
	sealer    *secrets.Sealer
}

func newMemoryEndpoints(sealer *secrets.Sealer) *memoryEndpoints {
	return &memoryEndpoints{
		revisions: map[string][]domain.WebhookEndpointRevision{}, sealed: map[string][]byte{},
		rotatedAt: map[string]time.Time{}, uses: map[string][]domain.PipelineUse{}, sealer: sealer,
	}
}

func (m *memoryEndpoints) CreateWebhookEndpointRevision(_ context.Context, input application.CreateWebhookEndpointRevisionInput) (application.CreatedWebhookEndpointRevision, error) {
	if err := input.Validate(); err != nil {
		return application.CreatedWebhookEndpointRevision{}, err
	}
	_, exists := m.sealed[input.EndpointKey]
	if !exists {
		m.sealed[input.EndpointKey], m.rotatedAt[input.EndpointKey] = input.SealedSigningSecret, input.Now
	}
	revision := domain.WebhookEndpointRevision{
		ID: input.ID, OrganizationID: input.OrganizationID, EndpointKey: input.EndpointKey,
		Revision: int64(len(m.revisions[input.EndpointKey]) + 1), Name: input.Name, URL: input.URL,
		CreatedByActorID: input.CreatedByActorID, CreatedAt: input.Now,
	}
	m.revisions[input.EndpointKey] = append(m.revisions[input.EndpointKey], revision)
	return application.CreatedWebhookEndpointRevision{Revision: revision, SecretCreated: !exists}, nil
}

func (m *memoryEndpoints) ListWebhookEndpointSummaries(context.Context, application.ListWebhookEndpointSummariesInput) ([]domain.WebhookEndpointSummary, error) {
	out := []domain.WebhookEndpointSummary{}
	for key, revisions := range m.revisions {
		latest := revisions[len(revisions)-1]
		summary := domain.WebhookEndpointSummary{EndpointKey: key, Name: latest.Name, URL: latest.URL, LatestRevision: latest.Revision, RevisionCount: int64(len(revisions)), SecretRotatedAt: m.rotatedAt[key]}
		for _, revision := range revisions {
			if revision.IsActive {
				active := revision.Revision
				summary.ActiveRevision = &active
			}
		}
		out = append(out, summary)
	}
	return out, nil
}

func (m *memoryEndpoints) GetWebhookEndpointRevision(_ context.Context, input application.GetWebhookEndpointRevisionInput) (domain.WebhookEndpointRevision, error) {
	for _, revision := range m.revisions[input.EndpointKey] {
		if revision.Revision == input.Revision && revision.OrganizationID == input.OrganizationID {
			return revision, nil
		}
	}
	return domain.WebhookEndpointRevision{}, domain.ErrNotFound
}

func (m *memoryEndpoints) ListWebhookEndpointRevisions(_ context.Context, input application.ListWebhookEndpointRevisionsInput) (application.RevisionPage[domain.WebhookEndpointRevision], error) {
	revisions := m.revisions[input.EndpointKey]
	return application.RevisionPage[domain.WebhookEndpointRevision]{Revisions: revisions, TotalCount: int64(len(revisions))}, nil
}

func (m *memoryEndpoints) GetActiveWebhookEndpointRevision(_ context.Context, input application.GetActiveWebhookEndpointRevisionInput) (domain.WebhookEndpointRevision, error) {
	for _, revision := range m.revisions[input.EndpointKey] {
		if revision.IsActive && revision.OrganizationID == input.OrganizationID {
			return revision, nil
		}
	}
	return domain.WebhookEndpointRevision{}, domain.ErrNotFound
}

func (m *memoryEndpoints) ActivateWebhookEndpointRevision(_ context.Context, input application.ActivateWebhookEndpointRevisionInput) (domain.WebhookEndpointRevision, error) {
	revisions := m.revisions[input.EndpointKey]
	var activated domain.WebhookEndpointRevision
	for index := range revisions {
		revisions[index].IsActive = revisions[index].Revision == input.Revision
		if revisions[index].IsActive {
			activated = revisions[index]
		}
	}
	if activated.ID == "" {
		return domain.WebhookEndpointRevision{}, domain.ErrNotFound
	}
	return activated, nil
}

func (m *memoryEndpoints) DeactivateWebhookEndpoint(_ context.Context, input application.DeactivateWebhookEndpointInput) (domain.WebhookEndpointRevision, error) {
	if uses := m.uses[input.EndpointKey]; len(uses) > 0 {
		return domain.WebhookEndpointRevision{}, domain.WebhookEndpointInUseError{EndpointKey: input.EndpointKey, Uses: uses}
	}
	revisions := m.revisions[input.EndpointKey]
	for index := range revisions {
		if revisions[index].IsActive {
			revisions[index].IsActive = false
			return revisions[index], nil
		}
	}
	return domain.WebhookEndpointRevision{}, domain.ErrPreconditionFailed
}

func (m *memoryEndpoints) ListWebhookEndpointUses(_ context.Context, input application.ListWebhookEndpointUsesInput) (map[string][]domain.PipelineUse, error) {
	uses := map[string][]domain.PipelineUse{}
	for _, key := range input.EndpointKeys {
		if len(m.uses[key]) > 0 {
			uses[key] = m.uses[key]
		}
	}
	return uses, nil
}

func (m *memoryEndpoints) ResolveWebhookEndpoints(_ context.Context, input application.ResolveWebhookEndpointsInput) (application.ResolvedWebhookEndpoints, error) {
	resolved := application.ResolvedWebhookEndpoints{Missing: []string{}, Inactive: []string{}}
	for _, key := range input.EndpointKeys {
		revisions, exists := m.revisions[key]
		if !exists {
			resolved.Missing = append(resolved.Missing, key)
			continue
		}
		active := false
		for _, revision := range revisions {
			active = active || revision.IsActive
		}
		if !active {
			resolved.Inactive = append(resolved.Inactive, key)
		}
	}
	return resolved, nil
}

func (m *memoryEndpoints) RotateWebhookSigningSecret(_ context.Context, input application.RotateWebhookSigningSecretInput) (application.WebhookSigningSecretState, error) {
	if _, exists := m.sealed[input.EndpointKey]; !exists {
		return application.WebhookSigningSecretState{}, domain.ErrNotFound
	}
	m.sealed[input.EndpointKey], m.rotatedAt[input.EndpointKey] = input.SealedSigningSecret, input.Now
	return application.WebhookSigningSecretState{EndpointKey: input.EndpointKey, RotatedAt: input.Now}, nil
}

func (m *memoryEndpoints) OpenWebhookSigningSecret(_ context.Context, input application.OpenWebhookSigningSecretInput) (string, error) {
	sealed, exists := m.sealed[input.EndpointKey]
	if !exists {
		return "", domain.ErrNotFound
	}
	return m.sealer.Open(application.WebhookSigningSecretLabel(input.OrganizationID, input.EndpointKey), sealed)
}

func (m *memoryEndpoints) RecordWebhookEndpointTest(_ context.Context, input application.RecordWebhookEndpointTestInput) error {
	m.tests = append(m.tests, input)
	return nil
}

type scriptedDeliverer struct {
	response   webhookdelivery.Response
	err        error
	deliveries []webhookdelivery.Delivery
}

func (d *scriptedDeliverer) Deliver(_ context.Context, delivery webhookdelivery.Delivery) (webhookdelivery.Response, error) {
	d.deliveries = append(d.deliveries, delivery)
	return d.response, d.err
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

func manager() domain.Principal {
	return domain.Principal{ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "organization-1", MembershipID: "membership-1"}.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionWebhookRead, domain.PermissionWebhookWrite}))
}

func newTestService(t *testing.T, deliverer Deliverer) (*Service, *memoryEndpoints) {
	t.Helper()
	sealer, err := secrets.NewSealer("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	repository := newMemoryEndpoints(sealer)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewService(repository, sealer, webhookdelivery.Policy{}, deliverer, fixedClock{now: time.Unix(1_000, 0).UTC()}, logger), repository
}

func TestFirstRevisionMintsTheSecretAndShowsItOnce(t *testing.T) {
	service, repository := newTestService(t, &scriptedDeliverer{})
	first, err := service.CreateRevision(context.Background(), manager(), "fraud-service", " Fraud service ", "https://fraud.example.com/hooks")
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision.Name != "Fraud service" || first.Revision.Revision != 1 || len(first.SigningSecret) < 40 || first.SigningSecret[:6] != "whsec_" {
		t.Fatalf("unexpected first revision: %+v", first)
	}
	opened, err := repository.OpenWebhookSigningSecret(context.Background(), application.OpenWebhookSigningSecretInput{OrganizationID: "organization-1", EndpointKey: "fraud-service"})
	if err != nil || opened != first.SigningSecret {
		t.Fatalf("stored secret does not open to the shown secret: %q %v", opened, err)
	}
	// A later revision changes the destination, not the secret.
	second, err := service.CreateRevision(context.Background(), manager(), "fraud-service", "Fraud service", "https://fraud-v2.example.com/hooks")
	if err != nil || second.Revision.Revision != 2 || second.SigningSecret != "" {
		t.Fatalf("second revision: %+v %v", second, err)
	}
	if opened, _ := repository.OpenWebhookSigningSecret(context.Background(), application.OpenWebhookSigningSecretInput{OrganizationID: "organization-1", EndpointKey: "fraud-service"}); opened != first.SigningSecret {
		t.Fatal("second revision replaced the secret")
	}

	if _, err := service.CreateRevision(context.Background(), manager(), "Fraud Service", "x", "https://fraud.example.com/"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid key accepted: %v", err)
	}
	if _, err := service.CreateRevision(context.Background(), manager(), "internal", "internal", "https://10.0.0.5/hooks"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("egress policy not applied on create: %v", err)
	}
	if _, err := service.CreateRevision(context.Background(), manager(), "plain", "plain", "http://fraud.example.com/hooks"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("insecure URL accepted: %v", err)
	}
	reader := manager()
	reader = reader.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionWebhookRead}))
	if _, err := service.CreateRevision(context.Background(), reader, "x", "x", "https://fraud.example.com/"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a reader could create a revision: %v", err)
	}
	if listed, err := service.ListEndpoints(context.Background(), reader, 10); err != nil || len(listed) != 1 || listed[0].Summary.ActiveRevision != nil || len(listed[0].Uses) != 0 {
		t.Fatalf("a reader cannot list endpoints: %v %v", listed, err)
	}
	if page, err := service.ListRevisions(context.Background(), reader, "fraud-service", 10, ""); err != nil || len(page.Revisions) != 2 || page.TotalCount != 2 {
		t.Fatalf("a reader cannot read revision history: %v %v", page, err)
	}
	if _, err := service.ActivateRevision(context.Background(), reader, "fraud-service", 2, nil); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a reader could activate a revision: %v", err)
	}

	activated, err := service.ActivateRevision(context.Background(), manager(), "fraud-service", 2, nil)
	if err != nil || !activated.IsActive || activated.Revision != 2 {
		t.Fatalf("activation: %+v %v", activated, err)
	}
	if active, err := service.GetActiveEndpoint(context.Background(), reader, "fraud-service"); err != nil || active.URL != "https://fraud-v2.example.com/hooks" {
		t.Fatalf("active endpoint: %+v %v", active, err)
	}
	rotated, err := service.RotateSecret(context.Background(), manager(), "fraud-service")
	if err != nil || rotated.SigningSecret == first.SigningSecret || rotated.EndpointKey != "fraud-service" {
		t.Fatalf("rotation: %+v %v", rotated, err)
	}
	if _, err := service.RotateSecret(context.Background(), manager(), "unknown"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("rotating an unknown key: %v", err)
	}

	repository.uses["fraud-service"] = []domain.PipelineUse{{PipelineID: "p1", Revision: 3, BlockKey: "fraud-check"}}
	var inUse domain.WebhookEndpointInUseError
	if err := service.Deactivate(context.Background(), manager(), "fraud-service", 2); !errors.As(err, &inUse) || len(inUse.Uses) != 1 {
		t.Fatalf("archive while used: %v", err)
	}
	if uses, err := service.ListUses(context.Background(), reader, "fraud-service"); err != nil || len(uses) != 1 {
		t.Fatalf("uses: %v %v", uses, err)
	}
	if listed, err := service.ListEndpoints(context.Background(), reader, 10); err != nil || len(listed[0].Uses) != 1 {
		t.Fatalf("list does not carry uses: %+v %v", listed, err)
	}
	repository.uses["fraud-service"] = nil
	if err := service.Deactivate(context.Background(), manager(), "fraud-service", 2); err != nil {
		t.Fatalf("archive: %v", err)
	}
}

func TestUnconfiguredServiceRefusesWrites(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := NewService(newMemoryEndpoints(nil), nil, webhookdelivery.Policy{}, nil, fixedClock{now: time.Unix(1, 0)}, logger)
	if service.Configured() {
		t.Fatal("service without a sealer reports configured")
	}
	if _, err := service.CreateRevision(context.Background(), manager(), "x", "x", "https://fraud.example.com/"); !errors.Is(err, domain.ErrWebhookUnconfigured) {
		t.Fatalf("got %v, want unconfigured", err)
	}
	if _, err := service.ActivateRevision(context.Background(), manager(), "x", 1, nil); !errors.Is(err, domain.ErrWebhookUnconfigured) {
		t.Fatalf("got %v, want unconfigured", err)
	}
}

func TestTestDeliveryTargetsOneRevisionAndAuditsWhatCameBack(t *testing.T) {
	deliverer := &scriptedDeliverer{response: webhookdelivery.Response{StatusCode: http.StatusOK, Body: []byte(`{"outcome":"next","reason":"ok"}`), Duration: 30 * time.Millisecond}}
	service, repository := newTestService(t, deliverer)
	first, err := service.CreateRevision(context.Background(), manager(), "fraud-service", "Fraud service", "https://fraud.example.com/hooks")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateRevision(context.Background(), manager(), "fraud-service", "Fraud service", "https://fraud-v2.example.com/hooks"); err != nil {
		t.Fatal(err)
	}
	// An inactive draft can be tested before it is activated.
	result, err := service.Test(context.Background(), manager(), "fraud-service", 2)
	if err != nil || result.Problem != "" || result.Outcome != "next" || result.Reason != "ok" || result.StatusCode != 200 || result.DurationMS != 30 {
		t.Fatalf("test result: %+v %v", result, err)
	}
	delivery := deliverer.deliveries[0]
	if delivery.Secret != first.SigningSecret || delivery.Timeout != testDeliveryTimeout || delivery.URL != "https://fraud-v2.example.com/hooks" {
		t.Fatalf("delivery: %+v", delivery)
	}
	var payload map[string]any
	_ = json.Unmarshal(delivery.Body, &payload)
	if payload["type"] != webhookdelivery.TypeEndpointTest || payload["preview"] != true {
		t.Fatalf("payload: %v", payload)
	}
	if len(repository.tests) != 1 || repository.tests[0].RevisionID == first.Revision.ID {
		t.Fatalf("test was not audited against revision 2: %+v", repository.tests)
	}
	if _, err := service.Test(context.Background(), manager(), "fraud-service", 9); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("testing an unknown revision: %v", err)
	}

	for name, tc := range map[string]struct {
		response webhookdelivery.Response
		err      error
		problem  string
	}{
		"blocked":     {err: webhookdelivery.ErrDestinationBlocked, problem: "blocked"},
		"timeout":     {err: context.DeadlineExceeded, problem: "timeout"},
		"unreachable": {err: errors.New("dial tcp: connection refused"), problem: "unreachable"},
		"status":      {response: webhookdelivery.Response{StatusCode: 503}, problem: "status"},
		"invalid":     {response: webhookdelivery.Response{StatusCode: 200, Body: []byte(`<html>`)}, problem: "invalid_answer"},
		"too large":   {response: webhookdelivery.Response{StatusCode: 200}, err: webhookdelivery.ErrResponseTooLarge, problem: "response_too_large"},
	} {
		deliverer.response, deliverer.err = tc.response, tc.err
		result, err := service.Test(context.Background(), manager(), "fraud-service", 1)
		if err != nil || result.Problem != tc.problem || result.Message == "" {
			t.Errorf("%s: %+v %v", name, result, err)
		}
	}
}
