package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// The membership and enterprise surfaces sit behind the human session
// gate; the invitation redemption and connector endpoints are public but
// authorize themselves (token, HMAC signature, or connector presence).
func TestMembershipRouteShapes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := NewRouter(Dependencies{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	for _, test := range []struct {
		method string
		path   string
		body   string
		status int
	}{
		{http.MethodGet, "/api/v1/members", "", http.StatusUnauthorized},
		{http.MethodPost, "/api/v1/me/password", `{"current_password":"a","new_password":"b"}`, http.StatusUnauthorized},
		{http.MethodPost, "/auth/password/reset", "", http.StatusBadRequest},
		{http.MethodPost, "/auth/password/reset/preview", "", http.StatusBadRequest},
		{http.MethodPost, "/auth/password/reset/complete", "", http.StatusBadRequest},
		{http.MethodPatch, "/api/v1/members/m-1", `{"status":"suspended"}`, http.StatusUnauthorized},
		{http.MethodPut, "/api/v1/members/m-1/assignments", `{"assignments":[]}`, http.StatusNotFound},
		{http.MethodDelete, "/api/v1/members/m-1", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/members/m-1/permission_policies", "", http.StatusUnauthorized},
		{http.MethodPost, "/api/v1/teams/t-1/permission_policies", `{}`, http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/roles", "", http.StatusNotFound},
		{http.MethodGet, "/api/v1/permissions", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/invitations", "", http.StatusUnauthorized},
		{http.MethodPost, "/api/v1/invitations", `{"email":"a@example.com"}`, http.StatusUnauthorized},
		{http.MethodPost, "/api/v1/invitations/i-1/resend", "", http.StatusUnauthorized},
		{http.MethodDelete, "/api/v1/invitations/i-1", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/teams", "", http.StatusUnauthorized},
		{http.MethodPost, "/api/v1/teams", `{"name":"payments"}`, http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/teams/t-1", "", http.StatusUnauthorized},
		{http.MethodPatch, "/api/v1/teams/t-1", `{"name":"payments"}`, http.StatusUnauthorized},
		{http.MethodDelete, "/api/v1/teams/t-1", "", http.StatusUnauthorized},
		{http.MethodPut, "/api/v1/teams/t-1/members", `{"membership_ids":[]}`, http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/teams/t-1/assignments", "", http.StatusNotFound},
		{http.MethodPut, "/api/v1/teams/t-1/assignments", `{"assignments":[]}`, http.StatusNotFound},
		{http.MethodPatch, "/api/v1/agents/a-1", `{"status":"disabled"}`, http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/enterprise/directory/groups", "", http.StatusUnauthorized},
		{http.MethodPut, "/api/v1/enterprise/directory/groups/g-1/grant", `{"role_id":"r-1","scope_kind":"organization"}`, http.StatusUnauthorized},
		{http.MethodDelete, "/api/v1/enterprise/directory/groups/g-1/grant", "", http.StatusUnauthorized},
		// Public: bad input beats auth errors, proving no session gate.
		{http.MethodPost, "/auth/invitations/preview", `{}`, http.StatusBadRequest},
		{http.MethodPost, "/auth/invitations/accept", `{}`, http.StatusBadRequest},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			var body io.Reader
			if test.body != "" {
				body = strings.NewReader(test.body)
			}
			request := httptest.NewRequest(test.method, test.path, body)
			if test.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("got status %d, want %d (body %s)", response.Code, test.status, response.Body.String())
			}
		})
	}
}
