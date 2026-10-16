package notifications

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// SMTPConfig is the open edition's email transport. Host and From are
// required; credentials are optional so an unauthenticated relay, which is
// the common self-hosted and local-development case, works without them.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	// From is the envelope and header sender, such as
	// "withHuman <approvals@example.com>".
	From string
	// PublicOrigin is where recipients reach this deployment, used to build
	// the review link.
	PublicOrigin string
}

// SendMailFunc matches smtp.SendMail so tests can capture what would be
// sent without a network or a mail server.
type SendMailFunc func(addr string, auth smtp.Auth, from string, to []string, msg []byte) error

// SMTPChannel delivers approval prompts and transactional mail over SMTP.
type SMTPChannel struct {
	config SMTPConfig
	// envelopeFrom is the bare address inside config.From. MAIL FROM takes
	// an address alone; relays reject a display name there even though the
	// From header carries it.
	envelopeFrom string
	sendMail     SendMailFunc
	now          func() time.Time
}

// NewSMTPChannel validates the transport configuration up front, so a
// misconfigured deployment fails at startup rather than silently dropping
// every notification.
func NewSMTPChannel(config SMTPConfig) (*SMTPChannel, error) {
	if strings.TrimSpace(config.Host) == "" {
		return nil, fmt.Errorf("SMTP host is required: %w", domain.ErrInvalid)
	}
	if config.Port <= 0 || config.Port > 65535 {
		return nil, fmt.Errorf("SMTP port must be between 1 and 65535: %w", domain.ErrInvalid)
	}
	from, err := mail.ParseAddress(config.From)
	if err != nil || from.Address == "" {
		return nil, fmt.Errorf("SMTP from address must be a valid email address: %w", domain.ErrInvalid)
	}
	if config.PublicOrigin == "" {
		return nil, fmt.Errorf("public origin is required to build review links: %w", domain.ErrInvalid)
	}
	return &SMTPChannel{config: config, envelopeFrom: from.Address, sendMail: smtp.SendMail, now: time.Now}, nil
}

func (c *SMTPChannel) Key() application.NotificationChannelKey {
	return application.NotificationChannelEmail
}

// SendApprovalPrompt emails every recipient that has an address. One
// recipient's failure does not stop the others: partial delivery beats
// none when a request is waiting on a human.
func (c *SMTPChannel) SendApprovalPrompt(ctx context.Context, input application.SendApprovalPromptInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	request := approvalMailRequest{
		Tool: input.Request.Tool, AgentName: input.Request.AgentName,
		AgentInstanceName: input.Request.AgentInstanceName, DeadlineAt: input.Request.DeadlineAt,
	}
	queueURL := c.requestURL(input.Request.ID)
	var failures []error
	for _, recipient := range input.Recipients {
		for _, address := range recipient.Emails {
			if !validAddress(address) {
				continue
			}
			if err := c.send(approvalPromptMessage(request, recipient, address, queueURL)); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

// SendApprovalDigest emails the digest to every address on the narrowed
// recipient.
func (c *SMTPChannel) SendApprovalDigest(ctx context.Context, input application.SendApprovalDigestInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	var failures []error
	for _, address := range input.Recipient.Emails {
		if !validAddress(address) {
			continue
		}
		if err := c.send(approvalDigestMessage(input, address, c.requestURL, c.queueURL())); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (c *SMTPChannel) SendTransactional(ctx context.Context, input application.SendTransactionalInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	// A code bound for an edition kind (a phone number) is that kind's
	// channel's to deliver.
	if input.Kind != "" && input.Kind != domain.ContactEmail {
		return nil
	}
	if !validAddress(input.Address) {
		return fmt.Errorf("recipient email is not a valid address: %w", domain.ErrInvalid)
	}
	switch input.Message.Kind {
	case application.TransactionalInvitation:
		return c.send(invitationMessage(input.Message, input.Address))
	case application.TransactionalContactVerification:
		return c.send(contactVerificationMessage(input.Message, input.Address))
	case application.TransactionalSignupVerification:
		return c.send(signupVerificationMessage(input.Message, input.Address))
	case application.TransactionalSignupOffer:
		return c.send(signupOfferMessage(input.Message, input.Address))
	case application.TransactionalPasswordReset:
		return c.send(passwordResetMessage(input.Message, input.Address))
	default:
		return fmt.Errorf("no template for message kind %q: %w", input.Message.Kind, domain.ErrInvalid)
	}
}

// send never includes the message body in its error: an invitation body
// carries a one-time token.
func (c *SMTPChannel) send(message mailMessage) error {
	address := net.JoinHostPort(c.config.Host, fmt.Sprint(c.config.Port))
	var auth smtp.Auth
	if c.config.Username != "" {
		auth = smtp.PlainAuth("", c.config.Username, c.config.Password, c.config.Host)
	}
	if err := c.sendMail(address, auth, c.envelopeFrom, []string{message.To}, message.encode(c.config.From, c.now())); err != nil {
		return fmt.Errorf("send mail to %s: %w", message.To, err)
	}
	return nil
}

func (c *SMTPChannel) requestURL(requestID domain.ID) string {
	return c.queueURL() + "/" + string(requestID)
}

func (c *SMTPChannel) queueURL() string {
	return strings.TrimRight(c.config.PublicOrigin, "/") + "/queue"
}
