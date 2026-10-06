package application

import (
	"context"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// NotificationWindowRepository throttles approval prompts per person,
// organization and contact kind (design doc 17). The first prompt in a
// quiet period goes out at once and opens the kind's first window;
// prompts due while a window is open are held, and closing the window
// hands back the held requests that still need someone, for one digest.
type NotificationWindowRepository interface {
	AdmitNotification(ctx context.Context, input AdmitNotificationInput) (NotificationAdmission, error)
	CloseNotificationWindow(ctx context.Context, input CloseNotificationWindowInput) (ClosedNotificationWindow, error)
}

// AdmitNotificationInput asks whether one request's prompt to one person
// on one kind goes out now. Windows are the kind's, shortest first.
type AdmitNotificationInput struct {
	OrganizationID domain.ID
	UserID         domain.ID
	Kind           domain.ContactMethodKind
	RequestID      domain.ID
	Windows        []time.Duration
	Now            time.Time
}

func (input AdmitNotificationInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("user ID", input.UserID),
		requiredString("contact kind", string(input.Kind)),
		requiredID("approval request ID", input.RequestID),
		requiredWindows(input.Windows),
		requiredTime("current time", input.Now),
	})
}

// NotificationAdmission answers an admission. A held prompt waits for the
// window that closes at ClosesAt; the holder closes it then.
type NotificationAdmission struct {
	Held     bool
	ClosesAt time.Time
}

// CloseNotificationWindowInput closes the window a hold was answered
// with. ClosesAt is the token: a window that already closed, by another
// holder or late, no longer matches and the close does nothing.
// MembershipID names the person in the audit events the close writes.
type CloseNotificationWindowInput struct {
	OrganizationID domain.ID
	UserID         domain.ID
	MembershipID   domain.ID
	Kind           domain.ContactMethodKind
	ClosesAt       time.Time
	Windows        []time.Duration
	Now            time.Time
}

func (input CloseNotificationWindowInput) Validate() error {
	return firstError([]error{
		requiredID("organization ID", input.OrganizationID),
		requiredID("user ID", input.UserID),
		requiredString("contact kind", string(input.Kind)),
		requiredTime("window close time", input.ClosesAt),
		requiredWindows(input.Windows),
		requiredTime("current time", input.Now),
	})
}

// ClosedNotificationWindow is what a close hands back: the held requests
// that are still pending and unclaimed, newest first. Empty when the
// close did nothing or nothing held still needs anyone.
type ClosedNotificationWindow struct {
	Requests []domain.ApprovalRequest
}

func requiredWindows(windows []time.Duration) error {
	if len(windows) == 0 {
		return invalidField("notification windows", "are required")
	}
	return nil
}
