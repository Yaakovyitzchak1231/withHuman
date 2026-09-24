package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/withHumanAI/withHuman/internal/cli/upgrade"
)

func TestUpdateNoticeCommandSelection(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{"install", []string{"agent", "install", "claude-code"}, true},
		{"discovery", []string{"agent", "discover"}, true},
		{"onboard", []string{"onboard"}, true},
		{"hook", []string{"hook", "claude-code"}, false},
		{"json", []string{"agent", "discover", "--json"}, false},
		{"json-equals", []string{"onboard", "--json=true"}, false},
		{"json-single-dash", []string{"onboard", "-json"}, false},
		{"help", []string{"help"}, false},
		{"command-help", []string{"agent", "install", "--help"}, false},
		{"short-help", []string{"onboard", "-h"}, false},
		{"version", []string{"version"}, false},
		{"upgrade", []string{"upgrade"}, false},
		{"empty", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			calls := 0
			a := noticeApp(t, &stdout, &stderr, &calls)
			a.updateNotice(context.Background(), tc.args)
			if (calls == 1) != tc.want || strings.Contains(stderr.String(), "Run withhuman upgrade.") != tc.want || stdout.Len() != 0 {
				t.Fatalf("calls=%d stdout=%q stderr=%q", calls, &stdout, &stderr)
			}
		})
	}
}

func TestUpdateNoticeOptOutAndNoninteractive(t *testing.T) {
	for _, scenario := range []string{"opt-out", "noninteractive"} {
		t.Run(scenario, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			calls := 0
			a := noticeApp(t, &stdout, &stderr, &calls)
			if scenario == "opt-out" {
				a.getenv = func(name string) string {
					if name == "WITHHUMAN_NO_UPDATE_CHECK" {
						return "1"
					}
					return ""
				}
			} else {
				a.updateEnv.Interactive = nil // bytes.Buffer is not a terminal.
			}
			a.updateNotice(context.Background(), []string{"onboard"})
			if calls != 0 || stderr.Len() != 0 {
				t.Fatal("unexpected update check", calls, &stderr)
			}
		})
	}
}

func TestNoticePrecedesCommandFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	calls := 0
	a := noticeApp(t, &stdout, &stderr, &calls)
	code, err := a.run(context.Background(), []string{"onboard", "--invalid"})
	if code != 1 || err == nil || calls != 1 || !strings.HasPrefix(stderr.String(), "withhuman 0.0.225 is available") {
		t.Fatalf("code=%d err=%v calls=%d stderr=%q", code, err, calls, &stderr)
	}
}

func TestUpgradeCommandUsage(t *testing.T) {
	for _, args := range [][]string{{"upgrade", "--help"}, {"upgrade", "unexpected"}, {"upgrade"}} {
		var stdout, stderr bytes.Buffer
		calls := 0
		a := noticeApp(t, &stdout, &stderr, &calls)
		a.version = "0.0.225"
		// Explicit upgrade ignores automatic-check opt-out and cached notices.
		a.getenv = func(name string) string {
			if name == "WITHHUMAN_NO_UPDATE_CHECK" {
				return "1"
			}
			return ""
		}
		code, err := a.run(context.Background(), args)
		if len(args) == 1 {
			if code != 0 || err != nil || calls != 1 || !strings.Contains(stdout.String(), "up to date") || stderr.Len() != 0 {
				t.Fatal(code, err, calls, &stdout, &stderr)
			}
		} else if calls != 0 || (args[1] == "--help" && code != 0) || (args[1] == "unexpected" && code != 2) {
			t.Fatal(code, err, calls)
		}
	}
}

func noticeApp(t *testing.T, stdout, stderr *bytes.Buffer, calls *int) *app {
	t.Helper()
	return &app{
		version: "0.0.198", configDir: t.TempDir(), stdin: strings.NewReader(""), stdout: stdout, stderr: stderr,
		getenv:    func(string) string { return "" },
		updateEnv: upgrade.Environment{Interactive: func() bool { return true }},
		http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			(*calls)++
			if r.URL.Path != "/cli/latest/version" {
				return nil, fmt.Errorf("unexpected request %s", r.URL)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("0.0.225\n"))}, nil
		})},
	}
}
