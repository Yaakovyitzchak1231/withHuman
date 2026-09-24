package singlebinary

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/embeddedpg/embeddedpgtest"
	"github.com/withHumanAI/withHuman/internal/postgres"
	"github.com/withHumanAI/withHuman/internal/secrets"
)

// testStore opens the instance's application database the way the
// server does, sealer included, so a test can seed rows the HTTP surface
// has no route for.
func testStore(t *testing.T, instance *Instance) *postgres.Store {
	t.Helper()
	store, err := postgres.Open(context.Background(), instance.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	sealer, err := secrets.NewSealer("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	store.WithSealer(sealer)
	return store
}

// The real HTTP server, database and worker expose a disconnected connection
// and preserve a member's rule through a fresh installation and member match.
func TestSlackLifecycleEndToEnd(t *testing.T) {
	_, store, api, connection, method := slackLifecycleFixture(t)
	// An accepted signed event is processed promptly by the actual Temporal
	// worker. An unknown team avoids any call to Slack from this fixture.
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	message := map[string]any{"type": "event_callback", "api_app_id": "A_FIXTURE", "event_id": "Ev_fixture", "team_id": "T_UNKNOWN", "event_time": time.Now().Unix(), "event": map[string]string{"type": "app_uninstalled"}}
	body, _ := json.Marshal(message)
	mac := hmac.New(sha256.New, []byte("fixture-signing-secret"))
	mac.Write([]byte("v0:" + stamp + ":"))
	mac.Write(body)
	api.request("POST", "/auth/slack/events", message, http.StatusOK, nil, "X-Slack-Request-Timestamp", stamp, "X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)), "Origin", "https://slack.com")
	deadline := time.Now().Add(10 * time.Second)
	for {
		var applied bool
		if err := store.Pool().QueryRow(context.Background(), `SELECT applied_at IS NOT NULL FROM slack_event_inbox WHERE external_event_id='Ev_fixture'`).Scan(&applied); err != nil {
			t.Fatal(err)
		}
		if applied {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("accepted event did not reach the worker promptly")
		}
		time.Sleep(20 * time.Millisecond)
	}
	var result struct {
		Connection domain.SlackConnection `json:"connection"`
	}
	api.request("GET", "/api/v1/enterprise/slack", nil, http.StatusOK, &result)
	if result.Connection.Status != "disconnected" || result.Connection.DisconnectReason != "app_uninstalled" {
		t.Fatalf("connection=%+v", result)
	}
	checkMethod := func(available bool) {
		var response struct {
			Methods []struct {
				ID        domain.ID `json:"id"`
				Available bool      `json:"available"`
				Verified  bool      `json:"verified"`
			} `json:"contact_methods"`
		}
		api.request("GET", "/api/v1/me/contact-methods", nil, http.StatusOK, &response)
		for _, item := range response.Methods {
			if item.ID == method.ID {
				if item.Available != available || !item.Verified {
					t.Fatalf("method=%+v", item)
				}
				return
			}
		}
		t.Fatal("saved Slack method disappeared")
	}
	checkMethod(false)
	api.request("PUT", "/api/v1/me/notification-rules/interrupt", map[string]any{"rules": []map[string]any{{"delay_seconds": 60, "contact_method_id": method.ID}}}, http.StatusOK, nil)
	var install struct {
		URL string `json:"authorize_url"`
	}
	api.postJSON("/api/v1/enterprise/slack/install", nil, http.StatusCreated, &install)
	if install.URL == "" {
		t.Fatal("reconnect did not create an OAuth URL")
	}
	sealer, _ := secrets.NewSealer("0123456789abcdef0123456789abcdef")
	sealed, _ := sealer.Seal("slack_bot_token", "fixture-new-token")
	next, err := store.UpsertSlackConnection(context.Background(), application.UpsertSlackConnectionInput{
		OrganizationID: connection.OrganizationID, InstalledByMembershipID: connection.InstalledByMembershipID,
		Installation: application.SlackInstallation{TeamID: "T_NEW", TeamName: "New Workspace", BotUserID: "BOT", Scopes: []string{}}, SealedBotToken: sealed, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	checkMethod(false)
	linked, err := store.LinkOrganizationContactMethod(context.Background(), application.LinkOrganizationContactMethodInput{
		MethodID: domain.ID(uuid.NewString()), UserID: method.UserID, OrganizationID: connection.OrganizationID,
		Kind: domain.ContactSlack, ExternalID: "U_NEW", Label: "Owner", SlackInstallationID: next.InstallationID, Now: time.Now().UTC(),
	})
	if err != nil || linked.ID != method.ID {
		t.Fatalf("relink=%+v,%v", linked, err)
	}
	checkMethod(true)
	var rules struct {
		Interrupt []struct {
			MethodID domain.ID `json:"contact_method_id"`
		} `json:"interrupt"`
	}
	api.request("GET", "/api/v1/me/notification-rules", nil, http.StatusOK, &rules)
	if len(rules.Interrupt) != 1 || rules.Interrupt[0].MethodID != method.ID {
		t.Fatalf("rules=%+v", rules)
	}
}

func slackLifecycleFixture(t *testing.T) (*Instance, *postgres.Store, *apiClient, domain.SlackConnection, domain.ContactMethod) {
	t.Helper()
	t.Setenv("WITHHUMAN_SLACK_APP_ID", "A_FIXTURE")
	t.Setenv("WITHHUMAN_SLACK_CLIENT_ID", "fixture-client")
	t.Setenv("WITHHUMAN_SLACK_CLIENT_SECRET", "fixture-secret")
	t.Setenv("WITHHUMAN_SLACK_SIGNING_SECRET", "fixture-signing-secret")
	t.Setenv("WITHHUMAN_SECRETS_KEY", "0123456789abcdef0123456789abcdef")
	cacheDir, binariesDir := embeddedpgtest.SharedDirs(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	unlock := embeddedpgtest.LockStartup(t)
	instance, err := Start(ctx, Config{Server: openEdition(), DataDir: t.TempDir(), HTTPAddress: address, PublicOrigin: "http://" + address, BootstrapSecret: "slack-e2e-secret", Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), PostgresCacheDir: cacheDir, PostgresBinariesDir: binariesDir})
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(shutdownHelper(t, instance))
	store := testStore(t, instance)
	jar, _ := cookiejar.New(nil)
	api := &apiClient{t: t, client: &http.Client{Jar: jar, Timeout: 30 * time.Second}, base: instance.BaseURL}
	api.waitReady(ctx)
	api.postJSON("/auth/bootstrap", map[string]any{"secret": "slack-e2e-secret", "organization_slug": "slack-preview", "organization_name": "Slack Preview", "display_name": "Preview Owner", "email": "slack-preview@example.com", "password": "slack-preview-password"}, http.StatusCreated, nil)
	var org, member, user domain.ID
	if err := store.Pool().QueryRow(ctx, `SELECT organization_id::text,id::text,user_id::text FROM memberships LIMIT 1`).Scan(&org, &member, &user); err != nil {
		t.Fatal(err)
	}
	sealer, _ := secrets.NewSealer("0123456789abcdef0123456789abcdef")
	sealed, _ := sealer.Seal("slack_bot_token", "fixture-token")
	now := time.Now().UTC()
	connection, err := store.UpsertSlackConnection(ctx, application.UpsertSlackConnectionInput{OrganizationID: org, InstalledByMembershipID: member, Installation: application.SlackInstallation{TeamID: "T_PREVIEW", TeamName: "Example Workspace", BotUserID: "BOT", Scopes: []string{}}, SealedBotToken: sealed, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	method, err := store.LinkOrganizationContactMethod(ctx, application.LinkOrganizationContactMethodInput{MethodID: domain.ID(uuid.NewString()), UserID: user, OrganizationID: org, Kind: domain.ContactSlack, ExternalID: "U_PREVIEW", Label: "Preview Owner", SlackInstallationID: connection.InstallationID, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ReplaceNotificationRules(ctx, application.ReplaceNotificationRulesInput{UserID: user, Urgency: domain.NotificationInterrupt, Rungs: []application.NotificationRung{{ContactMethodID: method.ID, Delay: time.Minute}}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.DisconnectSlackConnection(ctx, application.DisconnectSlackConnectionInput{SlackConnectionVersion: application.SlackConnectionVersion{OrganizationID: org, InstallationID: connection.InstallationID}, Reason: "app_uninstalled", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return instance, store, api, connection, method
}
