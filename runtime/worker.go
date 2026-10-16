package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/blockruntime"
	"github.com/withHumanAI/withHuman/config"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/entitlements"
	"github.com/withHumanAI/withHuman/internal/aapdelivery"
	"github.com/withHumanAI/withHuman/internal/approvalpipeline"
	"github.com/withHumanAI/withHuman/internal/discord"
	"github.com/withHumanAI/withHuman/internal/heartbeat"
	"github.com/withHumanAI/withHuman/internal/orchestration"
	"github.com/withHumanAI/withHuman/internal/postgres"
	"github.com/withHumanAI/withHuman/internal/reviewrouting"
	"github.com/withHumanAI/withHuman/internal/secrets"
	"github.com/withHumanAI/withHuman/internal/slack"
	"github.com/withHumanAI/withHuman/internal/slackconnect"
	"github.com/withHumanAI/withHuman/internal/webhookdelivery"
	"github.com/withHumanAI/withHuman/loggermdc"
	"github.com/withHumanAI/withHuman/notifications"
	"github.com/withHumanAI/withHuman/telemetry"
	"github.com/withHumanAI/withHuman/tenants"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	temporallog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

type WorkerOptions struct {
	Logger *slog.Logger
	// Version is the release the heartbeat reports.
	Version string
	// HeartbeatURL is where the daily installation heartbeat goes; empty
	// sends none. The open edition's main sets it, the hosted edition
	// leaves it empty, and an operator turns it off with
	// WITHHUMAN_TELEMETRY=off. Development builds never send.
	HeartbeatURL string
	// Config is the deployment configuration to compose with. Nil loads
	// it from the environment; the single-process topology supplies one
	// it derived from its embedded services.
	Config *config.Config
	// NotificationChannels are additional delivery channels the edition
	// registers, such as a hosted email provider or mobile push. They join
	// the open channels rather than replacing them.
	NotificationChannels []application.NotificationChannel
	// Hooks observes tenant lifecycle transitions driven from the worker,
	// such as dunning suspension; nil means the no-op open adapter.
	Hooks tenants.TenantHooks
	// Entitlements answers the seat quota that directory provisioning
	// enforces and meters block usage. Nil means the unlimited open adapter.
	Entitlements application.Entitlements
	// RegisterExtra lets an edition register additional workflows and
	// activities on the shared worker before it starts, and start its own
	// cron workflows. The hosted edition's billing workflows land here.
	RegisterExtra func(WorkerContext)
	// Ready is called once the worker is registered, started, and
	// dispatching. The single-process topology waits for it before
	// reporting itself ready; the split worker main leaves it nil.
	Ready func()
	// AuditSink receives the audit events the worker's writes build
	// (escalation ticks, lapsed claims, resumed pipelines, directory
	// sync), the same sink the server composes with. Nil discards them.
	AuditSink db.AuditSink
	// PurgeHooks remove the edition's own rows inside the purge workflow's
	// transaction, the same hooks the server composes with.
	PurgeHooks []db.PurgeHook
	// ApprovalBlocks are the edition's block kinds, the same list the
	// server composes with, so the worker resumes suspended pipelines
	// through the same runtimes.
	ApprovalBlocks []blockruntime.Runtime
}

// WorkerContext is what an edition's extra workflows may build on: the
// worker to register with, the Temporal client and task queue for starting
// crons, and the core services worker-driven policy calls into.
type WorkerContext struct {
	Worker    worker.Worker
	Client    client.Client
	TaskQueue string
	Config    config.Config
	Clock     application.Clock
	Logger    *slog.Logger
	Tenants   *tenants.Service
	// Entitlements is the edition's plan, for workflows that allocate
	// (the hosted directory sync's seat quota); Directory is core's
	// directory-managed membership those workflows apply into.
	Entitlements application.Entitlements
	Directory    application.DirectoryRepository
}

// RunWorker composes and runs the product worker: the approval,
// and tenant workflows, the outbox dispatcher, and the health server. Both
// editions' worker mains call it; edition differences arrive through the
// options.
func RunWorker(ctx context.Context, options WorkerOptions) error {
	logger := loggermdc.Wrap(options.Logger)
	cfg, err := loadConfig(options.Config)
	if err != nil {
		return err
	}
	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	if options.AuditSink != nil {
		store.WithAuditSink(options.AuditSink)
	}
	store.WithPurgeHooks(options.PurgeHooks...)
	stopObservingPool, err := telemetry.ObservePool("worker", store.Pool())
	if err != nil {
		return err
	}
	defer stopObservingPool()
	temporalClient, err := client.Dial(client.Options{HostPort: cfg.TemporalAddress, Namespace: cfg.TemporalNamespace,
		ContextPropagators: []workflow.ContextPropagator{orchestration.NewMDCPropagator()},
		Logger:             temporallog.NewStructuredLogger(logger), MetricsHandler: telemetry.TemporalMetrics(logger)})
	if err != nil {
		return err
	}
	defer temporalClient.Close()
	approvalWorker := worker.New(temporalClient, cfg.TemporalTaskQueue, worker.Options{Interceptors: []interceptor.WorkerInterceptor{orchestration.NewMDCInterceptor()}})
	approvalWorker.RegisterWorkflow(orchestration.ApprovalWorkflow)
	clock := application.RealClock{}
	orchestrator := orchestration.NewTemporal(temporalClient, cfg.TemporalTaskQueue)
	plan := options.Entitlements
	if plan == nil {
		plan = entitlements.NewUnlimited()
	}
	channels := append([]application.NotificationChannel{}, options.NotificationChannels...)
	var sealer *secrets.Sealer
	if cfg.SecretsKey != "" {
		if sealer, err = secrets.NewSealer(cfg.SecretsKey); err != nil {
			return err
		}
		store.WithSealer(sealer)
	}
	if cfg.SlackConfigured() {
		slackClient := slack.NewClient(slack.DefaultBaseURL, cfg.SlackClientID, cfg.SlackClientSecret)
		channels = append(channels, notifications.NewSlackChannel(store, slackClient, cfg.PublicOrigin).WithEventDispatcher(orchestrator))
		slackService := slackconnect.NewService(slackClient, slackClient, store, store, sealer, clock, logger, slackconnect.Config{PublicOrigin: cfg.PublicOrigin, AppID: cfg.SlackAppID, SigningSecret: cfg.SlackSigningSecret}).WithEventDispatcher(orchestrator)
		approvalWorker.RegisterWorkflow(orchestration.SlackEventsWorkflow)
		approvalWorker.RegisterActivity(&orchestration.SlackActivities{Service: slackService, Repository: store, Temporal: orchestrator, Clock: clock})
	}
	if cfg.DiscordConfigured() {
		discordClient := discord.NewClient(discord.DefaultBaseURL, cfg.DiscordClientID, cfg.DiscordClientSecret, cfg.DiscordBotToken)
		channels = append(channels, notifications.NewDiscordChannel(store, discordClient, cfg.PublicOrigin))
	}
	notifier, err := notifications.Build(cfg, logger, channels...)
	if err != nil {
		return err
	}
	// The worker resumes pipelines suspended before asynchronous blocks, so
	// it compiles pipelines with the same registry the server uses; a
	// compile failure at startup only delays resumes to their retries.
	blockRegistryOptions := []approvalpipeline.BlockRegistryOption{
		approvalpipeline.WithEntitlements(plan), approvalpipeline.WithClock(clock),
		approvalpipeline.WithRuntimes(options.ApprovalBlocks...),
	}
	if cfg.WebhookConfigured() {
		webhookClient := webhookdelivery.NewClient(webhookdelivery.Policy{AllowInsecure: cfg.WebhookAllowInsecure, AllowPrivateNetworks: cfg.WebhookAllowPrivateNetworks})
		blockRegistryOptions = append(blockRegistryOptions, approvalpipeline.WithWebhooks(approvalpipeline.WebhookRuntime{Endpoints: store, Deliverer: webhookClient}))
	}
	compiledPipelines := approvalpipeline.NewCompiledPipelineState(store, approvalpipeline.NewBlockRegistry(blockRegistryOptions...))
	if err := compiledPipelines.Initialize(ctx); err != nil {
		logger.Error("worker approval pipeline initialization failed; compiled pipelines are unavailable", "error", err)
	}
	pipelineExecution := approvalpipeline.NewExecutionService(store, store, compiledPipelines, clock)
	routingEvaluation := reviewrouting.NewEvaluationService(store, clock, logger)
	approvalWorker.RegisterActivity(orchestration.NewActivities(store, store, store, notifier, clock,
		orchestration.WithPipeline(pipelineExecution, routingEvaluation), orchestration.WithEscalation(store),
		orchestration.WithNotificationRecords(store), orchestration.WithNotificationWindows(store)))
	approvalWorker.RegisterWorkflow(orchestration.OrganizationDeletionWorkflow)
	approvalWorker.RegisterActivity(orchestration.NewTenantActivities(store, clock))
	if options.RegisterExtra != nil {
		options.RegisterExtra(WorkerContext{
			Worker: approvalWorker, Client: temporalClient, TaskQueue: cfg.TemporalTaskQueue, Config: cfg,
			Clock: clock, Logger: logger, Entitlements: plan, Directory: store,
			Tenants: tenants.NewService(store, orchestrator, options.Hooks, clock, logger, tenants.Config{}),
		})
	}
	if err := approvalWorker.Start(); err != nil {
		return err
	}
	defer approvalWorker.Stop()
	if cfg.SlackConfigured() {
		if err := orchestrator.EnsureSlackEvents(ctx); err != nil {
			return err
		}
	}
	startHeartbeat(ctx, options, cfg, store, clock, logger)
	dispatcher := orchestration.NewApprovalWorkflowDispatcher(store, store, orchestrator, clock, logger)
	go dispatcher.Run(ctx)
	if cfg.WebhookConfigured() {
		// Instances with an AAP receiver exist only when the server could
		// seal their secrets, so without the key there is nothing to send.
		webhookClient := webhookdelivery.NewClient(webhookdelivery.Policy{AllowInsecure: cfg.WebhookAllowInsecure, AllowPrivateNetworks: cfg.WebhookAllowPrivateNetworks})
		go aapdelivery.NewDispatcher(store, webhookClient, sealer, clock, logger).Run(ctx)
	}
	logger.Info("withHuman worker started", "task_queue", cfg.TemporalTaskQueue, "health_address", cfg.WorkerHealthAddress)
	if options.Ready != nil {
		options.Ready()
	}
	if cfg.WorkerHealthAddress == "" {
		// A co-located composition (the single-process topology) reports
		// health through the server's endpoints instead of a second
		// listener.
		<-ctx.Done()
		return nil
	}

	healthServer := &http.Server{Addr: cfg.WorkerHealthAddress, ReadHeaderTimeout: 2 * time.Second}
	healthServer.Handler = http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/livez" && request.URL.Path != "/readyz" {
			http.NotFound(response, request)
			return
		}
		status := http.StatusOK
		payload := map[string]string{"status": "ready"}
		if request.URL.Path == "/readyz" {
			checkContext, cancel := context.WithTimeout(request.Context(), 2*time.Second)
			defer cancel()
			if err := store.Health(checkContext); err != nil {
				status, payload["status"], payload["postgres"] = http.StatusServiceUnavailable, "not_ready", err.Error()
			}
			if _, err := temporalClient.CheckHealth(checkContext, &client.CheckHealthRequest{}); err != nil {
				status, payload["status"], payload["temporal"] = http.StatusServiceUnavailable, "not_ready", err.Error()
			}
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(status)
		_ = json.NewEncoder(response).Encode(payload)
	})
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		_ = healthServer.Shutdown(shutdownContext)
	}()
	err = healthServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// startHeartbeat runs the installation heartbeat when the edition asks for
// it, the operator has not turned telemetry off, and the build is a
// release. Every replica runs one; the database claim sends once a day.
func startHeartbeat(ctx context.Context, options WorkerOptions, cfg config.Config, store *postgres.Store, clock application.Clock, logger *slog.Logger) {
	switch {
	case options.HeartbeatURL == "":
	case !cfg.Telemetry:
		logger.Info("telemetry is off; no installation heartbeat is sent")
	case !heartbeat.ReleaseVersion(options.Version):
		logger.Info("telemetry skipped for a development build", "version", options.Version)
	default:
		logger.Info("telemetry is on: a daily heartbeat sends this installation's random ID and version; set WITHHUMAN_TELEMETRY=off to turn it off",
			"url", options.HeartbeatURL)
		go heartbeat.NewReporter(store, clock, logger, options.HeartbeatURL, options.Version).Run(ctx)
	}
}
