package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/approvals"
	"github.com/withHumanAI/withHuman/internal/discordconnect"
)

func TestDiscordLinkErrorsDistinguishSetupFromPermissions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{discordconnect.ErrNotConnected, http.StatusNotFound, "discord_not_connected"},
		{discordconnect.ErrNotConfigured, http.StatusNotImplemented, "discord_not_configured"},
		{domain.ErrForbidden, http.StatusForbidden, "forbidden"},
		{domain.ErrNotFound, http.StatusNotFound, "not_found"},
	} {
		t.Run(test.code, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/me/discord/link", nil)
			WriteError(c, slog.New(slog.NewTextHandler(io.Discard, nil)), test.err)
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != test.status || body.Error.Code != test.code {
				t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

// A decision the routing rule refused is a plain forbidden with the
// kernel's code in details.reason, so the page can say which rule.
func TestWriteErrorCarriesTheRoutingRejectionReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, code := range []string{"not_targeted", "break_glass_only", "outside_routing_required"} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/requests/r/decision", nil)
		WriteError(c, slog.New(slog.NewTextHandler(io.Discard, nil)), approvals.RoutingRejection{Code: code})

		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 403", code, recorder.Code)
		}
		var body struct {
			Error struct {
				Type    string            `json:"type"`
				Code    string            `json:"code"`
				Details map[string]string `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Error.Type != "authorization" || body.Error.Code != "forbidden" || body.Error.Details["reason"] != code {
			t.Fatalf("%s: envelope = %+v", code, body.Error)
		}
	}
}

func TestWriteErrorCarriesQuotaDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/invitations", nil)
	WriteError(c, slog.New(slog.NewTextHandler(io.Discard, nil)), domain.QuotaExceeded{Dimension: "seats", Limit: 10, Used: 10})

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", recorder.Code)
	}
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Details struct {
				Dimension string `json:"dimension"`
				Limit     int64  `json:"limit"`
				Used      int64  `json:"used"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Type != "conflict" || body.Error.Code != "quota_exceeded" {
		t.Fatalf("envelope = %+v, want conflict/quota_exceeded", body.Error)
	}
	if body.Error.Details.Dimension != "seats" || body.Error.Details.Limit != 10 || body.Error.Details.Used != 10 {
		t.Fatalf("details = %+v", body.Error.Details)
	}
}

func TestWriteErrorOmitsDetailsByDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	WriteError(c, slog.New(slog.NewTextHandler(io.Discard, nil)), domain.ErrNotFound)

	var body map[string]map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["error"]["details"]; present {
		t.Fatalf("details should be absent: %v", body)
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

func TestWriteErrorCarriesTargetingEscalationPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodDelete, "/api/v1/members/m-1", nil)
	WriteError(c, slog.New(slog.NewTextHandler(io.Discard, nil)), domain.MembershipInEscalationPathError{
		MembershipID: "m-1",
		Paths: []domain.EscalationPathReference{
			{PathKey: "payments", Name: "Payments", Revision: 3},
			{PathKey: "refunds", Name: "Refunds", Revision: 1},
		},
	})

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", recorder.Code)
	}
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Details struct {
				MembershipID    string `json:"membership_id"`
				EscalationPaths []struct {
					PathKey  string `json:"path_key"`
					Name     string `json:"name"`
					Revision int64  `json:"revision"`
				} `json:"escalation_paths"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Type != "conflict" || body.Error.Code != "membership_in_escalation_path" {
		t.Fatalf("envelope = %+v, want conflict/membership_in_escalation_path", body.Error)
	}
	paths := body.Error.Details.EscalationPaths
	if body.Error.Details.MembershipID != "m-1" || len(paths) != 2 || paths[0].PathKey != "payments" || paths[0].Name != "Payments" || paths[0].Revision != 3 || paths[1].PathKey != "refunds" {
		t.Fatalf("details = %+v", body.Error.Details)
	}
}
