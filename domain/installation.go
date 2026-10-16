package domain

import "time"

// Installation is the deployment itself, one per database: the random ID
// its heartbeat reports, and what the last answer said about newer
// releases. LatestVersion is empty and CheckedAt zero until a heartbeat
// has been answered, which never happens with telemetry turned off.
type Installation struct {
	ID            ID
	CreatedAt     time.Time
	CheckedAt     time.Time
	LatestVersion string
	Advisories    []SecurityAdvisory
}

// SecurityAdvisory is a published advisory that affects the version the
// heartbeat reported, as the release service described it.
type SecurityAdvisory struct {
	ID       string `json:"id"`
	Summary  string `json:"summary"`
	Severity string `json:"severity"`
	URL      string `json:"url"`
	FixedIn  string `json:"fixed_in"`
}
