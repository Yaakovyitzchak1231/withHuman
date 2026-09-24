package domain

import "time"

// DiscordConnection is a Discord server (guild) connected to an
// organization: the result of an organization-level OAuth install that
// added the deployment's bot to the guild. The bot token is deployment
// configuration rather than a per-install credential, so nothing here is
// secret.
type DiscordConnection struct {
	OrganizationID          ID        `json:"organization_id"`
	GuildID                 string    `json:"guild_id"`
	GuildName               string    `json:"guild_name"`
	InstalledByMembershipID ID        `json:"installed_by_membership_id"`
	InstalledAt             time.Time `json:"installed_at"`
	// LinkedMembers counts active members who have linked their Discord
	// account in this organization.
	LinkedMembers int `json:"linked_members"`
}
