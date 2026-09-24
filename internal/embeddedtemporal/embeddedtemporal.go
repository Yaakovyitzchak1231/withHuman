// Package embeddedtemporal runs the Temporal server in-process for the
// single-binary deployment mode, persisting to PostgreSQL through
// Temporal's standard SQL plugin. Everything binds to loopback; only the
// returned frontend address is meant to be dialled, and only from this
// machine.
package embeddedtemporal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	sdkclient "go.temporal.io/sdk/client"
	"go.temporal.io/server/common/cluster"
	"go.temporal.io/server/common/config"
	"go.temporal.io/server/common/dynamicconfig"
	tlog "go.temporal.io/server/common/log"
	"go.temporal.io/server/common/log/tag"
	"go.temporal.io/server/common/membership/static"
	"go.temporal.io/server/common/primitives"
	postgresqlversion "go.temporal.io/server/schema/postgresql/v12"
	"go.temporal.io/server/temporal"
	commonschema "go.temporal.io/server/tools/common/schema"
	sqltool "go.temporal.io/server/tools/sql"
	"google.golang.org/protobuf/types/known/durationpb"

	// Registers the postgres12 persistence plugin.
	_ "go.temporal.io/server/common/persistence/sql/sqlplugin/postgresql"
)

const pluginName = "postgres12"

type Config struct {
	// PostgresHost is the host:port of the PostgreSQL server holding the
	// Temporal databases.
	PostgresHost     string
	PostgresUser     string
	PostgresPassword string
	// DatabaseName and VisibilityDatabaseName must already exist.
	DatabaseName           string
	VisibilityDatabaseName string
	// Namespace is registered and waited for after startup.
	Namespace string
	Logger    *slog.Logger
}

type Server struct {
	server           temporal.Server
	frontendHostPort string
}

// FrontendHostPort is the loopback address for Temporal SDK clients.
func (s *Server) FrontendHostPort() string { return s.frontendHostPort }

func (s *Server) Stop() error { return s.server.Stop() }

// SetupSchema applies the Temporal and visibility schemas to their
// databases if they are not set up yet. Temporal owns these schemas; this
// runs Temporal's own setup task against its embedded schema assets.
func SetupSchema(cfg Config) error {
	logger := newLogAdapter(cfg.Logger)
	for _, target := range []struct {
		database   string
		schemaName string
		version    string
	}{
		{cfg.DatabaseName, "postgresql/v12/temporal", postgresqlversion.Version},
		{cfg.VisibilityDatabaseName, "postgresql/v12/visibility", postgresqlversion.VisibilityVersion},
	} {
		connection, err := sqltool.NewConnection(sqlConfig(cfg, target.database), logger)
		if err != nil {
			return fmt.Errorf("connect to %s: %w", target.database, err)
		}
		version, err := connection.ReadSchemaVersion()
		if err == nil && version != "" {
			connection.Close()
			continue
		}
		task := commonschema.NewSetupSchemaTask(connection, &commonschema.SetupConfig{
			SchemaName:     target.schemaName,
			InitialVersion: target.version,
		}, logger)
		err = task.Run()
		connection.Close()
		if err != nil {
			return fmt.Errorf("set up %s schema: %w", target.database, err)
		}
	}
	return nil
}

// Start boots the Temporal server on loopback, registers the configured
// namespace, and blocks until the namespace is usable through the
// frontend.
func Start(ctx context.Context, cfg Config) (*Server, error) {
	ports, err := freePorts(8)
	if err != nil {
		return nil, err
	}
	frontendPort := ports[0]
	frontendHostPort := fmt.Sprintf("127.0.0.1:%d", frontendPort)
	logger := newLogAdapter(cfg.Logger)

	serverConfig := &config.Config{
		Global: config.Global{
			Membership: config.Membership{
				MaxJoinDuration:  30 * time.Second,
				BroadcastAddress: "127.0.0.1",
			},
		},
		Persistence: config.Persistence{
			DefaultStore:     "sql-default",
			VisibilityStore:  "sql-visibility",
			NumHistoryShards: 1,
			DataStores: map[string]config.DataStore{
				"sql-default":    {SQL: sqlConfig(cfg, cfg.DatabaseName)},
				"sql-visibility": {SQL: sqlConfig(cfg, cfg.VisibilityDatabaseName)},
			},
		},
		ClusterMetadata: &cluster.Config{
			EnableGlobalNamespace:    false,
			FailoverVersionIncrement: 10,
			MasterClusterName:        "active",
			CurrentClusterName:       "active",
			ClusterInformation: map[string]cluster.ClusterInformation{
				"active": {
					Enabled:                true,
					InitialFailoverVersion: 1,
					RPCAddress:             frontendHostPort,
				},
			},
		},
		DCRedirectionPolicy: config.DCRedirectionPolicy{Policy: "noop"},
		Services: map[string]config.Service{
			string(primitives.FrontendService): service(frontendPort, ports[1]),
			string(primitives.HistoryService):  service(ports[2], ports[3]),
			string(primitives.MatchingService): service(ports[4], ports[5]),
			string(primitives.WorkerService):   service(ports[6], ports[7]),
		},
		Archival: config.Archival{
			History:    config.HistoryArchival{State: "disabled"},
			Visibility: config.VisibilityArchival{State: "disabled"},
		},
		NamespaceDefaults: config.NamespaceDefaults{
			Archival: config.ArchivalNamespaceDefaults{
				History:    config.HistoryArchivalNamespaceDefaults{State: "disabled"},
				Visibility: config.VisibilityArchivalNamespaceDefaults{State: "disabled"},
			},
		},
		PublicClient: config.PublicClient{HostPort: frontendHostPort},
	}

	dynamic := dynamicconfig.StaticClient{
		// The default namespace registry refresh is 10s, which would make
		// the freshly registered namespace unusable for that long after
		// every cold boot.
		dynamicconfig.NamespaceCacheRefreshInterval.Key(): time.Second,
	}

	server, err := temporal.NewServer(
		temporal.WithConfig(serverConfig),
		temporal.ForServices(temporal.DefaultServices),
		// All four services run in this one process; static membership
		// replaces ringpop discovery.
		temporal.WithStaticHosts(map[primitives.ServiceName]static.Hosts{
			primitives.FrontendService: static.SingleLocalHost(frontendHostPort),
			primitives.HistoryService:  static.SingleLocalHost(fmt.Sprintf("127.0.0.1:%d", ports[2])),
			primitives.MatchingService: static.SingleLocalHost(fmt.Sprintf("127.0.0.1:%d", ports[4])),
			primitives.WorkerService:   static.SingleLocalHost(fmt.Sprintf("127.0.0.1:%d", ports[6])),
		}),
		temporal.WithLogger(logger),
		temporal.WithDynamicConfigClient(dynamic),
	)
	if err != nil {
		return nil, fmt.Errorf("build temporal server: %w", err)
	}
	if err := server.Start(); err != nil {
		return nil, fmt.Errorf("start temporal server: %w", err)
	}
	embedded := &Server{server: server, frontendHostPort: frontendHostPort}
	if err := embedded.ensureNamespace(ctx, cfg.Namespace); err != nil {
		_ = server.Stop()
		return nil, err
	}
	return embedded, nil
}

func (s *Server) ensureNamespace(ctx context.Context, namespace string) error {
	namespaceClient, err := sdkclient.NewNamespaceClient(sdkclient.Options{HostPort: s.frontendHostPort})
	if err != nil {
		return err
	}
	defer namespaceClient.Close()
	deadline := time.Now().Add(60 * time.Second)
	registered := false
	for !registered {
		err = namespaceClient.Register(ctx, &workflowservice.RegisterNamespaceRequest{
			Namespace:                        namespace,
			WorkflowExecutionRetentionPeriod: durationpb.New(72 * time.Hour),
		})
		var alreadyExists *serviceerror.NamespaceAlreadyExists
		switch {
		case err == nil, errors.As(err, &alreadyExists):
			registered = true
		case time.Now().After(deadline):
			return fmt.Errorf("register namespace %s: %w", namespace, err)
		default:
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
	}

	// Registration writes to the metadata store, but the frontend serves
	// namespaced calls from its registry cache. Poll a cache-gated call
	// until the namespace is actually usable.
	temporalClient, err := sdkclient.Dial(sdkclient.Options{HostPort: s.frontendHostPort, Namespace: namespace})
	if err != nil {
		return err
	}
	defer temporalClient.Close()
	for {
		_, err = temporalClient.WorkflowService().ListWorkflowExecutions(ctx, &workflowservice.ListWorkflowExecutionsRequest{
			Namespace: namespace,
			PageSize:  1,
		})
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("namespace %s did not become usable: %w", namespace, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func sqlConfig(cfg Config, database string) *config.SQL {
	return &config.SQL{
		PluginName:      pluginName,
		ConnectAddr:     cfg.PostgresHost,
		ConnectProtocol: "tcp",
		User:            cfg.PostgresUser,
		Password:        cfg.PostgresPassword,
		DatabaseName:    database,
		MaxConns:        10,
		MaxIdleConns:    10,
		MaxConnLifetime: time.Hour,
	}
}

func service(grpcPort, membershipPort int) config.Service {
	return config.Service{RPC: config.RPC{
		GRPCPort:        grpcPort,
		MembershipPort:  membershipPort,
		BindOnLocalHost: true,
	}}
}

func freePorts(count int) ([]int, error) {
	listeners := make([]net.Listener, 0, count)
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	ports := make([]int, 0, count)
	for range count {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		listeners = append(listeners, listener)
		ports = append(ports, listener.Addr().(*net.TCPAddr).Port)
	}
	return ports, nil
}

// logAdapter maps Temporal's server logger onto slog, keeping the embedded
// server quiet at Info and below.
type logAdapter struct {
	logger *slog.Logger
}

func newLogAdapter(logger *slog.Logger) tlog.Logger {
	if logger == nil {
		logger = slog.Default()
	}
	return &logAdapter{logger: logger.With("component", "embedded-temporal")}
}

func (a *logAdapter) log(level slog.Level, msg string, tags []tag.Tag) {
	if !a.logger.Enabled(context.Background(), level) {
		return
	}
	attrs := make([]any, 0, len(tags)*2)
	for _, t := range tags {
		attrs = append(attrs, t.Key(), t.Value())
	}
	a.logger.Log(context.Background(), level, msg, attrs...)
}

func (a *logAdapter) Debug(msg string, tags ...tag.Tag)  {}
func (a *logAdapter) Info(msg string, tags ...tag.Tag)   {}
func (a *logAdapter) Warn(msg string, tags ...tag.Tag)   { a.log(slog.LevelWarn, msg, tags) }
func (a *logAdapter) Error(msg string, tags ...tag.Tag)  { a.log(slog.LevelError, msg, tags) }
func (a *logAdapter) DPanic(msg string, tags ...tag.Tag) { a.log(slog.LevelError, msg, tags) }
func (a *logAdapter) Panic(msg string, tags ...tag.Tag)  { a.log(slog.LevelError, msg, tags) }
func (a *logAdapter) Fatal(msg string, tags ...tag.Tag)  { a.log(slog.LevelError, msg, tags) }
