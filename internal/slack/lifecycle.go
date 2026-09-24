package slack

import (
	"context"
	"fmt"
	"net/url"

	"github.com/withHumanAI/withHuman/application"
)

func (c *Client) CheckToken(ctx context.Context, in application.SlackTokenInput) (application.SlackTokenIdentity, error) {
	if err := in.Validate(); err != nil {
		return application.SlackTokenIdentity{}, err
	}
	var response struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		TeamID string `json:"team_id"`
		UserID string `json:"user_id"`
	}
	if err := c.post(ctx, "/api/auth.test", url.Values{}, in.BotToken, &response); err != nil {
		return application.SlackTokenIdentity{}, err
	}
	if !response.OK {
		if response.Error == "token_revoked" || response.Error == "account_inactive" {
			return application.SlackTokenIdentity{}, application.ErrSlackRevoked
		}
		return application.SlackTokenIdentity{}, fmt.Errorf("Slack token check was inconclusive: %s", response.Error)
	}
	if response.TeamID == "" || response.UserID == "" {
		return application.SlackTokenIdentity{}, fmt.Errorf("Slack token check returned no identity")
	}
	return application.SlackTokenIdentity{TeamID: response.TeamID, BotUserID: response.UserID}, nil
}
