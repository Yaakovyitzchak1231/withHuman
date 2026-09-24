package cliinstaller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

type terminalOutput struct {
	buffer   bytes.Buffer
	input    io.WriteCloser
	answered bool
}

func (o *terminalOutput) Write(p []byte) (int, error) {
	n, err := o.buffer.Write(p)
	if o.input != nil && !o.answered && strings.Contains(o.buffer.String(), "Choose a runtime: ") {
		o.answered = true
		_, _ = io.WriteString(o.input, "2\n")
	}
	return n, err
}

// The real installer downloads a harmless executable from a local release
// server and installs only into t.TempDir. Never touch a developer's CLI.
func TestInstaller(t *testing.T) {
	for _, scenario := range []string{"install_only", "agent", "onboard", "terminal", "no_terminal", "implicit_onboard", "download_failure", "checksum_failure", "command_failure"} {
		t.Run(scenario, func(t *testing.T) {
			script, err := os.ReadFile("install.sh")
			if err != nil {
				t.Fatal(err)
			}
			binary := []byte(`#!/bin/sh
set -eu
if [ "$1" = version ]; then echo 'withhuman test'; exit; fi
printf '%s\n' "$@" > "$INSTALL_TEST_LOG"
case "$1" in
  onboard|agent) ;;
  *) exit 23 ;;
esac
if [ "$INSTALL_TEST_SCENARIO" = command_failure ]; then exit 27; fi
if [ "$INSTALL_TEST_SCENARIO" = terminal ]; then
  [ -t 0 ] || { echo 'terminal input is unavailable' >&2; exit 1; }
  printf 'Choose a runtime: '
  read -r choice
  printf 'runtime=%s\n' "$choice" >> "$INSTALL_TEST_LOG"
fi
if [ "$INSTALL_TEST_SCENARIO" = no_terminal ]; then
  [ -t 0 ] || { echo 'terminal input is unavailable; use withhuman agent install <runtime>' >&2; exit 1; }
fi
`)
			file := "withhuman-" + runtime.GOOS + "-" + runtime.GOARCH
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/install.sh":
					_, _ = w.Write(script)
				case "/cli/latest/" + file:
					if scenario == "download_failure" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					_, _ = w.Write(binary)
				case "/cli/latest/SHA256SUMS":
					sum := sha256.Sum256(binary)
					if scenario == "checksum_failure" {
						sum = sha256.Sum256([]byte("different binary"))
					}
					_, _ = fmt.Fprintf(w, "%x  %s\n", sum, file)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			log := filepath.Join(dir, "arguments")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			// Shadow any existing server binary during the collision probe.
			if err := os.WriteFile(filepath.Join(bin, "withhuman"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			arguments := "agent install --agent support-assistant --url https://app.example.test"
			want := "agent\ninstall\n--agent\nsupport-assistant\n--url\nhttps://app.example.test\n"
			switch scenario {
			case "install_only":
				arguments = ""
			case "onboard":
				arguments = "onboard --url https://app.example.test --code ABCD-EFGH-IJKL"
				want = "onboard\n--url\nhttps://app.example.test\n--code\nABCD-EFGH-IJKL\n"
			case "implicit_onboard":
				arguments = "--url https://app.example.test --code ABCD-EFGH-IJKL"
			}
			command := fmt.Sprintf("curl -fsSL %s/install.sh | sh -s -- %s", server.URL, arguments)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-c", command)
			if scenario == "terminal" {
				if _, err := exec.LookPath("script"); err != nil {
					t.Skip("script is needed to exercise the controlling terminal")
				}
				if runtime.GOOS == "darwin" {
					cmd = exec.CommandContext(ctx, "script", "-q", "/dev/null", "sh", "-c", command)
				} else {
					cmd = exec.CommandContext(ctx, "script", "-q", "-e", "-c", command, "/dev/null")
				}
				want += "runtime=2\n"
			}
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin", "WITHHUMAN_CLI_BASE="+server.URL,
				"WITHHUMAN_CLI_VERSION=latest", "WITHHUMAN_CLI_INSTALL_DIR="+bin,
				"INSTALL_TEST_LOG="+log, "INSTALL_TEST_SCENARIO="+scenario)
			var captured terminalOutput
			if scenario == "terminal" {
				captured.input, err = cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				defer captured.input.Close()
			}
			cmd.Stdout, cmd.Stderr = &captured, &captured
			err = cmd.Run()
			output := captured.buffer.Bytes()
			switch scenario {
			case "download_failure", "checksum_failure", "implicit_onboard", "no_terminal", "command_failure":
				if err == nil {
					t.Fatalf("expected failure: %s", output)
				}
			default:
				if err != nil {
					t.Fatalf("install: %v\n%s", err, output)
				}
			}
			if scenario == "no_terminal" && !strings.Contains(string(output), "terminal input is unavailable") {
				t.Fatalf("missing actionable error: %s", output)
			}
			raw, readErr := os.ReadFile(log)
			switch scenario {
			case "install_only", "download_failure", "checksum_failure":
				if !os.IsNotExist(readErr) {
					t.Fatalf("must not launch a command: %s, %v", raw, readErr)
				}
			case "agent", "onboard", "terminal":
				if readErr != nil || string(raw) != want {
					t.Fatalf("arguments: %q, %v; want %q", raw, readErr, want)
				}
			}
		})
	}
}
