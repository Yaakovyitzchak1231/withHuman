// Package slack is the open edition's Slack adapter. This file covers the
// organization-level OAuth install; delivery and identity lookup build on
// the bot token it yields.
package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// DefaultBaseURL is Slack's API origin. Tests point the client at an
// in-process fake.
const DefaultBaseURL = "https://slack.com"

// BotScopes is what an install asks for: sending direct messages,
// and finding members by email so their Slack contact method
// appears without setup. Kept here so the manifest and the authorize URL
// cannot disagree.
var BotScopes = []string{"chat:write", "users:read", "users:read.email", "im:write"}

type Client struct {
	baseURL      string
	clientID     string
	clientSecret string
	httpClient   *http.Client
}

func NewClient(baseURL, clientID, clientSecret string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"), clientID: clientID, clientSecret: clientSecret,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) AuthorizeURL(state, redirectURL string) string {
	query := url.Values{}
	query.Set("client_id", c.clientID)
	query.Set("scope", strings.Join(BotScopes, ","))
	query.Set("state", state)
	query.Set("redirect_uri", redirectURL)
	return c.baseURL + "/oauth/v2/authorize?" + query.Encode()
}

// oauthAccessResponse is the subset of oauth.v2.access this adapter
// reads. Slack answers HTTP 200 with ok=false on failure, so the ok flag
// is the real status.
type oauthAccessResponse struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error"`
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	BotUserID   string `json:"bot_user_id"`
	Team        struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"team"`
}

func (c *Client) Exchange(ctx context.Context, input application.SlackExchangeInput) (application.SlackInstallation, error) {
	if err := input.Validate(); err != nil {
		return application.SlackInstallation{}, err
	}
	// Slack documents the app credentials as form fields and also accepts
	// them as HTTP basic auth; send both so the exchange cannot fail on
	// which one a given endpoint honours.
	form := url.Values{}
	form.Set("code", input.Code)
	form.Set("redirect_uri", input.RedirectURL)
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)
	var response oauthAccessResponse
	if err := c.post(ctx, "/api/oauth.v2.access", form, "", &response); err != nil {
		return application.SlackInstallation{}, err
	}
	if !response.OK {
		return application.SlackInstallation{}, fmt.Errorf("slack refused the install: %s: %w", response.Error, domain.ErrInvalid)
	}
	if response.TokenType != "bot" || response.AccessToken == "" || response.Team.ID == "" || response.BotUserID == "" {
		return application.SlackInstallation{}, fmt.Errorf("slack install response is missing the bot grant: %w", domain.ErrInvalid)
	}
	var scopes []string
	for _, scope := range strings.Split(response.Scope, ",") {
		if scope = strings.TrimSpace(scope); scope != "" {
			scopes = append(scopes, scope)
		}
	}
	return application.SlackInstallation{
		TeamID: response.Team.ID, TeamName: response.Team.Name, BotUserID: response.BotUserID,
		Scopes: scopes, BotToken: response.AccessToken,
	}, nil
}

// Revoke invalidates the bot token. An already invalid token is not an
// error: the outcome the caller wants, a dead token, already holds.
func (c *Client) Revoke(ctx context.Context, botToken string) error {
	var response struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := c.post(ctx, "/api/auth.revoke", url.Values{}, botToken, &response); err != nil {
		return err
	}
	if !response.OK && response.Error != "invalid_auth" && response.Error != "token_revoked" {
		return fmt.Errorf("slack did not revoke the token: %s", response.Error)
	}
	return nil
}

// post sends a form-encoded call. The app credentials travel as HTTP basic
// auth for the OAuth exchange and a bearer token for everything else;
// neither ever appears in an error.
func (c *Client) post(ctx context.Context, path string, form url.Values, bearer string, into any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	} else {
		request.SetBasicAuth(c.clientID, c.clientSecret)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("slack %s: %w", path, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("slack %s returned HTTP %d", path, response.StatusCode)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("slack %s returned an unreadable response: %w", path, err)
	}
	return nil
}

// slackUserResponse is the subset of users.lookupByEmail this adapter reads.
type slackUserResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	User  struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Profile struct {
			DisplayName string `json:"display_name"`
			RealName    string `json:"real_name"`
		} `json:"profile"`
	} `json:"user"`
}

// LookupUserByEmail finds the workspace member behind an address. Slack
// answers users_not_found for an unknown address, which is the ordinary
// "this person is not in Slack" outcome rather than a failure.
func (c *Client) LookupUserByEmail(ctx context.Context, botToken, email string) (application.SlackUser, error) {
	form := url.Values{}
	form.Set("email", email)
	var response slackUserResponse
	if err := c.post(ctx, "/api/users.lookupByEmail", form, botToken, &response); err != nil {
		return application.SlackUser{}, err
	}
	if !response.OK {
		if response.Error == "token_revoked" || response.Error == "account_inactive" {
			return application.SlackUser{}, application.ErrSlackRevoked
		}
		if response.Error == "users_not_found" {
			return application.SlackUser{}, domain.ErrNotFound
		}
		return application.SlackUser{}, fmt.Errorf("slack user lookup failed: %s", response.Error)
	}
	display := response.User.Profile.DisplayName
	if display == "" {
		display = response.User.Profile.RealName
	}
	return application.SlackUser{ID: response.User.ID, Handle: response.User.Name, DisplayName: display}, nil
}

// PostDirectMessage opens the person's DM with the bot and posts into it.
// Opening is idempotent at Slack: it returns the existing conversation.
func (c *Client) PostDirectMessage(ctx context.Context, input application.SlackDirectMessageInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	open := url.Values{}
	open.Set("users", input.SlackUserID)
	var opened struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := c.post(ctx, "/api/conversations.open", open, input.BotToken, &opened); err != nil {
		return err
	}
	if !opened.OK || opened.Channel.ID == "" {
		if opened.Error == "token_revoked" || opened.Error == "account_inactive" {
			return application.ErrSlackRevoked
		}
		return fmt.Errorf("slack would not open a direct message: %s", opened.Error)
	}
	message := map[string]any{"channel": opened.Channel.ID, "text": input.Text}
	if len(input.Blocks) > 0 {
		message["blocks"] = input.Blocks
	}
	var posted struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := c.postJSON(ctx, "/api/chat.postMessage", input.BotToken, message, &posted); err != nil {
		return err
	}
	if !posted.OK {
		if posted.Error == "token_revoked" || posted.Error == "account_inactive" {
			return application.ErrSlackRevoked
		}
		return fmt.Errorf("slack did not post the message: %s", posted.Error)
	}
	return nil
}

// postJSON sends a JSON body with a bearer token; chat.postMessage needs
// JSON to carry blocks.
func (c *Client) postJSON(ctx context.Context, path, bearer string, body any, into any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(string(encoded)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Authorization", "Bearer "+bearer)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("slack %s: %w", path, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("slack %s returned HTTP %d", path, response.StatusCode)
	}
	return json.Unmarshal(payload, into)
}
