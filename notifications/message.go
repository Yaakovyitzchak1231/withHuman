package notifications

import (
	"fmt"
	"mime"
	"net/mail"
	"strings"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// mailMessage is one rendered RFC 5322 message ready for a transport.
type mailMessage struct {
	To      string
	Subject string
	Body    string
}

// approvalPromptMessage renders the reviewer prompt.
//
// The body deliberately carries the tool name, the agent that asked, and
// the deadline, but never the request arguments: mail is forwardable and
// long-lived, while arguments routinely hold customer data. The reviewer
// follows the link and decides in the product, where authorization applies
// and the decision joins the audit trail.
func approvalPromptMessage(request approvalMailRequest, recipient application.NotificationRecipient, address, queueURL string) mailMessage {
	greeting := "Hello,"
	if name := strings.TrimSpace(recipient.DisplayName); name != "" {
		greeting = fmt.Sprintf("Hello %s,", name)
	}
	lines := []string{
		greeting,
		"",
		fmt.Sprintf("%s is waiting for your approval.", request.Tool),
		"",
		fmt.Sprintf("Agent: %s (%s)", request.AgentName, request.AgentInstanceName),
		fmt.Sprintf("Expires: %s", request.DeadlineAt.UTC().Format(time.RFC1123)),
		"",
		"Review it here:",
		queueURL,
		"",
		"The request details are in the app, where your decision is recorded.",
	}
	return mailMessage{
		To:      address,
		Subject: fmt.Sprintf("Approval needed: %s", request.Tool),
		Body:    strings.Join(lines, "\r\n"),
	}
}

// digestListLimit is how many requests a digest names one by one; the
// rest are counted, and the queue lists everything.
const digestListLimit = 10

// digestShown splits a digest's requests into the ones it names and how
// many more it only counts.
func digestShown(requests []domain.ApprovalRequest) ([]domain.ApprovalRequest, int) {
	if len(requests) <= digestListLimit {
		return requests, 0
	}
	return requests[:digestListLimit], len(requests) - digestListLimit
}

// approvalDigestMessage renders a notification window's digest: one line
// per request with its link, then the queue. Like the prompt it names the
// tool, the agent and the deadline, never the arguments.
func approvalDigestMessage(input application.SendApprovalDigestInput, address string, requestURL func(domain.ID) string, queueURL string) mailMessage {
	greeting := "Hello,"
	if name := strings.TrimSpace(input.Recipient.DisplayName); name != "" {
		greeting = fmt.Sprintf("Hello %s,", name)
	}
	shown, more := digestShown(input.Requests)
	lines := []string{
		greeting,
		"",
		fmt.Sprintf("%d requests are waiting for your approval:", len(input.Requests)),
		"",
	}
	for _, request := range shown {
		lines = append(lines,
			fmt.Sprintf("- %s from %s (%s), expires %s", request.Tool, request.AgentName, request.AgentInstanceName, request.DeadlineAt.UTC().Format(time.RFC1123)),
			"  "+requestURL(request.ID))
	}
	if more > 0 {
		lines = append(lines, fmt.Sprintf("And %d more.", more))
	}
	lines = append(lines,
		"",
		"Review them all here:",
		queueURL,
		"",
		"The request details are in the app, where your decisions are recorded.",
	)
	return mailMessage{
		To:      address,
		Subject: fmt.Sprintf("%d approvals waiting", len(input.Requests)),
		Body:    strings.Join(lines, "\r\n"),
	}
}

// invitationMessage renders the invitation email. The invite URL embeds a
// one-time token, so this message is the only place it may appear, and it
// goes to the addressed recipient alone.
func invitationMessage(message application.TransactionalMessage, address string) mailMessage {
	lines := []string{
		"Hello,",
		"",
		fmt.Sprintf("You have been invited to join %s on withHuman.", message.OrganizationName),
		"",
		"Accept your invitation here:",
		message.InviteURL,
		"",
		fmt.Sprintf("The link expires on %s.", message.ExpiresAt.UTC().Format(time.RFC1123)),
		"",
		"If you were not expecting this invitation you can ignore this message.",
	}
	return mailMessage{
		To:      address,
		Subject: fmt.Sprintf("You are invited to %s on withHuman", message.OrganizationName),
		Body:    strings.Join(lines, "\r\n"),
	}
}

// contactVerificationMessage renders the code that proves an address
// belongs to whoever asked for it. The code is the credential, so it
// appears here and nowhere else.
func contactVerificationMessage(message application.TransactionalMessage, address string) mailMessage {
	lines := []string{
		"Hello,",
		"",
		"Use this code to confirm this address for withHuman notifications:",
		"",
		"    " + message.VerificationCode,
		"",
		fmt.Sprintf("The code expires on %s.", message.ExpiresAt.UTC().Format(time.RFC1123)),
		"",
		"If you did not add this address you can ignore this message. It will not",
		"be used until somebody confirms it.",
	}
	return mailMessage{
		To:      address,
		Subject: "Confirm your withHuman notification address",
		Body:    strings.Join(lines, "\r\n"),
	}
}

// signupVerificationMessage renders the founder vouch: the link is the
// only proof that the self-serve signup address belongs to whoever typed
// it, so it appears here and nowhere else.
func signupVerificationMessage(message application.TransactionalMessage, address string) mailMessage {
	lines := []string{
		"Hello,",
		"",
		fmt.Sprintf("Confirm your email address to create %s on withHuman.", message.OrganizationName),
		"",
		"Finish creating your organization here:",
		message.VerifyURL,
		"",
		fmt.Sprintf("The link expires on %s.", message.ExpiresAt.UTC().Format(time.RFC1123)),
		"",
		"If you did not start this signup you can ignore this message. Nothing is",
		"created until the link is opened.",
	}
	return mailMessage{
		To:      address,
		Subject: fmt.Sprintf("Confirm your email to create %s on withHuman", message.OrganizationName),
		Body:    strings.Join(lines, "\r\n"),
	}
}

// signupOfferMessage answers a password login for an address with no
// account. The link is the credential that proves the address, so it
// appears here and nowhere else.
func signupOfferMessage(message application.TransactionalMessage, address string) mailMessage {
	lines := []string{
		"Hello,",
		"",
		"Somebody tried to sign in to withHuman with this email address.",
		"There is no withHuman account for it yet.",
		"",
		"Create your account here:",
		message.VerifyURL,
		"",
		fmt.Sprintf("The link expires on %s.", message.ExpiresAt.UTC().Format(time.RFC1123)),
		"",
		"If this was not you, you can ignore this message. Nothing is",
		"created until the link is opened.",
	}
	return mailMessage{
		To:      address,
		Subject: "Create your withHuman account",
		Body:    strings.Join(lines, "\r\n"),
	}
}

// passwordResetMessage renders the reset link. The link is the credential,
// so it appears here and nowhere else, and only to the address on the
// account.
func passwordResetMessage(message application.TransactionalMessage, address string) mailMessage {
	lines := []string{
		"Hello,",
		"",
		"Somebody asked to reset the password for this withHuman account.",
		"",
		"Set a new password here:",
		message.ResetURL,
		"",
		fmt.Sprintf("The link expires on %s and works once.", message.ExpiresAt.UTC().Format(time.RFC1123)),
		"",
		"If you did not ask for this you can ignore this message. Your password",
		"stays as it is until the link is used.",
	}
	return mailMessage{
		To:      address,
		Subject: "Reset your withHuman password",
		Body:    strings.Join(lines, "\r\n"),
	}
}

// encode renders the message as RFC 5322 bytes. Headers are encoded so
// non-ASCII organization names survive, and the caller's From address is
// used verbatim after validation at construction.
func (m mailMessage) encode(from string, now time.Time) []byte {
	var builder strings.Builder
	builder.WriteString("From: " + from + "\r\n")
	builder.WriteString("To: " + m.To + "\r\n")
	builder.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", m.Subject) + "\r\n")
	builder.WriteString("Date: " + now.UTC().Format(time.RFC1123Z) + "\r\n")
	builder.WriteString("MIME-Version: 1.0\r\n")
	builder.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	builder.WriteString("\r\n")
	builder.WriteString(m.Body)
	builder.WriteString("\r\n")
	return []byte(builder.String())
}

// approvalMailRequest is the subset of an approval request the email needs.
// Keeping it explicit is what stops the arguments from drifting into the
// message body later.
type approvalMailRequest struct {
	Tool              string
	AgentName         string
	AgentInstanceName string
	DeadlineAt        time.Time
}

func validAddress(address string) bool {
	parsed, err := mail.ParseAddress(address)
	return err == nil && parsed.Address != ""
}
