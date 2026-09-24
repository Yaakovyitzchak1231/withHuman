package notifications

import (
	"fmt"
	"log/slog"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/config"
)

// Build assembles the registry every composition root uses: the logging
// channel always, SMTP when the deployment configured it, and whatever
// channels the edition supplies. Keeping the assembly here is what stops
// the server, the worker, and the single binary from drifting into
// notifying through different sets of channels.
//
// A configured but invalid SMTP transport is an error rather than a
// warning: silently dropping every email is worse than refusing to start.
func Build(cfg config.Config, logger *slog.Logger, extra ...application.NotificationChannel) (*Registry, error) {
	channels := []application.NotificationChannel{NewLogChannel(logger)}
	if cfg.SMTPConfigured() {
		email, err := NewSMTPChannel(SMTPConfig{
			Host: cfg.SMTPHost, Port: cfg.SMTPPort,
			Username: cfg.SMTPUsername, Password: cfg.SMTPPassword,
			From: cfg.SMTPFrom, PublicOrigin: cfg.PublicOrigin,
		})
		if err != nil {
			return nil, err
		}
		channels = append(channels, email)
	}
	channels = append(channels, extra...)
	registry := NewRegistry(logger, channels...)
	logger.Info(fmt.Sprintf("notification channels registered - %v", registry.Keys()), "channels", registry.Keys())
	return registry, nil
}
