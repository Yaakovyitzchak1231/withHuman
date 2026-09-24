package slack_test

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
	"github.com/withHumanAI/withHuman/internal/slack"
)

const (
	testClientID     = "123.456"
	testClientSecret = "app-secret"
)

// fakeSlack answers the two OAuth calls the install uses, with the
// response shapes documented for oauth.v2.access and auth.revoke.
type fakeSlack struct {
	server   *httptest.Server
	exchange func(code, redirect string) map[string]any
	revoked  []string
	opened   []string
	posted   []map[string]any
}

func newFakeSlack(t *testing.T) *fakeSlack {
	t.Helper()
	fake := &fakeSlack{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/oauth.v2.access", func(w http.ResponseWriter, r *http.Request) {
		user, secret, ok := r.BasicAuth()
		if !ok || user != testClientID || secret != testClientSecret {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_client_id"})
			return
		}
		_ = r.ParseForm()
		_ = json.NewEncoder(w).Encode(fake.exchange(r.Form.Get("code"), r.Form.Get("redirect_uri")))
	})
	mux.HandleFunc("/api/auth.revoke", func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		fake.revoked = append(fake.revoked, token)
		if token == "xoxb-already-dead" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_auth"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "revoked": true})
	})
	mux.HandleFunc("/api/users.lookupByEmail", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xoxb-bot-token" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_auth"})
			return
		}
		_ = r.ParseForm()
		if r.Form.Get("email") != "ana@example.com" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "users_not_found"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "user": map[string]any{
			"id": "U0ANA", "name": "ana", "profile": map[string]any{"display_name": "Ana S", "real_name": "Ana Suarez"},
		}})
	})
	mux.HandleFunc("/api/conversations.open", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		fake.opened = append(fake.opened, r.Form.Get("users"))
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": map[string]any{"id": "D0" + r.Form.Get("users")}})
	})
	mux.HandleFunc("/api/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		fake.posted = append(fake.posted, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "1.2"})
	})
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	fake.exchange = func(code, redirect string) map[string]any {
		if code != "good-code" || redirect != "https://approvals.example.com/auth/slack/callback" {
			return map[string]any{"ok": false, "error": "invalid_code"}
		}
		return map[string]any{
			"ok": true, "access_token": "xoxb-bot-token", "token_type": "bot",
			"scope":       "chat:write,users:read,users:read.email,im:write",
			"bot_user_id": "U0BOT", "team": map[string]any{"id": "T0ACME", "name": "Acme"},
		}
	}
	return fake
}

func TestAuthorizeURLCarriesStateAndScopes(t *testing.T) {
	client := slack.NewClient("https://slack.example", testClientID, testClientSecret)
	target := client.AuthorizeURL("state-1", "https://approvals.example.com/auth/slack/callback")
	for _, expected := range []string{
		"https://slack.example/oauth/v2/authorize?",
		"client_id=123.456",
		"state=state-1",
		"redirect_uri=https%3A%2F%2Fapprovals.example.com%2Fauth%2Fslack%2Fcallback",
		"scope=chat%3Awrite%2Cusers%3Aread%2Cusers%3Aread.email%2Cim%3Awrite",
	} {
		if !strings.Contains(target, expected) {
			t.Fatalf("authorize URL missing %q: %s", expected, target)
		}
	}
}

func TestExchangeReturnsTheBotGrant(t *testing.T) {
	fake := newFakeSlack(t)
	client := slack.NewClient(fake.server.URL, testClientID, testClientSecret)
	installation, err := client.Exchange(context.Background(), application.SlackExchangeInput{
		Code: "good-code", RedirectURL: "https://approvals.example.com/auth/slack/callback",
	})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if installation.TeamID != "T0ACME" || installation.TeamName != "Acme" || installation.BotUserID != "U0BOT" || installation.BotToken != "xoxb-bot-token" {
		t.Fatalf("installation = %+v", installation)
	}
	if len(installation.Scopes) != 4 || installation.Scopes[0] != "chat:write" {
		t.Fatalf("scopes = %v", installation.Scopes)
	}
}

// Slack signals failure with ok=false inside an HTTP 200, so the ok flag
// is the status that matters; a bad code must surface as invalid, not as
// an install with an empty token.
func TestExchangeRejectsABadCode(t *testing.T) {
	fake := newFakeSlack(t)
	client := slack.NewClient(fake.server.URL, testClientID, testClientSecret)
	_, err := client.Exchange(context.Background(), application.SlackExchangeInput{
		Code: "stale-code", RedirectURL: "https://approvals.example.com/auth/slack/callback",
	})
	if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "invalid_code") {
		t.Fatalf("bad code error = %v", err)
	}
}

func TestExchangeRefusesAUserGrant(t *testing.T) {
	fake := newFakeSlack(t)
	fake.exchange = func(string, string) map[string]any {
		return map[string]any{"ok": true, "access_token": "xoxp-user", "token_type": "user",
			"team": map[string]any{"id": "T0ACME", "name": "Acme"}}
	}
	client := slack.NewClient(fake.server.URL, testClientID, testClientSecret)
	if _, err := client.Exchange(context.Background(), application.SlackExchangeInput{
		Code: "good-code", RedirectURL: "https://approvals.example.com/auth/slack/callback",
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("user grant error = %v, want invalid", err)
	}
}

func TestRevokeTreatsADeadTokenAsDone(t *testing.T) {
	fake := newFakeSlack(t)
	client := slack.NewClient(fake.server.URL, testClientID, testClientSecret)
	if err := client.Revoke(context.Background(), "xoxb-bot-token"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := client.Revoke(context.Background(), "xoxb-already-dead"); err != nil {
		t.Fatalf("revoking a dead token = %v, want success", err)
	}
	if len(fake.revoked) != 2 {
		t.Fatalf("revoke calls = %v", fake.revoked)
	}
}

func TestLookupUserByEmailDistinguishesUnknownFromFailure(t *testing.T) {
	fake := newFakeSlack(t)
	client := slack.NewClient(fake.server.URL, testClientID, testClientSecret)
	user, err := client.LookupUserByEmail(context.Background(), "xoxb-bot-token", "ana@example.com")
	if err != nil || user.ID != "U0ANA" || user.Handle != "ana" || user.DisplayName != "Ana S" {
		t.Fatalf("lookup = %+v, %v", user, err)
	}
	if _, err := client.LookupUserByEmail(context.Background(), "xoxb-bot-token", "nobody@example.com"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown address = %v, want not found", err)
	}
	if _, err := client.LookupUserByEmail(context.Background(), "xoxb-wrong", "ana@example.com"); err == nil || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("bad token = %v, want a real failure", err)
	}
}

func TestPostDirectMessageOpensThenPosts(t *testing.T) {
	fake := newFakeSlack(t)
	client := slack.NewClient(fake.server.URL, testClientID, testClientSecret)
	if err := client.PostDirectMessage(context.Background(), application.SlackDirectMessageInput{
		BotToken: "xoxb-bot-token", SlackUserID: "U0ANA", Text: "stripe.refund needs your approval",
		Blocks: json.RawMessage(`[{"type":"section","text":{"type":"mrkdwn","text":"hi"}}]`),
	}); err != nil {
		t.Fatalf("PostDirectMessage: %v", err)
	}
	if len(fake.opened) != 1 || fake.opened[0] != "U0ANA" {
		t.Fatalf("opened = %v", fake.opened)
	}
	if len(fake.posted) != 1 || fake.posted[0]["channel"] != "D0U0ANA" || fake.posted[0]["blocks"] == nil {
		t.Fatalf("posted = %v", fake.posted)
	}
}
