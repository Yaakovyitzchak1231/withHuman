package domain

import (
	"testing"
	"time"
)

// An edition kind must say how it is throttled, so a new channel cannot
// page someone once per request in a burst.
func TestRegisterContactKindsRequiresDigestWindows(t *testing.T) {
	for name, windows := range map[string][]time.Duration{
		"missing":   nil,
		"zero":      {0},
		"shrinking": {time.Hour, time.Minute},
	} {
		if err := RegisterContactKinds(ContactKind{Kind: "pager_" + ContactMethodKind(name), Label: "Pager", DigestWindows: windows}); err == nil {
			t.Fatalf("%s windows were accepted", name)
		}
	}
	if err := RegisterContactKinds(ContactKind{Kind: "pager_ok", Label: "Pager", DigestWindows: []time.Duration{time.Minute, time.Hour}}); err != nil {
		t.Fatalf("valid windows refused: %v", err)
	}
	if windows := ContactMethodKind("pager_ok").DigestWindows(); len(windows) != 2 || windows[1] != time.Hour {
		t.Fatalf("registered windows = %v", windows)
	}
	if windows := ContactEmail.DigestWindows(); len(windows) != 3 || windows[0] != 5*time.Minute || windows[2] != time.Hour {
		t.Fatalf("email windows = %v", windows)
	}
	if windows := ContactMethodKind("unknown").DigestWindows(); windows != nil {
		t.Fatalf("an unknown kind should have no windows, got %v", windows)
	}
}
