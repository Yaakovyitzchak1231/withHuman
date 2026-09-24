package domain

import "time"

// SlackConnection is a Slack workspace connected to an organization: the
// result of an organization-level OAuth install. The bot token it yields
// is never carried on this type; it stays sealed in storage and is opened
// only by the adapter that speaks to Slack.
type SlackConnection struct {
	InstallationID          ID         `json:"installation_id"`
	Status                  string     `json:"status"`
	DisconnectedAt          *time.Time `json:"disconnected_at,omitempty"`
	DisconnectReason        string     `json:"disconnect_reason,omitempty"`
	OrganizationID          ID         `json:"organization_id"`
	TeamID                  string     `json:"team_id"`
	TeamName                string     `json:"team_name"`
	BotUserID               string     `json:"bot_user_id"`
	Scopes                  []string   `json:"scopes"`
	InstalledByMembershipID ID         `json:"installed_by_membership_id"`
	InstalledAt             time.Time  `json:"installed_at"`
	// LinkedMembers counts members whose Slack identity has been matched
	// in this workspace.
	LinkedMembers int `json:"linked_members"`
}
