package postgres

import "github.com/withHumanAI/withHuman/application"

// revisionPageLimit clamps a revision listing's page size: absent or
// absurd asks fall back to 50, the same for every revisioned resource.
func revisionPageLimit(limit int) int {
	if limit <= 0 || limit > 200 {
		return 50
	}
	return limit
}

// trimRevisionPage turns a limit+1 query result into one page. The extra
// row proves a next page exists without a second query; it is dropped and
// the cursor points below the last kept revision.
func trimRevisionPage[Revision any](revisions []Revision, limit int, number func(Revision) int64) ([]Revision, string) {
	if len(revisions) <= limit {
		return revisions, ""
	}
	kept := revisions[:limit]
	return kept, application.EncodeRevisionCursor(number(kept[limit-1]))
}
