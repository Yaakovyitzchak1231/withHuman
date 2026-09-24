package postgres

import (
	"testing"

	"github.com/withHumanAI/withHuman/application"
)

// revisionPageOf is one page of a history as its revision numbers, so the
// three revisioned stores share one paging assertion.
type revisionPageOf struct {
	numbers    []int64
	nextCursor string
	totalCount int64
}

// assertRevisionPaging walks a key's whole history in pages of pageSize
// and checks what every revisioned store promises: newest first, no row
// repeated or skipped across page boundaries, the total the same on every
// page, and no cursor on the last page. want is the history's revision
// numbers, newest first.
func assertRevisionPaging(t *testing.T, list func(cursor string, limit int) (revisionPageOf, error), want []int64, pageSize int) {
	t.Helper()
	var seen []int64
	cursor := ""
	for pages := 0; ; pages++ {
		page, err := list(cursor, pageSize)
		if err != nil {
			t.Fatalf("page %d (cursor %q): %v", pages, cursor, err)
		}
		if page.totalCount != int64(len(want)) {
			t.Fatalf("page %d total_count = %d, want %d", pages, page.totalCount, len(want))
		}
		if len(page.numbers) > pageSize {
			t.Fatalf("page %d has %d revisions, more than the limit %d", pages, len(page.numbers), pageSize)
		}
		seen = append(seen, page.numbers...)
		if page.nextCursor == "" {
			break
		}
		if len(page.numbers) != pageSize {
			t.Fatalf("page %d is short (%d rows) yet carries a cursor", pages, len(page.numbers))
		}
		cursor = page.nextCursor
		if pages > len(want) {
			t.Fatalf("paging never ends; seen %v", seen)
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("paged history = %v, want %v", seen, want)
	}
	for index := range want {
		if seen[index] != want[index] {
			t.Fatalf("paged history = %v, want %v", seen, want)
		}
	}
	if _, err := list("not-a-cursor", pageSize); err == nil {
		t.Fatal("a malformed cursor was accepted")
	}
	if _, err := application.DecodeRevisionCursor(application.EncodeRevisionCursor(7)); err != nil {
		t.Fatalf("a cursor the store issued does not decode: %v", err)
	}
}

// descending lists n..1, the order every history is returned in.
func descending(n int) []int64 {
	numbers := make([]int64, 0, n)
	for revision := int64(n); revision >= 1; revision-- {
		numbers = append(numbers, revision)
	}
	return numbers
}
