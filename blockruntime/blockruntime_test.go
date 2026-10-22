package blockruntime

import (
	"errors"
	"testing"
)

func TestBudgetDerivesOncePerRequest(t *testing.T) {
	budget := &Budget{}
	builds := 0
	build := func() (any, error) {
		builds++
		return builds, errors.New("broken")
	}
	for range 3 {
		value, err := budget.Derive("arguments", build)
		if value != 1 || err == nil {
			t.Fatalf("got %v, %v; want the first build's answer", value, err)
		}
	}
	if builds != 1 {
		t.Fatalf("built %d times, want once", builds)
	}

	if _, err := budget.Derive("other", func() (any, error) { return "other", nil }); err != nil {
		t.Fatal(err)
	}
	var unshared *Budget
	unshared.Derive("arguments", build)
	unshared.Derive("arguments", build)
	if builds != 3 {
		t.Fatalf("a nil budget built %d times in total, want every call", builds)
	}
}
