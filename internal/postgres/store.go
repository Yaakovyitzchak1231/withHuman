package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/db"
	"github.com/withHumanAI/withHuman/internal/secrets"
)

type Store struct {
	pool *pgxpool.Pool
	// sealer opens and seals credentials stored reversibly, such as a
	// Slack bot token. Nil when the deployment configured no secrets key,
	// in which case those features refuse rather than store plaintext.
	sealer *secrets.Sealer
	// audit receives every audit event a write builds, inside that
	// write's transaction. The open edition discards them.
	audit db.AuditSink
	// purgeHooks remove an edition's own rows inside a tenant purge.
	purgeHooks []db.PurgeHook
}

// WithSealer attaches the sealer for reversible credentials. It returns
// the same store so composition roots can chain it onto Open.
func (s *Store) WithSealer(sealer *secrets.Sealer) *Store {
	s.sealer = sealer
	return s
}

// WithAuditSink routes the audit events every write builds to the sink,
// replacing the discarding default. Tests pass a db.RecordingAudit; the
// hosted edition its own table.
func (s *Store) WithAuditSink(sink db.AuditSink) *Store {
	s.audit = sink
	return s
}

// WithPurgeHooks adds the edition's purge steps to a tenant purge.
func (s *Store) WithPurgeHooks(hooks ...db.PurgeHook) *Store {
	s.purgeHooks = append(s.purgeHooks, hooks...)
	return s
}

var (
	_ application.IdentityRepository                = (*Store)(nil)
	_ application.AgentRepository                   = (*Store)(nil)
	_ application.ApprovalRepository                = (*Store)(nil)
	_ application.ApprovalWorkflowOutboxRepository  = (*Store)(nil)
	_ application.RequestApprovalPipelineRepository = (*Store)(nil)
	_ application.EscalationPathRepository          = (*Store)(nil)
	_ application.MembershipRepository              = (*Store)(nil)
	_ application.RoleRepository                    = (*Store)(nil)
	_ application.TeamRepository                    = (*Store)(nil)
	_ application.RoutingRecipientResolver          = (*Store)(nil)
	_ application.DirectoryRepository               = (*Store)(nil)
	_ application.TenantRepository                  = (*Store)(nil)
	_ application.ContactMethodRepository           = (*Store)(nil)
	_ application.SlackConnectionRepository         = (*Store)(nil)
	_ application.DiscordConnectionRepository       = (*Store)(nil)
	_ application.WebhookEndpointRepository         = (*Store)(nil)
	_ application.ToolPresentationRepository        = (*Store)(nil)
	_ application.TeamEscalationPolicyRepository    = (*Store)(nil)
	_ application.EscalationRepository              = (*Store)(nil)
	_ application.InstallationRepository            = (*Store)(nil)
)

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	config.ConnConfig.RuntimeParams["application_name"] = "withhuman"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool, audit: db.DiscardAudit{}}, nil
}

func (s *Store) Pool() *pgxpool.Pool              { return s.pool }
func (s *Store) Close()                           { s.pool.Close() }
func (s *Store) Health(ctx context.Context) error { return s.pool.Ping(ctx) }
