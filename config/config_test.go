package config

import (
	"strings"
	"testing"
)

// Email settings are all or nothing. Half a configuration would start
// cleanly and then fail at every send, which is the failure mode operators
// notice last.
func TestLoadRejectsHalfConfiguredEmail(t *testing.T) {
	t.Setenv("WITHHUMAN_SMTP_HOST", "mail.example.com")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WITHHUMAN_SMTP_FROM") {
		t.Fatalf("host without a sender = %v, want an error naming the missing setting", err)
	}

	t.Setenv("WITHHUMAN_SMTP_HOST", "")
	t.Setenv("WITHHUMAN_SMTP_FROM", "approvals@example.com")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WITHHUMAN_SMTP_HOST") {
		t.Fatalf("sender without a host = %v, want an error naming the missing setting", err)
	}
}

func TestLoadDefaultsEmailToDisabled(t *testing.T) {
	config, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if config.SMTPConfigured() {
		t.Fatal("email is configured by default")
	}
	if config.SMTPPort != 587 {
		t.Fatalf("default SMTP port = %d, want 587", config.SMTPPort)
	}
}

func TestLoadDefaultsDiscordToDisabled(t *testing.T) {
	config, err := Load()
	if err != nil || config.DiscordConfigured() {
		t.Fatalf("Discord configured by default: %v, %v", config.DiscordConfigured(), err)
	}
}

// Telemetry is on unless the operator turns it off, by our variable or
// the cross-tool DO_NOT_TRACK, and a typo stops startup.
func TestLoadTelemetrySwitch(t *testing.T) {
	for _, tc := range []struct {
		telemetry, doNotTrack string
		want                  bool
	}{
		{"", "", true},
		{"on", "", true},
		{"off", "", false},
		{"FALSE", "", false},
		{"0", "", false},
		{"", "1", false},
		{"on", "true", false},
		{"", "0", true},
	} {
		t.Setenv("WITHHUMAN_TELEMETRY", tc.telemetry)
		t.Setenv("DO_NOT_TRACK", tc.doNotTrack)
		config, err := Load()
		if err != nil {
			t.Fatalf("Load(%q, %q): %v", tc.telemetry, tc.doNotTrack, err)
		}
		if config.Telemetry != tc.want {
			t.Fatalf("Telemetry with WITHHUMAN_TELEMETRY=%q DO_NOT_TRACK=%q = %v, want %v", tc.telemetry, tc.doNotTrack, config.Telemetry, tc.want)
		}
	}
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("WITHHUMAN_TELEMETRY", "nope")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WITHHUMAN_TELEMETRY") {
		t.Fatalf("unrecognized value = %v, want an error naming WITHHUMAN_TELEMETRY", err)
	}
}
