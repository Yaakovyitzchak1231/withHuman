package discord_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/discord"
)

const (
	testClientID     = "1234567890"
	testClientSecret = "app-secret"
	testBotToken     = "bot-token"
	installRedirect  = "https://approvals.example.com/auth/discord/callback"
	linkRedirect     = "https://approvals.example.com/auth/discord/link/callback"
)

// fakeDiscord answers the OAuth2 token endpoint and the REST calls the
// adapter makes, with the response shapes Discord documents.
type fakeDiscord struct {
	server   *httptest.Server
	exchange func(code, redirect string) (int, map[string]any)
	left     []string
	opened   []string
	posted   []map[string]any
	dmStatus int
}

func newFakeDiscord(t *testing.T) *fakeDiscord {
	t.Helper()
	fake := &fakeDiscord{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("client_id") != testClientID || r.Form.Get("client_secret") != testClientSecret || r.Form.Get("grant_type") != "authorization_code" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_client"})
			return
		}
		status, body := fake.exchange(r.Form.Get("code"), r.Form.Get("redirect_uri"))
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	})
	mux.HandleFunc("GET /api/v10/users/@me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer user-access-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "401: Unauthorized", "code": 0})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "100200300", "username": "ana", "global_name": "Ana S"})
	})
	mux.HandleFunc("GET /api/v10/guilds/{guild}/members/{user}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bot "+testBotToken {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "401: Unauthorized", "code": 0})
			return
		}
		if r.PathValue("guild") != "G0ACME" {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "boom", "code": 0})
			return
		}
		if r.PathValue("user") != "100200300" {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "Unknown Member", "code": 10007})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"nick": "Ana at Acme", "user": map[string]any{"id": "100200300", "username": "ana", "global_name": "Ana S"}})
	})
	mux.HandleFunc("POST /api/v10/users/@me/channels", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		recipient, _ := body["recipient_id"].(string)
		fake.opened = append(fake.opened, recipient)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "D" + recipient})
	})
	mux.HandleFunc("POST /api/v10/channels/{channel}/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bot "+testBotToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["channel"] = r.PathValue("channel")
		fake.posted = append(fake.posted, body)
		if fake.dmStatus != 0 {
			w.WriteHeader(fake.dmStatus)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "Cannot send messages to this user", "code": 50007})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "M1"})
	})
	mux.HandleFunc("DELETE /api/v10/users/@me/guilds/{guild}", func(w http.ResponseWriter, r *http.Request) {
		fake.left = append(fake.left, r.PathValue("guild"))
		if r.PathValue("guild") == "G0GONE" {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "Unknown Guild", "code": 10004})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	fake.exchange = func(code, redirect string) (int, map[string]any) {
		switch {
		case code == "install-code" && redirect == installRedirect:
			return http.StatusOK, map[string]any{
				"access_token": "install-access-token", "token_type": "Bearer", "scope": "bot",
				"guild": map[string]any{"id": "G0ACME", "name": "Acme"},
			}
		case code == "link-code" && redirect == linkRedirect:
			return http.StatusOK, map[string]any{"access_token": "user-access-token", "token_type": "Bearer", "scope": "identify"}
		default:
			return http.StatusBadRequest, map[string]any{"error": "invalid_grant", "error_description": "Invalid \"code\" in request."}
		}
	}
	return fake
}

func newClient(fake *fakeDiscord) *discord.Client {
	return discord.NewClient(fake.server.URL, testClientID, testClientSecret, testBotToken)
}

func TestAuthorizeURLsCarryTheirScopes(t *testing.T) {
	client := discord.NewClient("https://discord.example", testClientID, testClientSecret, testBotToken)
	install := client.AuthorizeInstallURL("state-1", installRedirect)
	for _, expected := range []string{
		"https://discord.example/oauth2/authorize?", "client_id=1234567890", "scope=bot", "permissions=0",
		"response_type=code", "state=state-1", "redirect_uri=https%3A%2F%2Fapprovals.example.com%2Fauth%2Fdiscord%2Fcallback",
	} {
		if !strings.Contains(install, expected) {
			t.Fatalf("install URL missing %q: %s", expected, install)
		}
	}
	link := client.AuthorizeLinkURL("state-2", linkRedirect)
	for _, expected := range []string{"scope=identify", "state=state-2", "response_type=code"} {
		if !strings.Contains(link, expected) {
			t.Fatalf("link URL missing %q: %s", expected, link)
		}
	}
	if strings.Contains(link, "permissions") || strings.Contains(link, "scope=bot") {
		t.Fatalf("link URL asks for the bot: %s", link)
	}
}

func TestExchangeInstallReturnsTheGuild(t *testing.T) {
	fake := newFakeDiscord(t)
	installation, err := newClient(fake).ExchangeInstall(context.Background(), application.DiscordExchangeInput{Code: "install-code", RedirectURL: installRedirect})
	if err != nil || installation.GuildID != "G0ACME" || installation.GuildName != "Acme" {
		t.Fatalf("ExchangeInstall = %+v, %v", installation, err)
	}
}

// A bad or replayed code is a bad request, distinct from a transport
// failure, so the callback can tell the admin to start again.
func TestExchangeRejectsABadCode(t *testing.T) {
	fake := newFakeDiscord(t)
	_, err := newClient(fake).ExchangeInstall(context.Background(), application.DiscordExchangeInput{Code: "stale", RedirectURL: installRedirect})
	if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("bad code error = %v", err)
	}
}

// An install answered without a guild means the bot went nowhere; it must
// not become a connection with an empty guild id.
func TestExchangeInstallRefusesAGrantWithoutAGuild(t *testing.T) {
	fake := newFakeDiscord(t)
	fake.exchange = func(string, string) (int, map[string]any) {
		return http.StatusOK, map[string]any{"access_token": "x", "token_type": "Bearer", "scope": "bot"}
	}
	if _, err := newClient(fake).ExchangeInstall(context.Background(), application.DiscordExchangeInput{Code: "install-code", RedirectURL: installRedirect}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("guildless grant = %v, want invalid", err)
	}
}

func TestIdentifyUserReadsTheProfileWithTheUserToken(t *testing.T) {
	fake := newFakeDiscord(t)
	user, err := newClient(fake).IdentifyUser(context.Background(), application.DiscordExchangeInput{Code: "link-code", RedirectURL: linkRedirect})
	if err != nil || user.ID != "100200300" || user.Username != "ana" || user.DisplayName != "Ana S" {
		t.Fatalf("IdentifyUser = %+v, %v", user, err)
	}
}

func TestGetGuildMemberDistinguishesAbsenceFromFailure(t *testing.T) {
	fake := newFakeDiscord(t)
	client := newClient(fake)
	member, err := client.GetGuildMember(context.Background(), application.DiscordGuildMemberInput{GuildID: "G0ACME", UserID: "100200300"})
	if err != nil || member.ID != "100200300" || member.DisplayName != "Ana at Acme" || member.Username != "ana" {
		t.Fatalf("member = %+v, %v", member, err)
	}
	if _, err := client.GetGuildMember(context.Background(), application.DiscordGuildMemberInput{GuildID: "G0ACME", UserID: "999"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown member = %v, want not found", err)
	}
	if _, err := client.GetGuildMember(context.Background(), application.DiscordGuildMemberInput{GuildID: "G0OTHER", UserID: "100200300"}); err == nil || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("server failure = %v, want a real failure", err)
	}
}

func TestSendDirectMessageOpensThenPosts(t *testing.T) {
	fake := newFakeDiscord(t)
	if err := newClient(fake).SendDirectMessage(context.Background(), application.DiscordDirectMessageInput{
		DiscordUserID: "100200300", Content: "stripe.refund needs your approval",
		Embeds:     json.RawMessage(`[{"title":"Approval needed"}]`),
		Components: json.RawMessage(`[{"type":1,"components":[{"type":2,"style":5,"label":"Open","url":"https://x"}]}]`),
	}); err != nil {
		t.Fatalf("SendDirectMessage: %v", err)
	}
	if len(fake.opened) != 1 || fake.opened[0] != "100200300" {
		t.Fatalf("opened = %v", fake.opened)
	}
	if len(fake.posted) != 1 || fake.posted[0]["channel"] != "D100200300" || fake.posted[0]["embeds"] == nil || fake.posted[0]["components"] == nil {
		t.Fatalf("posted = %v", fake.posted)
	}
}

// A person whose privacy settings block messages from server members is
// a delivery failure to report, never a crash and never a silent success.
func TestSendDirectMessageReportsARefusedDM(t *testing.T) {
	fake := newFakeDiscord(t)
	fake.dmStatus = http.StatusForbidden
	err := newClient(fake).SendDirectMessage(context.Background(), application.DiscordDirectMessageInput{DiscordUserID: "100200300", Content: "hi"})
	if err == nil || !strings.Contains(err.Error(), "does not accept direct messages") {
		t.Fatalf("refused DM = %v", err)
	}
	if strings.Contains(err.Error(), testBotToken) {
		t.Fatalf("bot token leaked into the error: %v", err)
	}
}

func TestLeaveGuildTreatsAnAbsentGuildAsDone(t *testing.T) {
	fake := newFakeDiscord(t)
	client := newClient(fake)
	if err := client.LeaveGuild(context.Background(), "G0ACME"); err != nil {
		t.Fatalf("LeaveGuild: %v", err)
	}
	if err := client.LeaveGuild(context.Background(), "G0GONE"); err != nil {
		t.Fatalf("leaving an absent guild = %v, want success", err)
	}
	if len(fake.left) != 2 {
		t.Fatalf("leave calls = %v", fake.left)
	}
}
