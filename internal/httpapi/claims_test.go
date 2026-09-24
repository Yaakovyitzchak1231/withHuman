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
	"github.com/withHumanAI/withHuman/domain"
)

func TestClaimRoutesAreProductAPIs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := NewRouter(Dependencies{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		t.Run(method+" product claim route exists", func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(method, "/api/v1/requests/11111111-1111-4111-8111-111111111111/claim", nil))
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("got status %d, want %d", response.Code, http.StatusUnauthorized)
			}
		})
		t.Run(method+" claim stays out of AAP", func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(method, "/api/aap/v1/requests/11111111-1111-4111-8111-111111111111/claim", nil))
			if response.Code != http.StatusNotFound {
				t.Fatalf("claim leaked into AAP: status %d", response.Code)
			}
		})
	}
}

func TestClaimNeverCrossesTheAAPRepresentation(t *testing.T) {
	now := time.Now().UTC()
	request := domain.ApprovalRequest{
		ID: "request-1", OrganizationID: "org-1", Status: domain.ApprovalPending,
		DeadlineAt: now.Add(time.Hour), CreatedAt: now,
		Claim: &domain.Claim{
			ID: "claim-1", RequestID: "request-1", OrganizationID: "org-1",
			MembershipID: "membership-1", ClaimantDisplayName: "Ana Suarez",
			ClaimedAt: now, ExpiresAt: now.Add(15 * time.Minute),
		},
	}

	aap, err := json.Marshal(newAAPApprovalResponse(request))
	if err != nil {
		t.Fatal(err)
	}
	var aapFields map[string]json.RawMessage
	if err := json.Unmarshal(aap, &aapFields); err != nil {
		t.Fatal(err)
	}
	if _, leaked := aapFields["claim"]; leaked {
		t.Fatalf("claim crossed the AAP boundary: %s", aap)
	}

	product, err := json.Marshal(newQueueApprovalResponse(request, now))
	if err != nil {
		t.Fatal(err)
	}
	var productFields map[string]json.RawMessage
	if err := json.Unmarshal(product, &productFields); err != nil {
		t.Fatal(err)
	}
	if _, present := productFields["claim"]; !present {
		t.Fatalf("product representation dropped the live claim: %s", product)
	}
}

func TestQueueRepresentationHidesExpiredClaims(t *testing.T) {
	now := time.Now().UTC()
	request := domain.ApprovalRequest{
		ID: "request-1", OrganizationID: "org-1", Status: domain.ApprovalPending,
		DeadlineAt: now.Add(time.Hour), CreatedAt: now,
		Claim: &domain.Claim{
			ID: "claim-1", RequestID: "request-1", OrganizationID: "org-1",
			MembershipID: "membership-1", ClaimedAt: now.Add(-time.Hour),
			ExpiresAt: now.Add(-time.Minute),
		},
	}
	response := newQueueApprovalResponse(request, now)
	if response.Claim != nil {
		t.Fatalf("expired claim survived serialization: %#v", response.Claim)
	}
}
