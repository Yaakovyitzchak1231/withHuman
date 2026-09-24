package domain

import "time"

// NotificationUrgency is the two-way split every notification carries:
// standard waits quietly, interrupt is a page. The values match routing's
// urgency so a request's pinned urgency selects a person's ladder directly.
type NotificationUrgency string

const (
	NotificationStandard  NotificationUrgency = "standard"
	NotificationInterrupt NotificationUrgency = "interrupt"
)

func (u NotificationUrgency) Valid() bool {
	return u == NotificationStandard || u == NotificationInterrupt
}

// NotificationRule is one rung of a person's ladder for an urgency: reach
// me after Delay via this method. An empty ContactMethodID means the
// account email, which is not a stored method.
type NotificationRule struct {
	ID              ID                  `json:"id"`
	UserID          ID                  `json:"user_id"`
	Urgency         NotificationUrgency `json:"urgency"`
	Position        int                 `json:"position"`
	Delay           time.Duration       `json:"delay"`
	ContactMethodID ID                  `json:"contact_method_id,omitempty"`
	CreatedAt       time.Time           `json:"created_at"`
}

// MaximumNotificationDelay bounds a rung. A day already exceeds any
// request's useful life; the deadline caps every rung regardless.
const MaximumNotificationDelay = 24 * time.Hour
