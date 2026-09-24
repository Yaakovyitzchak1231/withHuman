package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/approvals"
)

func TestReviewRouteIsAProductAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := NewRouter(Dependencies{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/requests/11111111-1111-4111-8111-111111111111/review", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want %d", response.Code, http.StatusUnauthorized)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/aap/v1/requests/11111111-1111-4111-8111-111111111111/review", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("review leaked into AAP: status %d", response.Code)
	}
}

func TestReviewRepresentationAlwaysCarriesItsLists(t *testing.T) {
	now := time.Now().UTC()
	encoded, err := json.Marshal(newApprovalReviewResponse(approvals.Review{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["added_reviewers"]) != "[]" {
		t.Fatalf("empty lists must serialize as arrays, not null: %s", encoded)
	}
	if _, present := fields["routing"]; present {
		t.Fatalf("routing must be omitted when unknown: %s", encoded)
	}

	full := newApprovalReviewResponse(approvals.Review{
		AddedReviewers: []domain.ApprovalReviewer{{MembershipID: "membership-2", DisplayName: "Ben", AddedByMembershipID: "membership-1", AddedByDisplayName: "Ana", CreatedAt: now}},
		Viewer:         approvals.Viewer{Decidable: approvals.DecidableBreakGlass, Reason: approvals.ViewerReasonNotTargeted},
	}, &application.ApprovalRoutingView{
		ApprovalRequestRouting: domain.ApprovalRequestRouting{Outcome: domain.RoutingEscalated, Reason: domain.RoutingReasonBlockEscalation, Urgency: "interrupt", EscalationPathKey: "platform", CreatedAt: now},
		EscalationPathName:     "Platform on-call",
		Targets:                []application.ApprovalRoutingTarget{{Type: "user", ID: "membership-3", Name: "Cy"}, {Type: "team", ID: "team-1", Name: "Platform"}},
	})
	encoded, err = json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Routing struct {
			EscalationPathName string           `json:"escalation_path_name"`
			Urgency            string           `json:"urgency"`
			AddedReviewers     []map[string]any `json:"added_reviewers"`
			Targets            []struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"targets"`
		} `json:"routing"`
		AddedReviewers []struct {
			MembershipID       string `json:"membership_id"`
			DisplayName        string `json:"display_name"`
			AddedByDisplayName string `json:"added_by_display_name"`
		} `json:"added_reviewers"`
		Viewer struct {
			Targeted  bool   `json:"targeted"`
			Decidable string `json:"decidable"`
			Reason    string `json:"reason"`
		} `json:"viewer"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Routing.EscalationPathName != "Platform on-call" || decoded.Routing.Urgency != "interrupt" {
		t.Fatalf("unexpected review representation: %s", encoded)
	}
	if len(decoded.AddedReviewers) != 1 || decoded.AddedReviewers[0].MembershipID != "membership-2" || decoded.AddedReviewers[0].DisplayName != "Ben" ||
		decoded.AddedReviewers[0].AddedByDisplayName != "Ana" || len(decoded.Routing.AddedReviewers) != 1 {
		t.Fatalf("added reviewers missing from the review or its routing: %s", encoded)
	}
	if decoded.Viewer.Targeted || decoded.Viewer.Decidable != "break_glass" || decoded.Viewer.Reason != "not_targeted" {
		t.Fatalf("unexpected viewer: %s", encoded)
	}
	if len(decoded.Routing.Targets) != 2 || decoded.Routing.Targets[0].Type != "user" || decoded.Routing.Targets[0].Name != "Cy" ||
		decoded.Routing.Targets[1].Type != "team" || decoded.Routing.Targets[1].ID != "team-1" {
		t.Fatalf("routing targets missing from the review: %s", encoded)
	}

	// A routing view with no reachable targets still encodes an array.
	encoded, err = json.Marshal(newApprovalRoutingResponse(application.ApprovalRoutingView{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["targets"]) != "[]" {
		t.Fatalf("empty targets must serialize as an array, not null: %s", encoded)
	}

	// A viewer who may decide outright carries no reason.
	encoded, err = json.Marshal(newApprovalReviewResponse(approvals.Review{Viewer: approvals.Viewer{Targeted: true, Decidable: approvals.DecidableYes}}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["viewer"]) != `{"targeted":true,"decidable":"yes"}` {
		t.Fatalf("viewer = %s", fields["viewer"])
	}
}

func TestReviewerRouteIsAProductAPIBehindDecide(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := NewRouter(Dependencies{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/requests/11111111-1111-4111-8111-111111111111/reviewers", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want %d", response.Code, http.StatusUnauthorized)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/aap/v1/requests/11111111-1111-4111-8111-111111111111/reviewers", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("reviewer widening leaked into AAP: status %d", response.Code)
	}
}
