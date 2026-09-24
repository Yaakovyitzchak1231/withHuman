// Package discord is the open edition's Discord adapter: the two OAuth2
// grants (an organization install and a person's identity link) and the
// bot-token calls that confirm guild membership and send direct messages.
package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// DefaultBaseURL is Discord's origin. Tests point the client at an
// in-process fake.
const DefaultBaseURL = "https://discord.com"

const (
	// A bot that only sends direct messages needs no guild permissions.
	installPermissions = "0"
	// The codes Discord attaches to a JSON error body.
	codeUnknownMember  = 10007
	codeUnknownUser    = 10013
	codeUnknownGuild   = 10004
	codeCannotSendToDM = 50007
)

type Client struct {
	baseURL      string
	clientID     string
	clientSecret string
	botToken     string
	httpClient   *http.Client
}

func NewClient(baseURL, clientID, clientSecret, botToken string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"), clientID: clientID, clientSecret: clientSecret, botToken: botToken,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) AuthorizeInstallURL(state, redirectURL string) string {
	return c.authorizeURL("bot", state, redirectURL, url.Values{"permissions": {installPermissions}})
}

func (c *Client) AuthorizeLinkURL(state, redirectURL string) string {
	return c.authorizeURL("identify", state, redirectURL, nil)
}

func (c *Client) authorizeURL(scope, state, redirectURL string, extra url.Values) string {
	query := url.Values{}
	query.Set("client_id", c.clientID)
	query.Set("scope", scope)
	query.Set("response_type", "code")
	query.Set("state", state)
	query.Set("redirect_uri", redirectURL)
	for key, values := range extra {
		query[key] = values
	}
	return c.baseURL + "/oauth2/authorize?" + query.Encode()
}

// tokenResponse is the subset of the token endpoint's answer this adapter
// reads. A bot install returns the guild the bot joined alongside a user
// token neither flow keeps.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Guild       *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"guild"`
}

// ExchangeInstall trades the callback code for the guild the bot was
// added to. The guild in the response, not the guild_id the browser
// carried, is what proves where the bot went.
func (c *Client) ExchangeInstall(ctx context.Context, input application.DiscordExchangeInput) (application.DiscordInstallation, error) {
	if err := input.Validate(); err != nil {
		return application.DiscordInstallation{}, err
	}
	response, err := c.exchange(ctx, input)
	if err != nil {
		return application.DiscordInstallation{}, err
	}
	if response.Guild == nil || response.Guild.ID == "" {
		return application.DiscordInstallation{}, fmt.Errorf("discord install response carries no guild: %w", domain.ErrInvalid)
	}
	return application.DiscordInstallation{GuildID: response.Guild.ID, GuildName: response.Guild.Name}, nil
}

// IdentifyUser trades a link code for the account behind it. The user
// token lives only for the one call that reads the profile.
func (c *Client) IdentifyUser(ctx context.Context, input application.DiscordExchangeInput) (application.DiscordUser, error) {
	if err := input.Validate(); err != nil {
		return application.DiscordUser{}, err
	}
	response, err := c.exchange(ctx, input)
	if err != nil {
		return application.DiscordUser{}, err
	}
	if response.AccessToken == "" {
		return application.DiscordUser{}, fmt.Errorf("discord link response carries no token: %w", domain.ErrInvalid)
	}
	var user userResponse
	if err := c.call(ctx, http.MethodGet, "/api/v10/users/@me", "Bearer "+response.AccessToken, nil, &user); err != nil {
		return application.DiscordUser{}, err
	}
	return user.toDomain(), nil
}

func (c *Client) exchange(ctx context.Context, input application.DiscordExchangeInput) (tokenResponse, error) {
	form := url.Values{}
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)
	form.Set("grant_type", "authorization_code")
	form.Set("code", input.Code)
	form.Set("redirect_uri", input.RedirectURL)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var response tokenResponse
	status, body, err := c.do(request)
	if err != nil {
		return tokenResponse{}, err
	}
	if status != http.StatusOK {
		// The token endpoint answers OAuth-shaped errors, not Discord's
		// usual {message, code}; a bad or replayed code is a bad request.
		var failure struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &failure)
		if status == http.StatusBadRequest {
			return tokenResponse{}, fmt.Errorf("discord refused the code: %s: %w", failure.Error, domain.ErrInvalid)
		}
		return tokenResponse{}, fmt.Errorf("discord token exchange returned HTTP %d: %s", status, failure.Error)
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return tokenResponse{}, fmt.Errorf("discord token exchange returned an unreadable response: %w", err)
	}
	return response, nil
}

// LeaveGuild makes the bot leave. A guild it is no longer in is the
// outcome the caller wants already.
func (c *Client) LeaveGuild(ctx context.Context, guildID string) error {
	if strings.TrimSpace(guildID) == "" {
		return fmt.Errorf("discord guild ID is required: %w", domain.ErrInvalid)
	}
	err := c.call(ctx, http.MethodDelete, "/api/v10/users/@me/guilds/"+url.PathEscape(guildID), c.botAuthorization(), nil, nil)
	var apiErr *apiError
	if errors.As(err, &apiErr) && (apiErr.Status == http.StatusNotFound || apiErr.Code == codeUnknownGuild) {
		return nil
	}
	return err
}

// userResponse is the subset of a user object this adapter reads. The
// global name is the display name people chose; username is the handle.
type userResponse struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	GlobalName string `json:"global_name"`
}

func (u userResponse) toDomain() application.DiscordUser {
	display := u.GlobalName
	if display == "" {
		display = u.Username
	}
	return application.DiscordUser{ID: u.ID, Username: u.Username, DisplayName: display}
}

// GetGuildMember answers ErrNotFound for someone who is not in the guild,
// which is the ordinary "this person cannot be messaged" outcome rather
// than a failure.
func (c *Client) GetGuildMember(ctx context.Context, input application.DiscordGuildMemberInput) (application.DiscordUser, error) {
	if err := input.Validate(); err != nil {
		return application.DiscordUser{}, err
	}
	var member struct {
		Nick string       `json:"nick"`
		User userResponse `json:"user"`
	}
	path := "/api/v10/guilds/" + url.PathEscape(input.GuildID) + "/members/" + url.PathEscape(input.UserID)
	err := c.call(ctx, http.MethodGet, path, c.botAuthorization(), nil, &member)
	var apiErr *apiError
	if errors.As(err, &apiErr) && (apiErr.Status == http.StatusNotFound || apiErr.Code == codeUnknownMember || apiErr.Code == codeUnknownUser) {
		return application.DiscordUser{}, domain.ErrNotFound
	}
	if err != nil {
		return application.DiscordUser{}, err
	}
	user := member.User.toDomain()
	if member.Nick != "" {
		user.DisplayName = member.Nick
	}
	return user, nil
}

// SendDirectMessage opens the person's DM with the bot and posts into it.
// Opening is idempotent at Discord: it returns the existing channel.
func (c *Client) SendDirectMessage(ctx context.Context, input application.DiscordDirectMessageInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	var channel struct {
		ID string `json:"id"`
	}
	if err := c.call(ctx, http.MethodPost, "/api/v10/users/@me/channels", c.botAuthorization(),
		map[string]any{"recipient_id": input.DiscordUserID}, &channel); err != nil {
		return fmt.Errorf("discord would not open a direct message: %w", err)
	}
	if channel.ID == "" {
		return fmt.Errorf("discord opened a direct message without an id")
	}
	message := map[string]any{"content": input.Content}
	if len(input.Embeds) > 0 {
		message["embeds"] = input.Embeds
	}
	if len(input.Components) > 0 {
		message["components"] = input.Components
	}
	err := c.call(ctx, http.MethodPost, "/api/v10/channels/"+url.PathEscape(channel.ID)+"/messages", c.botAuthorization(), message, nil)
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.Code == codeCannotSendToDM {
		return fmt.Errorf("discord user does not accept direct messages from this server: %w", err)
	}
	if err != nil {
		return fmt.Errorf("discord did not post the message: %w", err)
	}
	return nil
}

func (c *Client) botAuthorization() string { return "Bot " + c.botToken }

// apiError is Discord's JSON error envelope with the HTTP status it came
// with. Neither the request nor the token appears in it.
type apiError struct {
	Status  int
	Code    int
	Message string
}

func (e *apiError) Error() string {
	return "discord returned HTTP " + strconv.Itoa(e.Status) + " (" + strconv.Itoa(e.Code) + "): " + e.Message
}

// call sends one JSON request with the given authorization and decodes a
// JSON answer into `into` when the caller wants one. Every non-2xx answer
// becomes an apiError so callers can map the codes that matter.
func (c *Client) call(ctx context.Context, method, path, authorization string, body any, into any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", authorization)
	status, payload, err := c.do(request)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		failure := &apiError{Status: status}
		var envelope struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(payload, &envelope) == nil {
			failure.Code, failure.Message = envelope.Code, envelope.Message
		}
		return failure
	}
	if into == nil || len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, into); err != nil {
		return fmt.Errorf("discord %s returned an unreadable response: %w", path, err)
	}
	return nil
}

func (c *Client) do(request *http.Request) (int, []byte, error) {
	response, err := c.httpClient.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("discord %s: %w", request.URL.Path, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return response.StatusCode, payload, nil
}
