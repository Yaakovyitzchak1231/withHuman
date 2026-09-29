package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/internal/orgsettings"
)

func (a *API) installOrganizationRoutes(router *gin.RouterGroup) {
	a.handle(router, http.MethodGet, "/organization/policy", string(domain.PermissionOrganizationRead), a.getOrganizationPolicy)
	a.handle(router, http.MethodPatch, "/organization/policy", string(domain.PermissionOrganizationSettingsWrite), a.updateOrganizationPolicy)
}

// organizationPolicyResponse is the settable part of the organization
// policy. Durations are Go duration strings, never unitless numbers.
type organizationPolicyResponse struct {
	MFAMode                    domain.MFAMode                 `json:"mfa_mode"`
	ApprovalReauthWindow       string                         `json:"approval_reauth_window"`
	ClaimRetriggerWindow       string                         `json:"claim_retrigger_window"`
	UnroutedRequests           domain.UnroutedRequestsPolicy  `json:"unrouted_requests"`
	RequestVisibility          domain.RequestVisibilityPolicy `json:"request_visibility"`
	PersonalAPIKeysAllowed     bool                           `json:"personal_api_keys_allowed"`
	OrganizationAPIKeysAllowed bool                           `json:"organization_api_keys_allowed"`
}

func newOrganizationPolicyResponse(policy domain.OrganizationPolicy) organizationPolicyResponse {
	return organizationPolicyResponse{
		MFAMode:              policy.MFAMode,
		ApprovalReauthWindow: policy.ApprovalReauthWindow.String(),
		ClaimRetriggerWindow: policy.ClaimRetriggerWindow.String(),
		UnroutedRequests:     policy.UnroutedRequests, RequestVisibility: policy.RequestVisibility,
		PersonalAPIKeysAllowed:     policy.PersonalAPIKeysAllowed,
		OrganizationAPIKeysAllowed: policy.OrganizationAPIKeysAllowed,
	}
}

// organizationPolicyPatchRequest is any subset of the settable fields.
// Durations arrive as strings and are parsed here, at the boundary.
type organizationPolicyPatchRequest struct {
	MFAMode                    *domain.MFAMode                 `json:"mfa_mode"`
	ApprovalReauthWindow       *string                         `json:"approval_reauth_window"`
	ClaimRetriggerWindow       *string                         `json:"claim_retrigger_window"`
	UnroutedRequests           *domain.UnroutedRequestsPolicy  `json:"unrouted_requests"`
	RequestVisibility          *domain.RequestVisibilityPolicy `json:"request_visibility"`
	PersonalAPIKeysAllowed     *bool                           `json:"personal_api_keys_allowed"`
	OrganizationAPIKeysAllowed *bool                           `json:"organization_api_keys_allowed"`
}

func (request organizationPolicyPatchRequest) patch() (orgsettings.PolicyPatch, error) {
	patch := orgsettings.PolicyPatch{
		MFAMode:          request.MFAMode,
		UnroutedRequests: request.UnroutedRequests, RequestVisibility: request.RequestVisibility,
		PersonalAPIKeysAllowed:     request.PersonalAPIKeysAllowed,
		OrganizationAPIKeysAllowed: request.OrganizationAPIKeysAllowed,
	}
	var err error
	if patch.ApprovalReauthWindow, err = parsePolicyDuration("approval_reauth_window", request.ApprovalReauthWindow); err != nil {
		return orgsettings.PolicyPatch{}, err
	}
	if patch.ClaimRetriggerWindow, err = parsePolicyDuration("claim_retrigger_window", request.ClaimRetriggerWindow); err != nil {
		return orgsettings.PolicyPatch{}, err
	}
	return patch, nil
}

func parsePolicyDuration(field string, value *string) (*time.Duration, error) {
	if value == nil {
		return nil, nil
	}
	duration, err := time.ParseDuration(*value)
	if err != nil {
		return nil, fmt.Errorf("%s must be a duration such as 15m or 24h: %w", field, domain.ErrInvalid)
	}
	return &duration, nil
}

func (a *API) getOrganizationPolicy(c *gin.Context) {
	policy, err := a.orgSettings.Policy(c.Request.Context(), currentPrincipal(c))
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"policy": newOrganizationPolicyResponse(policy)})
}

func (a *API) updateOrganizationPolicy(c *gin.Context) {
	var request organizationPolicyPatchRequest
	if !bindStrict(c, &request) {
		return
	}
	patch, err := request.patch()
	if err != nil {
		a.writeError(c, err)
		return
	}
	policy, err := a.orgSettings.UpdatePolicy(c.Request.Context(), currentPrincipal(c), patch)
	if err != nil {
		a.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"policy": newOrganizationPolicyResponse(policy)})
}
