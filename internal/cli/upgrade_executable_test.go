package cli

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestExecutableUpgrade(t *testing.T) {
	dir := t.TempDir()
	oldBinary := filepath.Join(dir, "old CLI")
	newBinary := filepath.Join(dir, "new CLI")
	for version, binary := range map[string]string{"0.0.198": oldBinary, "0.0.225": newBinary} {
		cmd := exec.Command("go", "build", "-ldflags", "-X main.version="+version, "-o", binary, "../../cmd/withhuman-cli")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", version, err, out)
		}
	}
	newBytes, err := os.ReadFile(newBinary)
	if err != nil {
		t.Fatal(err)
	}
	oldBytes, err := os.ReadFile(oldBinary)
	if err != nil {
		t.Fatal(err)
	}
	asset := "withhuman-" + runtime.GOOS + "-" + runtime.GOARCH
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/cli/latest/version":
			fmt.Fprintln(w, "0.0.225")
		case "/cli/0.0.225/SHA256SUMS":
			fmt.Fprintf(w, "%x  %s\n", sha256.Sum256(newBytes), asset)
		case "/cli/0.0.225/" + asset:
			w.Write(newBytes)
		default:
			t.Errorf("unexpected release request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	home, config := t.TempDir(), t.TempDir()
	files := map[string][]byte{
		filepath.Join(config, "credentials", "claude-code.json"): []byte(`{"token":"fixture-secret"}`),
		filepath.Join(config, "aap", "plugin.js"):                []byte("preserve installed plugin"),
		filepath.Join(home, ".claude", "settings.json"):          []byte(`{"userChoice":true}`),
	}
	for path, body := range files {
		mustWrite(t, path, string(body), 0600)
	}
	link := filepath.Join(dir, "withhuman")
	if err := os.Symlink(oldBinary, link); err != nil {
		t.Fatal(err)
	}
	run := func(binary string, args ...string) string {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "HOME="+home, "WITHHUMAN_CONFIG_DIR="+config, "WITHHUMAN_CLI_BASE="+server.URL, "WITHHUMAN_NO_UPDATE_CHECK=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	if out := run(link, "upgrade"); !strings.Contains(out, "from 0.0.198 to 0.0.225") {
		t.Fatal(out)
	}
	if out := run(link, "version"); out != "withhuman 0.0.225\n" {
		t.Fatal(out)
	}
	if out := run(link, "upgrade"); !strings.Contains(out, "up to date") {
		t.Fatal(out)
	}
	if info, _ := os.Lstat(link); info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("upgrade destroyed executable symlink")
	}
	for path, before := range files {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("changed runtime configuration %s: %v", path, err)
		}
	}
	mu.Lock()
	got := append([]string(nil), requests...)
	mu.Unlock()
	want := []string{"/cli/latest/version", "/cli/0.0.225/SHA256SUMS", "/cli/0.0.225/" + asset, "/cli/latest/version"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("requests: %v", got)
	}

	// Exercise the exact helper entrypoint without needing sudo, replacing a
	// disposable executable. Empty HOME makes ordinary ConfigDir fail on Unix.
	helper := filepath.Join(dir, "helper CLI")
	if err := os.WriteFile(helper, oldBytes, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(helper, "upgrade-install", "--source", newBinary, "--sha256", fmt.Sprintf("%x", sha256.Sum256(newBytes)))
	cmd.Env = []string{"HOME=", "XDG_CONFIG_HOME=", "WITHHUMAN_CONFIG_DIR="}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper unexpectedly needed configuration: %v\n%s", err, out)
	}
	if out := run(helper, "version"); out != "withhuman 0.0.225\n" {
		t.Fatal(out)
	}
}
