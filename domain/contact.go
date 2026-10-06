package domain

import (
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"
)

// ContactMethodKind names a way to reach a person. Core knows email and
// the two organization-scoped chat kinds; an edition registers the kinds
// it delivers to (the hosted edition's text messages, WhatsApp and the
// phone apps) through RegisterContactKinds, and the store validates a
// kind in code rather than in a database constraint.
type ContactMethodKind string

const (
	ContactEmail ContactMethodKind = "email"
	// ContactSlack is a Slack user id inside one connected workspace. It is
	// organization scoped, created by matching rather than typed in, and
	// verified by Slack rather than by a code.
	ContactSlack ContactMethodKind = "slack"
	// ContactDiscord is a Discord user id inside one connected server. Like
	// Slack it is organization scoped and verified by the provider, but the
	// person links it themselves: Discord offers no lookup by email.
	ContactDiscord ContactMethodKind = "discord"
)

// ContactKind is a kind an edition adds: how the contacts service, the
// notification planner and the pages treat a method of it. Every
// edition kind is person-wide, like email.
type ContactKind struct {
	Kind ContactMethodKind
	// Label names the kind to people.
	Label string
	// Challengeable kinds are typed in by their owner and proved by
	// answering a code; the others are vouched for (an installed app by
	// the session that registered it).
	Challengeable bool
	// Consent kinds need the person's express agreement on add (a phone
	// number and the messages it will receive).
	Consent bool
	// Immediate kinds join a person's default ladders at delay zero:
	// somebody who installed the app did so to hear about requests there.
	Immediate bool
	// Multiple kinds deliver to every method of the kind on one rung; for
	// the others the last method on a rung is the one that stands.
	Multiple bool
	// DigestWindows are the kind's notification windows, shortest first
	// (design doc 17): the first message in a quiet period goes out at
	// once and opens the first window, prompts due while a window is open
	// fold into one digest when it closes, and a burst widens the window
	// up to the last, which is therefore the kind's cap. Required: a new
	// channel cannot forget to be throttled.
	DigestWindows []time.Duration
}

var contactKindPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)

var contactKinds = struct {
	sync.RWMutex
	entries map[ContactMethodKind]ContactKind
}{entries: map[ContactMethodKind]ContactKind{}}

// RegisterContactKinds adds an edition's kinds. It runs once at
// composition time; a core kind, a malformed name, or a duplicate is
// refused.
func RegisterContactKinds(kinds ...ContactKind) error {
	contactKinds.Lock()
	defer contactKinds.Unlock()
	for _, kind := range kinds {
		if !contactKindPattern.MatchString(string(kind.Kind)) {
			return fmt.Errorf("contact kind %q: name must match %s", kind.Kind, contactKindPattern)
		}
		if kind.Kind.core() {
			return fmt.Errorf("contact kind %q: the kind is core's", kind.Kind)
		}
		if kind.Label == "" {
			return fmt.Errorf("contact kind %q: a label is required", kind.Kind)
		}
		if err := validDigestWindows(kind.DigestWindows); err != nil {
			return fmt.Errorf("contact kind %q: %w", kind.Kind, err)
		}
		if _, exists := contactKinds.entries[kind.Kind]; exists {
			return fmt.Errorf("contact kind %q: already registered", kind.Kind)
		}
		contactKinds.entries[kind.Kind] = kind
	}
	return nil
}

// LookupContactKind finds an edition kind.
func LookupContactKind(kind ContactMethodKind) (ContactKind, bool) {
	contactKinds.RLock()
	defer contactKinds.RUnlock()
	definition, ok := contactKinds.entries[kind]
	return definition, ok
}

// RegisteredContactKinds lists the edition kinds by name.
func RegisteredContactKinds() []ContactKind {
	contactKinds.RLock()
	defer contactKinds.RUnlock()
	list := make([]ContactKind, 0, len(contactKinds.entries))
	for _, kind := range contactKinds.entries {
		list = append(list, kind)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Kind < list[j].Kind })
	return list
}

// coreDigestWindows are core's kinds' windows. Email guards sender
// reputation, so it waits longest; the chat kinds are cheap to send and
// quick to read.
var coreDigestWindows = map[ContactMethodKind][]time.Duration{
	ContactEmail:   {5 * time.Minute, 15 * time.Minute, time.Hour},
	ContactSlack:   {time.Minute, 5 * time.Minute, 15 * time.Minute},
	ContactDiscord: {time.Minute, 5 * time.Minute, 15 * time.Minute},
}

// DigestWindows lists the kind's notification windows, shortest first.
// An unknown kind has none, and its messages are never held.
func (k ContactMethodKind) DigestWindows() []time.Duration {
	if windows, ok := coreDigestWindows[k]; ok {
		return windows
	}
	definition, _ := LookupContactKind(k)
	return definition.DigestWindows
}

func validDigestWindows(windows []time.Duration) error {
	if len(windows) == 0 {
		return fmt.Errorf("digest windows are required")
	}
	for index, window := range windows {
		if window <= 0 || (index > 0 && window < windows[index-1]) {
			return fmt.Errorf("digest windows must be positive and never shrink")
		}
	}
	return nil
}

func (k ContactMethodKind) core() bool {
	return k == ContactEmail || k == ContactSlack || k == ContactDiscord
}

// Known reports whether the kind is core's or one an edition registered.
func (k ContactMethodKind) Known() bool {
	if k.core() {
		return true
	}
	_, ok := LookupContactKind(k)
	return ok
}

// Challengeable reports whether the kind is typed in by its owner and
// proved by answering a code, as opposed to vouched for by a provider.
func (k ContactMethodKind) Challengeable() bool {
	if k == ContactEmail {
		return true
	}
	definition, ok := LookupContactKind(k)
	return ok && definition.Challengeable
}

// RequiresConsent reports whether adding a method of the kind needs the
// person's express agreement.
func (k ContactMethodKind) RequiresConsent() bool {
	definition, ok := LookupContactKind(k)
	return ok && definition.Consent
}

// Immediate reports whether the kind joins a default ladder at delay zero.
func (k ContactMethodKind) Immediate() bool {
	definition, ok := LookupContactKind(k)
	return ok && definition.Immediate
}

// Multiple reports whether every method of the kind on one rung delivers.
func (k ContactMethodKind) Multiple() bool {
	definition, ok := LookupContactKind(k)
	return ok && definition.Multiple
}

// ChatContactKinds lists the organization-scoped chat kinds in the order
// the default interrupt ladder prefers them.
var ChatContactKinds = []ContactMethodKind{ContactSlack, ContactDiscord}

// OrganizationScoped reports whether a method belongs to one
// organization's workspace rather than to the person everywhere.
func (k ContactMethodKind) OrganizationScoped() bool {
	return k == ContactSlack || k == ContactDiscord
}

// ContactMethod is somewhere a person can be reached, beyond the primary
// email their account already carries. An unverified method is never
// delivered to: it exists only so its owner can prove the address is
// theirs.
type ContactMethod struct {
	// Unavailable preserves a verified identity while its Slack installation is gone.
	Unavailable         bool              `json:"-"`
	SlackInstallationID ID                `json:"-"`
	ID                  ID                `json:"id"`
	UserID              ID                `json:"user_id"`
	Kind                ContactMethodKind `json:"kind"`
	Value               string            `json:"value"`
	// OrganizationID is set for organization-scoped kinds only.
	OrganizationID ID `json:"organization_id,omitempty"`
	// Label is a display name for kinds whose value is an opaque id, such
	// as the Slack or Discord handle behind a user id.
	Label      string     `json:"label,omitempty"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
	// Primary marks the account's own address, which is verified through
	// sign-in and cannot be removed here.
	Primary   bool      `json:"primary"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (m ContactMethod) Verified() bool { return m.VerifiedAt != nil }

func (m ContactMethod) Available() bool { return m.Verified() && !m.Unavailable }
