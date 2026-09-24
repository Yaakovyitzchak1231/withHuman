package slack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/withHumanAI/withHuman/application"
)

func TestTokenCheckDistinguishesRevocationFromUncertainty(t *testing.T) {
	for _, code := range []string{"", "token_revoked", "account_inactive", "invalid_auth", "ratelimited", "service_unavailable", "accesslimited"} {
		t.Run(code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/auth.test" || r.Header.Get("Authorization") != "Bearer secret" {
					t.Error("incorrect token probe")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": code == "", "error": code, "team_id": "T1", "user_id": "U1"})
			}))
			defer server.Close()
			client := NewClient(server.URL, "id", "secret")
			identity, err := client.CheckToken(context.Background(), application.SlackTokenInput{BotToken: "secret"})
			revoked := code == "token_revoked" || code == "account_inactive"
			if errors.Is(err, application.ErrSlackRevoked) != revoked {
				t.Fatalf("error=%v", err)
			}
			if code == "" && (err != nil || identity.TeamID != "T1" || identity.BotUserID != "U1") {
				t.Fatalf("identity=%+v,%v", identity, err)
			}
			if code != "" && err == nil {
				t.Fatal("failure accepted")
			}
		})
	}
}
