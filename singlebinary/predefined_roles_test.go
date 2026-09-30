package singlebinary

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// The open edition offers predefined roles alone: a person is invited
// with one and later given another, and every custom permission policy
// write is refused.
func testPredefinedRolesAPI(t *testing.T, api *apiClient) {
	t.Helper()
	var config struct {
		CustomPermissions bool `json:"custom_permissions_enabled"`
	}
	api.request(http.MethodGet, "/api/runtime-config", nil, http.StatusOK, &config)
	if config.CustomPermissions {
		t.Fatal("the open edition reports custom permissions")
	}
	var roles struct {
		Roles []struct {
			Key string `json:"key"`
		} `json:"roles"`
	}
	api.request(http.MethodGet, "/api/v1/roles", nil, http.StatusOK, &roles)
	var keys []string
	for _, role := range roles.Roles {
		keys = append(keys, role.Key)
	}
	if !slices.Equal(keys, []string{"owner", "admin", "reviewer", "auditor"}) {
		t.Fatalf("roles = %v", keys)
	}

	policy := map[string]any{"scope_kind": "organization", "resource_ids": []string{}, "permissions": []string{"request.read"}}
	api.postJSON("/api/v1/invitations", map[string]any{"email": "custom@example.com", "permission_policies": []any{policy}}, http.StatusForbidden, nil)
	api.postJSON("/api/v1/invitations", map[string]any{"email": "neither@example.com"}, http.StatusBadRequest, nil)
	api.postJSON("/api/v1/invitations", map[string]any{"email": "agent@example.com", "role": "agent"}, http.StatusBadRequest, nil)

	var invited struct {
		Invitation struct {
			Grants []struct {
				Role     string `json:"role"`
				RoleName string `json:"role_name"`
			} `json:"grants"`
		} `json:"invitation"`
		InviteURL string `json:"invite_url"`
	}
	api.postJSON("/api/v1/invitations", map[string]any{"email": "auditor@example.com", "role": "auditor"}, http.StatusCreated, &invited)
	if grants := invited.Invitation.Grants; len(grants) != 1 || grants[0].Role != "auditor" || grants[0].RoleName != "Auditor" {
		t.Fatalf("invitation grants = %+v", grants)
	}
	if invited.InviteURL == "" {
		t.Fatal("no invite link without email delivery")
	}
	invitee := &apiClient{t: t, client: &http.Client{Timeout: 30 * time.Second}, base: api.base}
	var accepted struct {
		Principal struct {
			MembershipID string `json:"membership_id"`
		} `json:"principal"`
	}
	invitee.postJSON("/auth/invitations/accept", map[string]any{
		"token":        invited.InviteURL[strings.LastIndex(invited.InviteURL, "/")+1:],
		"display_name": "Ada Auditor",
		"password":     "correct-horse-battery-staple",
	}, http.StatusOK, &accepted)
	member := "/api/v1/members/" + accepted.Principal.MembershipID
	if roleOf(t, api, accepted.Principal.MembershipID) != "auditor" {
		t.Fatal("the invitee does not hold the auditor role")
	}

	api.request(http.MethodPut, member+"/role", map[string]any{"role": "reviewer"}, http.StatusOK, nil)
	if roleOf(t, api, accepted.Principal.MembershipID) != "reviewer" {
		t.Fatal("the member does not hold the reviewer role")
	}
	api.request(http.MethodPut, member+"/role", map[string]any{"role": "agent"}, http.StatusBadRequest, nil)
	api.request(http.MethodPost, member+"/permission_policies", policy, http.StatusForbidden, nil, "Idempotency-Key", "open-edition-policy")
}

// roleOf is the one predefined role a member holds directly.
func roleOf(t *testing.T, api *apiClient, membershipID string) string {
	t.Helper()
	var listing struct {
		Members []struct {
			ID          string `json:"id"`
			Assignments []struct {
				BuiltInRole string `json:"built_in_role"`
			} `json:"assignments"`
		} `json:"members"`
	}
	api.request(http.MethodGet, "/api/v1/members", nil, http.StatusOK, &listing)
	for _, member := range listing.Members {
		if member.ID == membershipID {
			if len(member.Assignments) != 1 {
				t.Fatalf("member %s assignments = %+v", membershipID, member.Assignments)
			}
			return member.Assignments[0].BuiltInRole
		}
	}
	t.Fatalf("member %s is not listed", membershipID)
	return ""
}
