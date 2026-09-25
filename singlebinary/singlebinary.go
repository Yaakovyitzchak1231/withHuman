// Package singlebinary is the composition root for the single-process
// topology: embedded PostgreSQL, embedded Temporal, the worker, and the
// HTTP server in one process. It composes them from the same exported
// runtime the split binaries use, wired over loopback instead of a
// network, so an edition's server and worker options apply here
// unchanged. Only single-process mains (cmd/withhuman, a hosted edition's
// local main) and tests may import this package: the split binaries must
// never pay for the embedded server dependency graphs.
package singlebinary

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/withHumanAI/withHuman/config"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/internal/embeddedpg"
	"github.com/withHumanAI/withHuman/internal/embeddedtemporal"
	"github.com/withHumanAI/withHuman/loggermdc"
	"github.com/withHumanAI/withHuman/runtime"
)

const (
	applicationDatabase = "withhuman"
	temporalDatabase    = "temporal"
	visibilityDatabase  = "temporal_visibility"
)

type Config struct {
	// DataDir holds everything the instance persists. Defaults to
	// ~/.withhuman.
	DataDir string
	// HTTPAddress is the one public listen address. Defaults to
	// WITHHUMAN_HTTP_ADDRESS or :8080.
	HTTPAddress string
	// PublicOrigin overrides WITHHUMAN_PUBLIC_ORIGIN.
	PublicOrigin string
	// BootstrapSecret overrides WITHHUMAN_BOOTSTRAP_SECRET. When both are
	// empty a secret is generated and persisted in the data directory.
	BootstrapSecret string
	Logger          *slog.Logger
	// Version is reported by the version endpoint and runtime config.
	Version string
	// PostgresCacheDir and PostgresBinariesDir override where the
	// PostgreSQL archive cache and extracted binaries live. They default
	// to the data directory so the deployment stays self-contained; tests
	// point them at a shared location to avoid re-extracting per run.
	PostgresCacheDir    string
	PostgresBinariesDir string
	// Server and Worker are the edition's composition options: hooks,
	// entitlements, channels, routes, permissions, runtime-config
	// entries. Logger, Version and Config are filled in here, and the
	// bootstrap secret is always present (generated when absent), so
	// RequireBootstrapSecret is implied.
	Server runtime.ServerOptions
	Worker runtime.WorkerOptions
}

// Instance is a running single-process deployment.
type Instance struct {
	// BaseURL is the resolved http origin of the listener, useful when
	// HTTPAddress requested an ephemeral port.
	BaseURL string
	// BootstrapSecretPath is where the generated bootstrap secret lives,
	// or empty when the secret came from configuration.
	BootstrapSecretPath string
	// DatabaseURL reaches the embedded application database, for an
	// edition's own store or a test that inspects rows directly.
	DatabaseURL string

	logger     *slog.Logger
	stopServer context.CancelFunc
	serverDone chan error
	stopWorker context.CancelFunc
	workerDone chan error
	temporal   *embeddedtemporal.Server
	pg         *embeddedpg.Server
	pgLog      *os.File
	fatal      chan error
}

// Start boots the full provider. On error, everything already started is
// stopped again.
func Start(ctx context.Context, cfg Config) (_ *Instance, err error) {
	logger := loggermdc.Wrap(cfg.Logger)
	dataDir, err := resolveDataDir(cfg.DataDir)
	if err != nil {
		return nil, err
	}

	base, err := config.Load()
	if err != nil {
		return nil, err
	}
	if cfg.HTTPAddress != "" {
		base.HTTPAddress = cfg.HTTPAddress
	}
	if cfg.PublicOrigin != "" {
		base.PublicOrigin = cfg.PublicOrigin
	}
	if cfg.BootstrapSecret != "" {
		base.BootstrapSecret = cfg.BootstrapSecret
	}
	secretPath := ""
	if base.BootstrapSecret == "" {
		base.BootstrapSecret, secretPath, err = loadOrCreateBootstrapSecret(dataDir)
		if err != nil {
			return nil, err
		}
	}

	instance := &Instance{logger: logger, BootstrapSecretPath: secretPath, fatal: make(chan error, 2)}
	defer func() {
		if err != nil {
			shutdownContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = instance.Shutdown(shutdownContext)
		}
	}()

	// PostgreSQL first; everything else persists through it.
	pgDir := filepath.Join(dataDir, "postgres")
	if err = os.MkdirAll(pgDir, 0o700); err != nil {
		return nil, err
	}
	instance.pgLog, err = os.OpenFile(filepath.Join(pgDir, "postgres.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	logger.Info("starting embedded PostgreSQL", "directory", pgDir)
	instance.pg, err = embeddedpg.Start(embeddedpg.Config{
		Directory:   pgDir,
		Logger:      instance.pgLog,
		CacheDir:    cfg.PostgresCacheDir,
		BinariesDir: cfg.PostgresBinariesDir,
	})
	if err != nil {
		return nil, err
	}
	if err = instance.pg.EnsureDatabases(ctx, applicationDatabase, temporalDatabase, visibilityDatabase); err != nil {
		return nil, err
	}
	base.DatabaseURL = instance.pg.URL(applicationDatabase)
	instance.DatabaseURL = base.DatabaseURL
	if err = applyMigrations(ctx, base.DatabaseURL); err != nil {
		var foreign *db.ForeignChainError
		if errors.As(err, &foreign) {
			return nil, fmt.Errorf("apply application migrations: %w; the data directory %s was created by an earlier withHuman, so move it aside or start with a fresh --data-dir", err, dataDir)
		}
		return nil, fmt.Errorf("apply application migrations: %w", err)
	}

	// Temporal schema and server; the runtime dials it like any other.
	temporalConfig := embeddedtemporal.Config{
		PostgresHost:           instance.pg.Host(),
		PostgresUser:           instance.pg.User(),
		PostgresPassword:       instance.pg.Password(),
		DatabaseName:           temporalDatabase,
		VisibilityDatabaseName: visibilityDatabase,
		Namespace:              base.TemporalNamespace,
		Logger:                 logger,
	}
	if err = embeddedtemporal.SetupSchema(temporalConfig); err != nil {
		return nil, err
	}
	logger.Info("starting embedded Temporal")
	instance.temporal, err = embeddedtemporal.Start(ctx, temporalConfig)
	if err != nil {
		return nil, err
	}
	base.TemporalAddress = instance.temporal.FrontendHostPort()
	// One process, one listener: the worker reports health through the
	// server's endpoints instead of binding a second address.
	base.WorkerHealthAddress = ""

	// Server: the same composition the split server main runs.
	serverConfig := base
	serverOptions := cfg.Server
	serverOptions.Logger, serverOptions.Version, serverOptions.Config = logger, version(cfg.Version), &serverConfig
	serverOptions.RequireBootstrapSecret = true
	server, err := runtime.NewServer(ctx, serverOptions)
	if err != nil {
		return nil, err
	}
	listener, err := server.Listen()
	if err != nil {
		server.Close()
		return nil, err
	}
	instance.BaseURL = listenerOrigin(listener)

	// Worker, in-process, composed exactly as the split worker main. It
	// runs before the listener serves, as it did when both were wired by
	// hand, so ready means the whole product.
	workerConfig := base
	workerOptions := cfg.Worker
	workerReady := make(chan struct{})
	workerOptions.Logger, workerOptions.Config = logger, &workerConfig
	workerOptions.Ready = func() { close(workerReady) }
	workerCtx, stopWorker := context.WithCancel(context.Background())
	workerDone := make(chan error, 1)
	go func() {
		err := runtime.RunWorker(workerCtx, workerOptions)
		if workerCtx.Err() != nil {
			// Asked to stop: a cancellation that interrupted its startup
			// is not a failure.
			err = nil
		} else if err != nil {
			instance.fatal <- fmt.Errorf("worker: %w", err)
		}
		workerDone <- err
	}()
	select {
	case <-workerReady:
	case err = <-workerDone:
		stopWorker()
		listener.Close()
		server.Close()
		return nil, fmt.Errorf("worker: %w", err)
	case <-ctx.Done():
		stopWorker()
		<-workerDone
		listener.Close()
		server.Close()
		return nil, ctx.Err()
	}
	instance.stopWorker, instance.workerDone = stopWorker, workerDone

	serverCtx, stopServer := context.WithCancel(context.Background())
	instance.stopServer, instance.serverDone = stopServer, make(chan error, 1)
	go func() {
		err := server.Serve(serverCtx, listener)
		if serverCtx.Err() != nil {
			err = nil
		} else if err != nil {
			instance.fatal <- fmt.Errorf("server: %w", err)
		}
		instance.serverDone <- err
	}()
	logger.Info("withHuman ready - "+instance.BaseURL, "url", instance.BaseURL, "data_dir", dataDir)

	return instance, nil
}

// applyMigrations runs core's chain on a short-lived pool; the server and
// worker open their own afterwards, as they do in the split topology.
func applyMigrations(ctx context.Context, databaseURL string) error {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	return db.Apply(ctx, pool, db.Migrations())
}

func version(configured string) string {
	if configured == "" {
		return "dev"
	}
	return configured
}

// generateSecret mints a process-local shared secret: hex of 32 random
// bytes, the same shape as the generated bootstrap secret.
func generateSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// ServeErrors reports a fatal error from the server or the worker: a
// listener that failed, a worker that could not start. Shutdown never
// produces one.
func (i *Instance) ServeErrors() <-chan error { return i.fatal }

// Shutdown stops everything in reverse startup order, PostgreSQL last.
func (i *Instance) Shutdown(ctx context.Context) error {
	var errs []error
	if i.stopServer != nil {
		i.stopServer()
		errs = append(errs, awaitStop(ctx, "server", i.serverDone))
	}
	if i.stopWorker != nil {
		i.stopWorker()
		errs = append(errs, awaitStop(ctx, "worker", i.workerDone))
	}
	if i.temporal != nil {
		errs = append(errs, describe("temporal", i.temporal.Stop()))
	}
	if i.pg != nil {
		errs = append(errs, describe("postgres", i.pg.Stop()))
	}
	if i.pgLog != nil {
		errs = append(errs, describe("postgres log", i.pgLog.Close()))
	}
	return errors.Join(errs...)
}

func awaitStop(ctx context.Context, component string, done <-chan error) error {
	select {
	case err := <-done:
		return describe(component, err)
	case <-ctx.Done():
		return fmt.Errorf("%s did not stop: %w", component, ctx.Err())
	}
}

func describe(component string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", component, err)
}

func resolveDataDir(dir string) (string, error) {
	if dir == "" {
		dir = strings.TrimSpace(os.Getenv("WITHHUMAN_DATA_DIR"))
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory for default data dir: %w", err)
		}
		dir = filepath.Join(home, ".withhuman")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return "", err
	}
	return absolute, nil
}

func loadOrCreateBootstrapSecret(dataDir string) (secret, path string, err error) {
	path = filepath.Join(dataDir, "bootstrap-secret")
	contents, err := os.ReadFile(path)
	if err == nil {
		secret = strings.TrimSpace(string(contents))
		if secret != "" {
			return secret, path, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	secret = hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		return "", "", err
	}
	return secret, path, nil
}

func listenerOrigin(listener net.Listener) string {
	address := listener.Addr().(*net.TCPAddr)
	host := address.IP.String()
	if address.IP.IsUnspecified() {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s", net.JoinHostPort(host, fmt.Sprintf("%d", address.Port)))
}
