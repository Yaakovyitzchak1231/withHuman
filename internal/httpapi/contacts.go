package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/contacts"
)

// Contact methods are self-service: the routes act on the caller's own
// account and carry no permission beyond being signed in.
func (a *API) installContactRoutes(group *gin.RouterGroup) {
	a.handle(group, http.MethodGet, "/me/contact-methods", AccessSelf, a.listContactMethods)
	a.handle(group, http.MethodPost, "/me/contact-methods", AccessSelf, a.addContactMethod)
	a.handle(group, http.MethodPost, "/me/contact-methods/:id/verify", AccessSelf, a.verifyContactMethod)
	a.handle(group, http.MethodPost, "/me/contact-methods/:id/resend", AccessSelf, a.resendContactMethod)
	a.handle(group, http.MethodDelete, "/me/contact-methods/:id", AccessSelf, a.removeContactMethod)
	a.handle(group, http.MethodGet, "/me/notification-rules", AccessSelf, a.listNotificationRules)
	a.handle(group, http.MethodPut, "/me/notification-rules/:urgency", AccessSelf, a.saveNotificationRules)
}

type notificationRuleResponse struct {
	DelaySeconds    int64      `json:"delay_seconds"`
	ContactMethodID *domain.ID `json:"contact_method_id"`
}

type notificationRulesResponse struct {
	Standard   []notificationRuleResponse `json:"standard"`
	Interrupt  []notificationRuleResponse `json:"interrupt"`
	Customized map[string]bool            `json:"customized"`
}

func newNotificationRulesResponse(ladders contacts.Ladders) notificationRulesResponse {
	render := func(rungs []application.NotificationRung) []notificationRuleResponse {
		out := make([]notificationRuleResponse, 0, len(rungs))
		for _, rung := range rungs {
			entry := notificationRuleResponse{DelaySeconds: int64(rung.Delay / time.Second)}
			if rung.ContactMethodID != "" {
				id := rung.ContactMethodID
				entry.ContactMethodID = &id
			}
			out = append(out, entry)
		}
		return out
	}
	return notificationRulesResponse{
		Standard: render(ladders.Standard), Interrupt: render(ladders.Interrupt),
		Customized: map[string]bool{
			"standard":  ladders.Customized[domain.NotificationStandard],
			"interrupt": ladders.Customized[domain.NotificationInterrupt],
		},
	}
}

func (a *API) listNotificationRules(c *gin.Context) {
	ladders, err := a.contacts.Rules(c.Request.Context(), currentPrincipal(c))
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, newNotificationRulesResponse(ladders))
}

func (a *API) saveNotificationRules(c *gin.Context) {
	urgency := domain.NotificationUrgency(c.Param("urgency"))
	if !urgency.Valid() {
		a.writeError(c, fmt.Errorf("urgency must be standard or interrupt: %w", domain.ErrInvalid))
		return
	}
	var input struct {
		Rules []struct {
			DelaySeconds    int64  `json:"delay_seconds"`
			ContactMethodID string `json:"contact_method_id"`
		} `json:"rules" binding:"required"`
	}
	if !bindStrict(c, &input) {
		return
	}
	rungs := make([]application.NotificationRung, 0, len(input.Rules))
	for _, rule := range input.Rules {
		rungs = append(rungs, application.NotificationRung{
			Delay: time.Duration(rule.DelaySeconds) * time.Second, ContactMethodID: domain.ID(rule.ContactMethodID),
		})
	}
	ladders, err := a.contacts.SaveRules(c.Request.Context(), currentPrincipal(c), urgency, rungs)
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, newNotificationRulesResponse(ladders))
}

type contactMethodResponse struct {
	ID             domain.ID                `json:"id"`
	Kind           domain.ContactMethodKind `json:"kind"`
	Value          string                   `json:"value"`
	OrganizationID domain.ID                `json:"organization_id,omitempty"`
	Label          string                   `json:"label,omitempty"`
	Available      bool                     `json:"available"`
	Verified       bool                     `json:"verified"`
	VerifiedAt     *time.Time               `json:"verified_at,omitempty"`
	Primary        bool                     `json:"primary"`
	CreatedAt      time.Time                `json:"created_at"`
}

func newContactMethodResponse(method domain.ContactMethod) contactMethodResponse {
	return contactMethodResponse{
		ID: method.ID, Kind: method.Kind, Value: method.Value,
		OrganizationID: method.OrganizationID, Label: method.Label,
		Available: method.Available(), Verified: method.Verified(), VerifiedAt: method.VerifiedAt,
		Primary: method.Primary, CreatedAt: method.CreatedAt,
	}
}

func newContactMethodResponses(methods []domain.ContactMethod) []contactMethodResponse {
	responses := make([]contactMethodResponse, len(methods))
	for index, method := range methods {
		responses[index] = newContactMethodResponse(method)
	}
	return responses
}

// listContactMethods also names connected chat workspaces and servers,
// so the guides can explain whether linking is possible yet.
func (a *API) listContactMethods(c *gin.Context) {
	principal := currentPrincipal(c)
	methods, err := a.contacts.List(c.Request.Context(), principal)
	if err != nil {
		a.writeError(c, err)
		return
	}
	var workspace any
	if a.slack != nil {
		teamName, connected, err := a.slack.Workspace(c.Request.Context(), principal)
		if err != nil {
			a.writeError(c, err)
			return
		}
		if connected {
			workspace = gin.H{"team_name": teamName}
		}
	}
	var server any
	if a.discord != nil {
		guildName, connected, err := a.discord.Server(c.Request.Context(), principal)
		if err != nil {
			a.writeError(c, err)
			return
		}
		if connected {
			server = gin.H{"guild_name": guildName}
		}
	}
	c.JSON(http.StatusOK, gin.H{"contact_methods": newContactMethodResponses(methods), "slack_workspace": workspace, "discord_server": server})
}

func (a *API) addContactMethod(c *gin.Context) {
	var input struct {
		Kind  domain.ContactMethodKind `json:"kind"`
		Value string                   `json:"value" binding:"required"`
		// Consent is the person's express agreement to be messaged at a
		// phone number, given on an unchecked box; phone kinds refuse
		// without it.
		Consent bool `json:"consent"`
	}
	if !bindStrict(c, &input) {
		return
	}
	if input.Kind == "" {
		input.Kind = domain.ContactEmail
	}
	challenge, err := a.contacts.Add(c.Request.Context(), currentPrincipal(c), input.Kind, input.Value, input.Consent)
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, newChallengeResponse(challenge))
}

func (a *API) verifyContactMethod(c *gin.Context) {
	var input struct {
		Code string `json:"code" binding:"required"`
	}
	if !bindStrict(c, &input) {
		return
	}
	method, err := a.contacts.Verify(c.Request.Context(), currentPrincipal(c), domain.ID(c.Param("id")), input.Code)
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"contact_method": newContactMethodResponse(method)})
}

func (a *API) resendContactMethod(c *gin.Context) {
	challenge, err := a.contacts.Resend(c.Request.Context(), currentPrincipal(c), domain.ID(c.Param("id")))
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, newChallengeResponse(challenge))
}

// newChallengeResponse carries whatever the kind's adapter handed the
// person alongside the method (the hosted WhatsApp kind's confirmation
// link and QR code). Those fields are the only copy of the code, handed
// to the owner who just asked for it and never listed again.
func newChallengeResponse(challenge contacts.Challenge) gin.H {
	response := gin.H{"contact_method": newContactMethodResponse(challenge.Method)}
	for key, value := range challenge.Extras {
		response[key] = value
	}
	return response
}

func (a *API) removeContactMethod(c *gin.Context) {
	if err := a.contacts.Remove(c.Request.Context(), currentPrincipal(c), domain.ID(c.Param("id"))); err != nil {
		a.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
