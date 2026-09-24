// Package embeddedpg runs PostgreSQL as a managed child process for the
// single-binary deployment mode. The binaries are extracted once into the
// configured directory and reused, so only the very first start pays
// download and extraction; later starts take well under a second.
package embeddedpg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
)

const superuser = "postgres"

type Config struct {
	// Directory holds the binaries, data, runtime, and cache
	// subdirectories. Data persists across restarts; the rest is
	// disposable or reusable machinery.
	Directory string
	// Port to listen on. Zero picks a free loopback port.
	Port uint32
	// CacheDir and BinariesDir override where the downloaded archive and
	// the extracted binaries live. They default to subdirectories of
	// Directory; tests point them at a shared per-user location so many
	// short-lived instances reuse one download and one extraction.
	CacheDir    string
	BinariesDir string
	// Logger receives the PostgreSQL process output. Nil discards it.
	Logger io.Writer
	// StartTimeout bounds server startup. The first ever start downloads
	// and extracts binaries and needs a generous bound.
	StartTimeout time.Duration
}

type Server struct {
	embedded *embeddedpostgres.EmbeddedPostgres
	port     uint32
}

// Start launches PostgreSQL against Config.Directory, sweeping any stale
// postmaster left behind by an unclean parent shutdown first.
func Start(cfg Config) (*Server, error) {
	if cfg.Directory == "" {
		return nil, errors.New("embeddedpg: directory is required")
	}
	if cfg.StartTimeout <= 0 {
		cfg.StartTimeout = 2 * time.Minute
	}
	if cfg.Port == 0 {
		port, err := freePort()
		if err != nil {
			return nil, err
		}
		cfg.Port = port
	}
	dataPath := filepath.Join(cfg.Directory, "data")
	if err := sweepStalePostmaster(dataPath); err != nil {
		return nil, err
	}
	logger := cfg.Logger
	if logger == nil {
		logger = io.Discard
	}
	if cfg.CacheDir == "" {
		cfg.CacheDir = filepath.Join(cfg.Directory, "cache")
	}
	if cfg.BinariesDir == "" {
		cfg.BinariesDir = filepath.Join(cfg.Directory, "binaries")
	}
	embedded := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Username(superuser).
		Password(superuser).
		Port(cfg.Port).
		BinariesPath(cfg.BinariesDir).
		RuntimePath(filepath.Join(cfg.Directory, "runtime")).
		DataPath(dataPath).
		CachePath(cfg.CacheDir).
		Logger(logger).
		StartTimeout(cfg.StartTimeout))
	if err := embedded.Start(); err != nil {
		return nil, fmt.Errorf("embeddedpg: start: %w", err)
	}
	return &Server{embedded: embedded, port: cfg.Port}, nil
}

func (s *Server) Stop() error { return s.embedded.Stop() }

func (s *Server) Port() uint32 { return s.port }

func (s *Server) Host() string { return fmt.Sprintf("127.0.0.1:%d", s.port) }

// URL returns a connection URL for the named database.
func (s *Server) URL(database string) string {
	return fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s?sslmode=disable", superuser, superuser, s.port, database)
}

func (s *Server) User() string { return superuser }

func (s *Server) Password() string { return superuser }

// EnsureDatabases creates each named database if it does not exist yet.
func (s *Server) EnsureDatabases(ctx context.Context, names ...string) error {
	conn, err := pgx.Connect(ctx, s.URL(superuser))
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	for _, name := range names {
		var exists bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s`, pgx.Identifier{name}.Sanitize())); err != nil {
			return fmt.Errorf("create database %s: %w", name, err)
		}
	}
	return nil
}

// sweepStalePostmaster terminates a postmaster left running against
// dataPath by a hard parent crash. PostgreSQL correctly refuses to start a
// second postmaster on the same data directory, so the orphan must go
// first. The pid file's recorded data directory must match before anything
// is signalled, so a recycled pid can never kill an unrelated process.
func sweepStalePostmaster(dataPath string) error {
	contents, err := os.ReadFile(filepath.Join(dataPath, "postmaster.pid"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	lines := strings.Split(string(contents), "\n")
	if len(lines) < 2 {
		return nil
	}
	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || pid <= 0 {
		return nil
	}
	recordedData, err := filepath.EvalSymlinks(strings.TrimSpace(lines[1]))
	if err != nil {
		return nil
	}
	actualData, err := filepath.EvalSymlinks(dataPath)
	if err != nil || recordedData != actualData {
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	if err := process.Signal(syscall.Signal(0)); err != nil {
		// The process is gone; PostgreSQL will clear the stale pid file
		// itself during startup.
		return nil
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("embeddedpg: terminate stale postmaster %d: %w", pid, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := process.Signal(syscall.Signal(0)); err != nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := process.Kill(); err != nil {
		return fmt.Errorf("embeddedpg: kill stale postmaster %d: %w", pid, err)
	}
	return nil
}

func freePort() (uint32, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return uint32(listener.Addr().(*net.TCPAddr).Port), nil
}
