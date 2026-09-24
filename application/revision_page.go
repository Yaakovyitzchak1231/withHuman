package application

import (
	"strconv"
)

// RevisionPage is one page of a key's revision history, newest first.
// Every revisioned resource (pipelines, escalation paths, webhook
// endpoints) lists the same way, so the shape is shared.
type RevisionPage[Revision any] struct {
	Revisions []Revision
	// NextCursor positions the following page; empty when this is the last.
	NextCursor string
	// TotalCount is how many revisions the key has across all pages.
	TotalCount int64
}

// EncodeRevisionCursor renders a page boundary as the opaque string
// clients echo. Revision numbers are dense and monotonic within a key,
// so the last revision shown positions the next page on its own.
func EncodeRevisionCursor(revision int64) string {
	return "r" + strconv.FormatInt(revision, 10)
}

// DecodeRevisionCursor parses a client-supplied cursor into the revision
// the next page starts below; an empty cursor is the first page. A
// malformed one is an invalid input, never a server error.
func DecodeRevisionCursor(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	if len(value) < 2 || value[0] != 'r' {
		return 0, invalidField("cursor", "is not a valid cursor")
	}
	revision, err := strconv.ParseInt(value[1:], 10, 64)
	if err != nil || revision <= 0 {
		return 0, invalidField("cursor", "is not a valid cursor")
	}
	return revision, nil
}
