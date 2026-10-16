package heartbeat

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type fakeInstallations struct {
	claimable bool
	claims    []application.ClaimHeartbeatInput
	answer    *application.RecordHeartbeatAnswerInput
	stored    domain.Installation
}

func (f *fakeInstallations) ClaimHeartbeat(_ context.Context, input application.ClaimHeartbeatInput) (domain.ID, bool, error) {
	f.claims = append(f.claims, input)
	return "installation-1", f.claimable, nil
}

func (f *fakeInstallations) RecordHeartbeatAnswer(_ context.Context, input application.RecordHeartbeatAnswerInput) error {
	f.answer = &input
	return nil
}

func (f *fakeInstallations) GetInstallation(context.Context) (domain.Installation, error) {
	return f.stored, nil
}

func newTestReporter(installations *fakeInstallations, url string) *Reporter {
	return NewReporter(installations, fixedClock{time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)},
		slog.New(slog.NewTextHandler(io.Discard, nil)), url, "1.0.4")
}

func TestReportSendsOnlyTheInstallationAndKeepsTheAnswer(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("User-Agent") != "withhuman/1.0.4" {
			t.Errorf("request = %s with user agent %q", r.Method, r.Header.Get("User-Agent"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode body: %v", err)
		}
		_, _ = w.Write([]byte(`{"latest_version":"1.0.6","advisories":[{"id":"GHSA-1","summary":"Fix","severity":"high","url":"https://github.com/withHumanAI/withHuman/security/advisories/GHSA-1","fixed_in":"1.0.5"}]}`))
	}))
	defer server.Close()
	installations := &fakeInstallations{claimable: true}

	if err := newTestReporter(installations, server.URL).Report(context.Background()); err != nil {
		t.Fatalf("Report: %v", err)
	}
	want := map[string]any{
		"installation_id": "installation-1", "version": "1.0.4",
		"os": goruntime.GOOS, "arch": goruntime.GOARCH, "deployment": deployment(),
	}
	if len(received) != len(want) {
		t.Fatalf("body = %v, want exactly %v", received, want)
	}
	for key, value := range want {
		if received[key] != value {
			t.Fatalf("body[%s] = %v, want %v", key, received[key], value)
		}
	}
	if installations.answer == nil || installations.answer.LatestVersion != "1.0.6" || len(installations.answer.Advisories) != 1 {
		t.Fatalf("recorded answer = %+v", installations.answer)
	}
}

func TestReportSendsNothingWithoutTheClaim(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("heartbeat sent without the claim")
	}))
	defer server.Close()
	installations := &fakeInstallations{}
	if err := newTestReporter(installations, server.URL).Report(context.Background()); err != nil {
		t.Fatalf("Report: %v", err)
	}
	if len(installations.claims) != 1 || installations.answer != nil {
		t.Fatalf("claims = %d, answer = %+v", len(installations.claims), installations.answer)
	}
}

func TestReportRefusesAnAnswerItCannotShowSafely(t *testing.T) {
	for name, body := range map[string]string{
		"development version": `{"latest_version":"dev","advisories":[]}`,
		"script link":         `{"latest_version":"1.0.6","advisories":[{"id":"x","summary":"x","severity":"high","url":"javascript:alert(1)","fixed_in":"1.0.5"}]}`,
		"plain http link":     `{"latest_version":"1.0.6","advisories":[{"id":"x","summary":"x","severity":"high","url":"http://example.com","fixed_in":"1.0.5"}]}`,
		"bad fixed version":   `{"latest_version":"1.0.6","advisories":[{"id":"x","summary":"x","severity":"high","url":"https://example.com","fixed_in":"soon"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			installations := &fakeInstallations{claimable: true}
			if err := newTestReporter(installations, server.URL).Report(context.Background()); err == nil {
				t.Fatal("Report accepted the answer")
			}
			if installations.answer != nil {
				t.Fatalf("recorded answer = %+v", installations.answer)
			}
		})
	}
}

func TestVersionComparison(t *testing.T) {
	for _, tc := range []struct {
		candidate, current string
		newer              bool
	}{
		{"1.0.5", "1.0.4", true},
		{"1.10.0", "1.9.9", true},
		{"2.0.0", "1.99.99", true},
		{"1.0.4", "1.0.4", false},
		{"1.0.3", "1.0.4", false},
		{"1.0.5", "dev", false},
		{"dev", "1.0.4", false},
		{"v1.0.5", "1.0.4", false},
		{"1.0.05", "1.0.4", false},
	} {
		if got := Newer(tc.candidate, tc.current); got != tc.newer {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.candidate, tc.current, got, tc.newer)
		}
	}
}

func TestStatusLeavesOutAdvisoriesThisVersionFixed(t *testing.T) {
	installations := &fakeInstallations{stored: domain.Installation{
		ID: "installation-1", LatestVersion: "1.0.6", CheckedAt: time.Now(),
		Advisories: []domain.SecurityAdvisory{
			{ID: "fixed", FixedIn: "1.0.4"},
			{ID: "open", FixedIn: "1.0.5"},
			{ID: "unfixed", FixedIn: ""},
		},
	}}
	member := domain.Principal{ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1", MembershipID: "membership-1"}
	admin := member.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionOrganizationSettingsWrite}))
	status, err := NewStatusReader(installations, "1.0.4").Read(context.Background(), admin)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !status.UpdateAvailable || len(status.Advisories) != 2 || status.Advisories[0].ID != "open" || status.Advisories[1].ID != "unfixed" {
		t.Fatalf("status = %+v", status)
	}
	if _, err := NewStatusReader(installations, "1.0.4").Read(context.Background(), member.WithGrants(nil)); err == nil {
		t.Fatal("Read without organization.settings.write succeeded")
	}
}
