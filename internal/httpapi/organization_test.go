package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/orgsettings"
)

type organizationHTTPIdentities struct {
	application.IdentityRepository
	policy  domain.OrganizationPolicy
	updated *application.UpdateOrganizationPolicyInput
}

func (s *organizationHTTPIdentities) GetOrganizationPolicy(_ context.Context, input application.GetOrganizationPolicyInput) (domain.OrganizationPolicy, error) {
	policy := s.policy
	policy.OrganizationID = input.OrganizationID
	return policy, nil
}

func (s *organizationHTTPIdentities) UpdateOrganizationPolicy(_ context.Context, input application.UpdateOrganizationPolicyInput) (domain.OrganizationPolicy, error) {
	s.updated = &input
	return domain.OrganizationPolicy{
		OrganizationID: input.OrganizationID, MFAMode: input.MFAMode,
		ApprovalReauthWindow: input.ApprovalReauthWindow, ClaimRetriggerWindow: input.ClaimRetriggerWindow,
		UnroutedRequests: input.UnroutedRequests, RequestVisibility: input.RequestVisibility,
	}, nil
}

type organizationHTTPClock struct{}

func (organizationHTTPClock) Now() time.Time { return time.Unix(90_000, 0).UTC() }

func organizationTestRouter(identities *organizationHTTPIdentities, principal domain.Principal) *gin.Engine {
	api := &API{
		orgSettings: orgsettings.NewService(identities, organizationHTTPClock{}, nil),
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	router := gin.New()
	router.Use(api.observability())
	group := router.Group("/api/v1", func(c *gin.Context) {
		setPrincipal(c, principal)
		c.Next()
	})
	api.installOrganizationRoutes(group)
	return router
}

func organizationHTTPIdentitiesWithPolicy() *organizationHTTPIdentities {
	return &organizationHTTPIdentities{policy: domain.OrganizationPolicy{
		MFAMode: domain.MFAOptional, ApprovalReauthWindow: 24 * time.Hour, ClaimRetriggerWindow: 15 * time.Minute,
		UnroutedRequests: domain.UnroutedAnyReviewer, RequestVisibility: domain.VisibilityAllReaders,
	}}
}

func organizationHTTPPrincipal(permissions ...domain.Permission) domain.Principal {
	return domain.Principal{
		ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "organization-1", MembershipID: "membership-1",
	}.WithGrants(domain.OrganizationGrants(permissions))
}

type organizationPolicyEnvelope struct {
	Policy map[string]any `json:"policy"`
}

func TestOrganizationPolicyReadShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := organizationTestRouter(organizationHTTPIdentitiesWithPolicy(), organizationHTTPPrincipal(domain.PermissionOrganizationRead))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/organization/policy", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("read = %d %s", response.Code, response.Body.String())
	}
	var body organizationPolicyEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"mfa_mode": "optional", "approval_reauth_window": "24h0m0s",
		"claim_retrigger_window": "15m0s", "unrouted_requests": "any_reviewer", "request_visibility": "all_readers",
		"organization_api_keys_allowed": false,
		"personal_api_keys_allowed":     false,
	}
	if len(body.Policy) != len(want) {
		t.Fatalf("policy = %v, want exactly %v", body.Policy, want)
	}
	for key, value := range want {
		if body.Policy[key] != value {
			t.Fatalf("policy[%s] = %v, want %v", key, body.Policy[key], value)
		}
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/api/v1/organization/policy", strings.NewReader(`{"mfa_mode":"reviewers_required"}`)))
	if response.Code != http.StatusForbidden {
		t.Fatalf("patch with organization.read only = %d, want 403", response.Code)
	}
}

func TestOrganizationPolicyPatchParsesDurationsAndRejectsBadValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	identities := organizationHTTPIdentitiesWithPolicy()
	router := organizationTestRouter(identities, organizationHTTPPrincipal(domain.PermissionOrganizationSettingsWrite))

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/organization/policy",
		strings.NewReader(`{"unrouted_requests":"break_glass_only","claim_retrigger_window":"30m","approval_reauth_window":"12h"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("patch = %d %s", response.Code, response.Body.String())
	}
	if identities.updated == nil || identities.updated.ClaimRetriggerWindow != 30*time.Minute || identities.updated.ApprovalReauthWindow != 12*time.Hour ||
		identities.updated.UnroutedRequests != domain.UnroutedBreakGlassOnly || identities.updated.RequestVisibility != domain.VisibilityAllReaders {
		t.Fatalf("update input = %#v", identities.updated)
	}
	var body organizationPolicyEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Policy["claim_retrigger_window"] != "30m0s" || body.Policy["unrouted_requests"] != "break_glass_only" {
		t.Fatalf("patched policy = %v", body.Policy)
	}

	for name, payload := range map[string]string{
		"unitless duration": `{"claim_retrigger_window":900}`,
		"garbage duration":  `{"approval_reauth_window":"soon"}`,
		"unknown field":     `{"session_max_age":"1h"}`,
		"bad enum":          `{"request_visibility":"nobody"}`,
	} {
		t.Run(name, func(t *testing.T) {
			identities.updated = nil
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPatch, "/api/v1/organization/policy", strings.NewReader(payload))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("= %d %s, want 400", response.Code, response.Body.String())
			}
			if identities.updated != nil {
				t.Fatal("an invalid patch reached the repository")
			}
		})
	}
}

func TestOrganizationPolicyRoutesAreProductAPIs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := NewRouter(Dependencies{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(method, "/api/v1/organization/policy", nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s without a session = %d, want 401", method, response.Code)
		}
	}
}
