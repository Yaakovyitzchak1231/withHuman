package contacts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

// memoryContacts is the contact method repository in memory: enough of
// it to add, rotate, and list, with the same uniqueness the table has.
type memoryContacts struct {
	methods   []domain.ContactMethod
	rotations []application.SetContactMethodChallengeInput
	hashes    map[domain.ID][]byte
	deleted   []domain.ID
}

func (m *memoryContacts) remember(id domain.ID, hash []byte) {
	if m.hashes == nil {
		m.hashes = map[domain.ID][]byte{}
	}
	m.hashes[id] = hash
}

func (m *memoryContacts) ListContactMethods(_ context.Context, input application.ListContactMethodsInput) ([]domain.ContactMethod, error) {
	var out []domain.ContactMethod
	for _, method := range m.methods {
		if method.UserID == input.UserID {
			out = append(out, method)
		}
	}
	return out, nil
}
func (m *memoryContacts) CreateContactMethod(_ context.Context, input application.CreateContactMethodInput) (domain.ContactMethod, error) {
	if err := input.Validate(); err != nil {
		return domain.ContactMethod{}, err
	}
	for _, existing := range m.methods {
		if existing.UserID == input.UserID && existing.Kind == input.Kind && strings.EqualFold(existing.Value, input.Value) {
			return domain.ContactMethod{}, fmt.Errorf("taken: %w", domain.ErrConflict)
		}
	}
	method := domain.ContactMethod{ID: input.MethodID, UserID: input.UserID, Kind: input.Kind, Value: input.Value, CreatedAt: input.Now, UpdatedAt: input.Now}
	m.methods = append(m.methods, method)
	m.remember(method.ID, input.CodeHash)
	return method, nil
}
func (m *memoryContacts) SetContactMethodChallenge(_ context.Context, input application.SetContactMethodChallengeInput) (domain.ContactMethod, error) {
	m.rotations = append(m.rotations, input)
	for _, method := range m.methods {
		if method.ID == input.MethodID && method.UserID == input.UserID {
			m.remember(method.ID, input.CodeHash)
			return method, nil
		}
	}
	return domain.ContactMethod{}, domain.ErrNotFound
}
func (m *memoryContacts) VerifyContactMethodByValue(_ context.Context, input application.VerifyContactMethodByValueInput) (domain.ContactMethod, error) {
	if err := input.Validate(); err != nil {
		return domain.ContactMethod{}, err
	}
	pending := 0
	for index, method := range m.methods {
		if method.Kind != input.Kind || !strings.EqualFold(method.Value, input.Value) || method.Verified() {
			continue
		}
		pending++
		if bytes.Equal(m.hashes[method.ID], input.CodeHash) {
			now := input.Now
			m.methods[index].VerifiedAt = &now
			return m.methods[index], nil
		}
	}
	if pending == 0 {
		return domain.ContactMethod{}, domain.ErrNotFound
	}
	return domain.ContactMethod{}, fmt.Errorf("that code is not right: %w", domain.ErrInvalid)
}
func (m *memoryContacts) VerifyContactMethod(context.Context, application.VerifyContactMethodInput) (domain.ContactMethod, error) {
	return domain.ContactMethod{}, nil
}
func (m *memoryContacts) DeleteContactMethod(_ context.Context, input application.DeleteContactMethodInput) error {
	m.deleted = append(m.deleted, input.MethodID)
	return nil
}
func (m *memoryContacts) CreateVouchedContactMethod(_ context.Context, input application.CreateVouchedContactMethodInput) (domain.ContactMethod, error) {
	if err := input.Validate(); err != nil {
		return domain.ContactMethod{}, err
	}
	for index, existing := range m.methods {
		if existing.UserID == input.UserID && existing.Kind == input.Kind && strings.EqualFold(existing.Value, input.Value) {
			m.methods[index].Label = input.Label
			return m.methods[index], nil
		}
	}
	now := input.Now
	method := domain.ContactMethod{ID: input.MethodID, UserID: input.UserID, Kind: input.Kind, Value: input.Value, Label: input.Label, VerifiedAt: &now, CreatedAt: now, UpdatedAt: now}
	m.methods = append(m.methods, method)
	return method, nil
}
func (m *memoryContacts) ListMemberAddresses(context.Context, application.ListMemberAddressesInput) ([]application.MemberAddresses, error) {
	return nil, nil
}
func (m *memoryContacts) LinkOrganizationContactMethod(context.Context, application.LinkOrganizationContactMethodInput) (domain.ContactMethod, error) {
	return domain.ContactMethod{}, nil
}

type capturingDispatcher struct {
	sent []application.SendTransactionalInput
}

func (d *capturingDispatcher) SendApprovalPrompt(context.Context, application.SendApprovalPromptInput) error {
	return nil
}
func (d *capturingDispatcher) SendTransactional(_ context.Context, input application.SendTransactionalInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	d.sent = append(d.sent, input)
	return nil
}

type harness struct {
	service    *Service
	contacts   *memoryContacts
	dispatcher *capturingDispatcher
	now        time.Time
}

func newHarness(config Config) *harness {
	h := &harness{contacts: &memoryContacts{}, dispatcher: &capturingDispatcher{}, now: time.Unix(1_700_000_000, 0).UTC()}
	h.service = NewService(h.contacts, nil, nil, h.dispatcher, fixedClock{h.now}, slog.New(slog.NewTextHandler(io.Discard, nil)), config)
	return h
}

// The edition kinds the tests register: a pager number is typed in,
// consented to and proved by a code its adapter delivers with a link for
// the person; a beacon is vouched for and never challenged.
const (
	kindPager  domain.ContactMethodKind = "pager"
	kindBeacon domain.ContactMethodKind = "beacon"
)

func init() {
	if err := domain.RegisterContactKinds(
		domain.ContactKind{Kind: kindPager, Label: "Pager", Challengeable: true, Consent: true},
		domain.ContactKind{Kind: kindBeacon, Label: "Beacon", Immediate: true, Multiple: true},
	); err != nil && !strings.Contains(err.Error(), "already registered") {
		panic(err)
	}
}

// pagerAdapter is the pager kind's adapter: it records what it was asked
// and refuses codes past a budget the test sets.
type pagerAdapter struct {
	delivered []domain.ContactMethod
	budgets   []domain.ID
	refuse    error
	// lastCode is the plaintext the adapter was last asked to deliver.
	lastCode string
}

func (a *pagerAdapter) install(service *Service) {
	service.RegisterKind(application.ContactKindAdapter{
		Kind: kindPager,
		Normalize: func(value string) (string, error) {
			digits := strings.ReplaceAll(strings.TrimSpace(value), " ", "")
			if !regexp.MustCompile(`^[0-9]{4,8}$`).MatchString(digits) {
				return "", fmt.Errorf("a pager number is 4 to 8 digits: %w", domain.ErrInvalid)
			}
			return digits, nil
		},
		DeliverCode: func(_ context.Context, method domain.ContactMethod, code string, _ time.Time) (map[string]string, error) {
			a.delivered = append(a.delivered, method)
			a.lastCode = code
			return map[string]string{"pager_confirmation_url": "https://pager.example/confirm?code=" + code}, nil
		},
		CodeBudget: func(_ context.Context, _ domain.ID, methodID domain.ID, _ time.Time) error {
			a.budgets = append(a.budgets, methodID)
			return a.refuse
		},
	})
}

var owner = domain.Principal{ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1"}

// An email code still goes out addressed as email, with nothing on the
// phone fields, so the SMTP channel keeps recognising its own.
func TestEmailCodesStayOnTheEmailChannel(t *testing.T) {
	h := newHarness(Config{})
	method := domain.ContactMethod{ID: "m-email", UserID: owner.ActorID, Kind: domain.ContactEmail, Value: "ana@example.com"}
	if extras := h.service.deliverCode(context.Background(), method, "123456", h.now.Add(15*time.Minute)); extras != nil {
		t.Fatalf("an email code carries nothing extra, got %v", extras)
	}
	sent := h.dispatcher.sent[0]
	if sent.Kind != domain.ContactEmail || sent.Recipient.Emails[0] != "ana@example.com" || len(sent.Recipient.Targets) != 0 || sent.Address != "ana@example.com" {
		t.Fatalf("email code addressing = %+v", sent)
	}
}

// An edition kind is added through its adapter: consent when the kind
// asks for it, the adapter's normal form, its budget before a code goes
// out (on add and on resend, never for email), its delivery with the
// extras it hands the person, and nothing at all without an adapter.
func TestEditionKindsAreAddedThroughTheirAdapter(t *testing.T) {
	h := newHarness(Config{})
	if _, err := h.service.Add(context.Background(), owner, kindPager, "1234", true); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a kind without an adapter must be refused, got %v", err)
	}
	adapter := &pagerAdapter{}
	adapter.install(h.service)
	if _, err := h.service.Add(context.Background(), owner, kindPager, "1234", false); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a consent kind without consent = %v, want domain.ErrInvalid", err)
	}
	if _, err := h.service.Add(context.Background(), owner, kindPager, "12", true); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("the adapter's normalization must refuse a bad value, got %v", err)
	}
	added, err := h.service.Add(context.Background(), owner, kindPager, " 12 34 ", true)
	if err != nil || added.Method.Value != "1234" || added.Method.Kind != kindPager {
		t.Fatalf("add = %+v, %v", added, err)
	}
	if added.Extras["pager_confirmation_url"] == "" || len(adapter.delivered) != 1 || adapter.delivered[0].ID != added.Method.ID {
		t.Fatalf("the adapter must deliver the code and hand back its extras: %+v %+v", added.Extras, adapter.delivered)
	}
	if len(h.dispatcher.sent) != 0 {
		t.Fatal("an edition kind's code must not go through the notification registry")
	}
	if len(adapter.budgets) != 1 || adapter.budgets[0] != "" {
		t.Fatalf("the budget is asked once on add, without a method id: %v", adapter.budgets)
	}
	adapter.refuse = fmt.Errorf("wait a minute: %w", domain.ErrLimitExceeded)
	if _, err := h.service.Resend(context.Background(), owner, added.Method.ID); !errors.Is(err, domain.ErrLimitExceeded) {
		t.Fatalf("resend past the adapter's budget = %v, want domain.ErrLimitExceeded", err)
	}
	if len(h.contacts.rotations) != 0 || adapter.budgets[1] != added.Method.ID {
		t.Fatalf("a refused resend must not rotate the challenge, and the budget names the method: %v", adapter.budgets)
	}
	adapter.refuse = nil
	if _, err := h.service.Resend(context.Background(), owner, added.Method.ID); err != nil || len(h.contacts.rotations) != 1 || len(adapter.delivered) != 2 {
		t.Fatalf("a permitted resend rotates and delivers again: %v, %d rotations, %d deliveries", err, len(h.contacts.rotations), len(adapter.delivered))
	}
	email := domain.ContactMethod{ID: "m-email", UserID: owner.ActorID, Kind: domain.ContactEmail, Value: "ana@example.com"}
	h.contacts.methods = append(h.contacts.methods, email)
	before := len(adapter.budgets)
	if _, err := h.service.Resend(context.Background(), owner, email.ID); err != nil || len(adapter.budgets) != before {
		t.Fatalf("an email resend consulted an edition budget: %v", err)
	}
	// The value side of a challenge: a code arriving from the address.
	verified, err := h.service.VerifyByValue(context.Background(), kindPager, "1234", adapter.lastCode)
	if err != nil || !verified.Verified() || verified.ID != added.Method.ID {
		t.Fatalf("verify by value = %+v, %v", verified, err)
	}
	if _, err := h.service.VerifyByValue(context.Background(), kindBeacon, "b-1", "123456"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a vouched kind is never proved by a code, got %v", err)
	}
}

// A vouched kind is recorded verified without a code, and can be taken
// away by its value.
func TestVouchedKindsAreAddedWithoutAChallenge(t *testing.T) {
	h := newHarness(Config{})
	method, err := h.service.AddVouched(context.Background(), owner, kindBeacon, " beacon-1 ", "Ada's beacon")
	if err != nil || method.Kind != kindBeacon || method.Value != "beacon-1" || !method.Verified() || method.Label != "Ada's beacon" {
		t.Fatalf("vouched add = %+v, %v", method, err)
	}
	if _, err := h.service.AddVouched(context.Background(), owner, kindPager, "1234", ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a challengeable kind cannot be vouched for, got %v", err)
	}
	if err := h.service.RemoveByValue(context.Background(), owner, kindBeacon, "BEACON-1"); err != nil || len(h.contacts.deleted) != 1 || h.contacts.deleted[0] != method.ID {
		t.Fatalf("remove by value = %v, deleted %v", err, h.contacts.deleted)
	}
	if err := h.service.RemoveByValue(context.Background(), owner, kindBeacon, "nobody"); err != nil || len(h.contacts.deleted) != 1 {
		t.Fatalf("removing an absent value must do nothing: %v", err)
	}
}
