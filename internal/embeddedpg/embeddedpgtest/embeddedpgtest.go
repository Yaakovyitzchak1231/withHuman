// Package embeddedpgtest boots disposable embedded PostgreSQL instances
// for integration tests. Instances get a fresh data directory per test but
// share one per-user download cache and extracted-binaries directory, so
// only the first run on a machine pays the download and extraction.
package embeddedpgtest

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/withHumanAI/withHuman/internal/embeddedpg"
)

// Start boots a PostgreSQL instance on a free loopback port with a fresh
// data directory, and stops it when the test finishes.
func Start(t *testing.T) *embeddedpg.Server {
	t.Helper()
	cacheDir, binariesDir := SharedDirs(t)
	unlock := LockStartup(t)
	server, err := embeddedpg.Start(embeddedpg.Config{
		Directory:   t.TempDir(),
		CacheDir:    cacheDir,
		BinariesDir: binariesDir,
	})
	unlock()
	if err != nil {
		t.Fatalf("start embedded PostgreSQL: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Stop(); err != nil {
			t.Errorf("stop embedded PostgreSQL: %v", err)
		}
	})
	return server
}

// SharedDirs returns the per-user archive cache and extracted-binaries
// directories tests share, and skips the test in short mode.
func SharedDirs(t *testing.T) (cacheDir, binariesDir string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping embedded PostgreSQL test in short mode")
	}
	shared, err := sharedDir()
	if err != nil {
		t.Fatalf("resolve shared embedded-postgres dir: %v", err)
	}
	return filepath.Join(shared, "cache"), filepath.Join(shared, "binaries")
}

// LockStartup serializes embedded PostgreSQL startup across test packages:
// concurrent first-time extraction would corrupt the shared binaries
// directory. Call the returned unlock as soon as startup finishes.
func LockStartup(t *testing.T) (unlock func()) {
	t.Helper()
	shared, err := sharedDir()
	if err != nil {
		t.Fatalf("resolve shared embedded-postgres dir: %v", err)
	}
	unlock, err = lock(filepath.Join(shared, "start.lock"))
	if err != nil {
		t.Fatalf("lock embedded-postgres startup: %v", err)
	}
	return unlock
}

func sharedDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "withhuman-embedded-postgres")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func lock(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}
