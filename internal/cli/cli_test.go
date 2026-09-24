package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestHookWithoutCredentialFailsClosedInProtocol(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	app := &app{version: "test", configDir: dir, stdin: strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"rm -rf /"},"session_id":"s"}`), stdout: &stdout, stderr: &stderr, getenv: func(string) string { return "" }}
	code, err := app.run(context.Background(), []string{"hook", "claude-code"})
	if err == nil || code != 1 {
		t.Fatalf("code = %d, err = %v", code, err)
	}
	var verdict struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &verdict); err != nil {
		t.Fatalf("stdout %q: %v", stdout.String(), err)
	}
	if verdict.HookSpecificOutput.PermissionDecision != "deny" || verdict.HookSpecificOutput.PermissionDecisionReason == "" {
		t.Fatalf("verdict = %+v", verdict)
	}

}

func TestUsageAndVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := &app{version: "1.2.3", configDir: t.TempDir(), stdout: &stdout, stderr: &stderr, getenv: func(string) string { return "" }}
	if code, _ := app.run(context.Background(), []string{"version"}); code != 0 || stdout.String() != "withhuman 1.2.3\n" {
		t.Fatalf("version: code %d, out %q", code, stdout.String())
	}
	if code, _ := app.run(context.Background(), nil); code != 2 || !strings.Contains(stderr.String(), "withhuman onboard --url") {
		t.Fatalf("no args: code %d, err %q", code, stderr.String())
	}
	stderr.Reset()
	stdout.Reset()
	if code, err := app.run(context.Background(), []string{"onboard"}); err != nil || code != 0 || !strings.Contains(stdout.String(), "https://app.withhuman.ai/welcome") || stderr.Len() != 0 {
		t.Fatalf("setup without flags: code %d, err %v, stdout %q, stderr %q", code, err, stdout.String(), stderr.String())
	}
	stderr.Reset()
	if code, _ := app.run(context.Background(), []string{"frobnicate"}); code != 2 || !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("unknown: code %d, err %q", code, stderr.String())
	}
}
