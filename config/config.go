package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL         string
	TemporalAddress     string
	TemporalNamespace   string
	TemporalTaskQueue   string
	HTTPAddress         string
	WorkerHealthAddress string
	PublicOrigin        string
	TrustedProxyCIDRs   []string
	BootstrapSecret     string
	// SessionTTL is a browser session's lifetime; NativeSessionTTL is the
	// longer one a phone app gets, so a reviewer is not signed out of the
	// device that carries their approval prompts. The organization's
	// session max age caps both.
	SessionTTL        time.Duration
	NativeSessionTTL  time.Duration
	CredentialTTL     time.Duration
	ProvisionerTTL    time.Duration
	EnrollmentCodeTTL time.Duration
	// APIKeyMaxTTL caps how far out a personal API key may expire. Zero,
	// the default, lets a key live until revoked, the Cloudflare way.
	APIKeyMaxTTL time.Duration
	// OnboardingSessionTTL bounds one guided setup between the browser and
	// the CLI; the setup code inside it reuses EnrollmentCodeTTL.
	OnboardingSessionTTL time.Duration
	// CLIDownloadURL is where the onboarding page sends people for the
	// withhuman CLI: the base that serves install.sh and cli/<version>/.
	CLIDownloadURL  string
	ShutdownTimeout time.Duration
	// ShutdownDrainDelay is how long the server keeps serving after a stop
	// signal with readiness failing, before it closes its listener, so a
	// load balancer stops routing to it first. Zero, the default, suits a
	// process nothing balances across.
	ShutdownDrainDelay   time.Duration
	SMTPHost             string
	SMTPPort             int
	SMTPUsername         string
	SMTPPassword         string
	SMTPFrom             string
	SlackAppID           string
	SlackSigningSecret   string
	SlackClientID        string
	SlackClientSecret    string
	DiscordClientID      string
	DiscordClientSecret  string
	DiscordBotToken      string
	SecretsKey           string
	GatewayEncryptionKey string
	// GatewayToken is the deployment's gateway token: the shared secret a
	// running gateway process presents on the machine configuration
	// surface. It is deployment configuration, never an organization's
	// credential, because one gateway serves every organization.
	GatewayToken string
	// GatewayPublicURL is where agents reach the gateway (the origin that
	// serves /gateway/mcp), shown on the gateway page. Empty when unknown.
	GatewayPublicURL string
	// Webhook egress flags exist for development and tests: production
	// deployments deliver to public HTTPS only.
	WebhookAllowInsecure        bool
	WebhookAllowPrivateNetworks bool
}

// WebhookConfigured reports whether webhook endpoints can be stored and
// webhook blocks can run. The secrets key seals endpoint signing secrets,
// so it is the single switch.
func (c Config) WebhookConfigured() bool { return c.SecretsKey != "" }

// SlackConfigured reports whether organizations may connect a Slack
// workspace. The app credentials are all or nothing, and because the
// install yields a bot token stored at rest, they also require the
// secrets key that seals it.
func (c Config) SlackConfigured() bool { return c.SlackClientID != "" }

// DiscordConfigured reports whether organizations may connect a Discord
// server and members may link their accounts. A Discord application has
// one bot token, issued in the Developer Portal, so the three settings are
// deployment configuration and all or nothing; nothing reversible is
// stored per organization, so no secrets key is involved.
func (c Config) DiscordConfigured() bool { return c.DiscordClientID != "" }

// SMTPConfigured reports whether email delivery is wired. Without it the
// deployment still runs: notifications fall back to the logging channel and
// invite links stay available through the management API.
func (c Config) SMTPConfigured() bool { return c.SMTPHost != "" }

func Load() (Config, error) {
	config := Config{
		DatabaseURL:                 env("WITHHUMAN_DATABASE_URL", "postgres://withhuman:withhuman@localhost:5432/withhuman?sslmode=disable"),
		TemporalAddress:             env("WITHHUMAN_TEMPORAL_ADDRESS", "localhost:7233"),
		TemporalNamespace:           env("WITHHUMAN_TEMPORAL_NAMESPACE", "default"),
		TemporalTaskQueue:           env("WITHHUMAN_TEMPORAL_TASK_QUEUE", "withhuman-approvals"),
		HTTPAddress:                 env("WITHHUMAN_HTTP_ADDRESS", ":8080"),
		WorkerHealthAddress:         env("WITHHUMAN_WORKER_HEALTH_ADDRESS", ":8081"),
		PublicOrigin:                env("WITHHUMAN_PUBLIC_ORIGIN", "http://localhost:8080"),
		TrustedProxyCIDRs:           split(env("WITHHUMAN_TRUSTED_PROXIES", "")),
		BootstrapSecret:             os.Getenv("WITHHUMAN_BOOTSTRAP_SECRET"),
		SessionTTL:                  envDuration("WITHHUMAN_SESSION_TTL", 7*24*time.Hour),
		NativeSessionTTL:            envDuration("WITHHUMAN_NATIVE_SESSION_TTL", 30*24*time.Hour),
		CredentialTTL:               envDuration("WITHHUMAN_CREDENTIAL_TTL", 90*24*time.Hour),
		ProvisionerTTL:              envDuration("WITHHUMAN_PROVISIONER_TTL", 365*24*time.Hour),
		EnrollmentCodeTTL:           envDuration("WITHHUMAN_ENROLLMENT_CODE_TTL", 10*time.Minute),
		APIKeyMaxTTL:                envDuration("WITHHUMAN_API_KEY_MAX_TTL", 0),
		OnboardingSessionTTL:        envDuration("WITHHUMAN_ONBOARDING_SESSION_TTL", time.Hour),
		CLIDownloadURL:              strings.TrimRight(env("WITHHUMAN_CLI_DOWNLOAD_URL", "https://downloads.withhuman.ai"), "/"),
		ShutdownTimeout:             envDuration("WITHHUMAN_SHUTDOWN_TIMEOUT", 15*time.Second),
		ShutdownDrainDelay:          envDuration("WITHHUMAN_SHUTDOWN_DRAIN_DELAY", 0),
		SMTPHost:                    env("WITHHUMAN_SMTP_HOST", ""),
		SMTPUsername:                env("WITHHUMAN_SMTP_USERNAME", ""),
		SMTPPassword:                env("WITHHUMAN_SMTP_PASSWORD", ""),
		SMTPFrom:                    env("WITHHUMAN_SMTP_FROM", ""),
		SlackAppID:                  env("WITHHUMAN_SLACK_APP_ID", ""),
		SlackSigningSecret:          env("WITHHUMAN_SLACK_SIGNING_SECRET", ""),
		SlackClientID:               env("WITHHUMAN_SLACK_CLIENT_ID", ""),
		SlackClientSecret:           env("WITHHUMAN_SLACK_CLIENT_SECRET", ""),
		DiscordClientID:             env("WITHHUMAN_DISCORD_CLIENT_ID", ""),
		DiscordClientSecret:         os.Getenv("WITHHUMAN_DISCORD_CLIENT_SECRET"),
		DiscordBotToken:             os.Getenv("WITHHUMAN_DISCORD_BOT_TOKEN"),
		SecretsKey:                  os.Getenv("WITHHUMAN_SECRETS_KEY"),
		WebhookAllowInsecure:        env("WITHHUMAN_WEBHOOK_ALLOW_INSECURE", "false") == "true",
		WebhookAllowPrivateNetworks: env("WITHHUMAN_WEBHOOK_ALLOW_PRIVATE_NETWORKS", "false") == "true",
	}
	smtpPort, err := envPort("WITHHUMAN_SMTP_PORT", 587)
	if err != nil {
		return Config{}, err
	}
	config.SMTPPort = smtpPort
	if !strings.HasPrefix(config.PublicOrigin, "http://") && !strings.HasPrefix(config.PublicOrigin, "https://") {
		return Config{}, fmt.Errorf("WITHHUMAN_PUBLIC_ORIGIN must be an absolute HTTP(S) origin")
	}
	// Email is all or nothing: a host without a sender address would
	// accept mail nobody can reply to and fail at every send instead of at
	// startup. Credentials stay optional for unauthenticated relays.
	if config.SMTPHost != "" && config.SMTPFrom == "" {
		return Config{}, fmt.Errorf("WITHHUMAN_SMTP_HOST requires WITHHUMAN_SMTP_FROM")
	}
	if config.SMTPHost == "" && config.SMTPFrom != "" {
		return Config{}, fmt.Errorf("WITHHUMAN_SMTP_FROM requires WITHHUMAN_SMTP_HOST")
	}
	if (config.SlackAppID != "" || config.SlackSigningSecret != "" || config.SlackClientSecret != "") && config.SlackClientID == "" {
		return Config{}, fmt.Errorf("Slack configuration requires WITHHUMAN_SLACK_CLIENT_ID")
	}
	if config.SlackClientID != "" && (config.SlackClientSecret == "" || config.SecretsKey == "") {
		return Config{}, fmt.Errorf("WITHHUMAN_SLACK_CLIENT_ID requires WITHHUMAN_SLACK_CLIENT_SECRET and WITHHUMAN_SECRETS_KEY")
	}
	if config.SlackConfigured() && (config.SlackAppID == "" || config.SlackSigningSecret == "") {
		return Config{}, fmt.Errorf("Slack requires WITHHUMAN_SLACK_APP_ID and WITHHUMAN_SLACK_SIGNING_SECRET")
	}
	if config.DiscordClientID != "" || config.DiscordClientSecret != "" || config.DiscordBotToken != "" {
		if config.DiscordClientID == "" || config.DiscordClientSecret == "" || config.DiscordBotToken == "" {
			return Config{}, fmt.Errorf("WITHHUMAN_DISCORD_CLIENT_ID, WITHHUMAN_DISCORD_CLIENT_SECRET, and WITHHUMAN_DISCORD_BOT_TOKEN are all or nothing")
		}
	}
	return config, nil
}

// envPort reads a TCP port, refusing an unusable value instead of silently
// falling back the way envDuration does: a typo here would send every
// notification to the wrong place.
func envPort(key string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	port, err := strconv.Atoi(value)
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("%s must be a TCP port between 1 and 65535", key)
	}
	return port, nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func split(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)
