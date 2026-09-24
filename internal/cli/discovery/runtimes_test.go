package discovery

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/withHumanAI/withHuman/agentruntime"
)

const testBinary = "/opt/withhuman/bin/withhuman"

func fixtureEnv(t *testing.T, name string, key agentruntime.Key) (Env, *[]string) {
	t.Helper()
	home := t.TempDir()
	copyTree(t, filepath.Join("testdata", name, "before"), home)
	runtime, _ := agentruntime.Lookup(key)
	var runs []string
	env := Env{
		Home: home, ConfigDir: filepath.Join(t.TempDir(), "withhuman"), Binary: testBinary, Cwd: "/work/app",
		LookPath: func(binary string) (string, error) {
			if binary == runtime.Binary {
				return "/fake/bin/" + binary, nil
			}
			return "", fmt.Errorf("%s: not found", binary)
		},
		Run: func(_ context.Context, command string, args ...string) ([]byte, error) {
			runs = append(runs, strings.Join(append([]string{command}, args...), " "))
			if len(args) == 1 && args[0] == "--version" {
				return []byte(runtime.Binary + " 1.2.3\n"), nil
			}
			return []byte("ok\n"), nil
		},
		Getenv: func(string) string { return "" },
	}
	return env, &runs
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		target := filepath.Join(to, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o644)
	})
	if err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
}
