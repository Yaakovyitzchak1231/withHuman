package secrets

import (
	"errors"
	"testing"

	"github.com/withHumanAI/withHuman/domain"
)

func TestSealRoundTripsAndBindsLabel(t *testing.T) {
	sealer, err := NewSealer("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	sealed, err := sealer.Seal("slack_bot_token", "xoxb-secret")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if string(sealed) == "xoxb-secret" || len(sealed) <= len("xoxb-secret") {
		t.Fatalf("sealed value is not ciphertext: %q", sealed)
	}
	opened, err := sealer.Open("slack_bot_token", sealed)
	if err != nil || opened != "xoxb-secret" {
		t.Fatalf("Open = %q, %v", opened, err)
	}
	// The label is associated data: a token sealed as one thing cannot be
	// opened as another.
	if _, err := sealer.Open("something_else", sealed); err == nil {
		t.Fatal("sealed value opened under a different label")
	}
	// Two seals of the same value differ, so equal tokens are not
	// recognisable at rest.
	again, _ := sealer.Seal("slack_bot_token", "xoxb-secret")
	if string(again) == string(sealed) {
		t.Fatal("sealing is deterministic")
	}
}

func TestOtherKeyCannotOpen(t *testing.T) {
	first, _ := NewSealer("0123456789abcdef0123456789abcdef")
	second, _ := NewSealer("fedcba9876543210fedcba9876543210")
	sealed, _ := first.Seal("slack_bot_token", "xoxb-secret")
	if _, err := second.Open("slack_bot_token", sealed); err == nil {
		t.Fatal("a different key opened the value")
	}
}

func TestShortKeyRefused(t *testing.T) {
	if _, err := NewSealer("too-short"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("short key = %v, want invalid", err)
	}
}
