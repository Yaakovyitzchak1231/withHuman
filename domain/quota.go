package domain

import (
	"errors"
	"fmt"
)

// ErrQuotaExceeded is the sentinel for an allocation refused because the
// organization holds as much of a dimension as its plan allows. It differs
// from ErrLimitExceeded, which is a rate-style cap on in-flight work: a
// quota is resolved by buying more, not by waiting.
var ErrQuotaExceeded = errors.New("quota exceeded")

// QuotaExceeded carries the structured detail behind ErrQuotaExceeded so
// the HTTP boundary can tell the caller which dimension is full and by how
// much, and the frontend can offer the matching purchase.
type QuotaExceeded struct {
	Dimension string
	Limit     int64
	Used      int64
}

func (q QuotaExceeded) Error() string {
	return fmt.Sprintf("organization is at its quota of %d %s (%d in use): %s", q.Limit, q.Dimension, q.Used, ErrQuotaExceeded)
}

func (QuotaExceeded) Is(target error) bool { return target == ErrQuotaExceeded }
