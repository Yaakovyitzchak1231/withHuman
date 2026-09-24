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
)

func requiresTestRouter(principal domain.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	api := &API{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	router := gin.New()
	group := router.Group("/api/v1", func(c *gin.Context) {
		setPrincipal(c, principal)
		c.Next()
	})
	api.handle(group, http.MethodGet, "/teams", string(domain.PermissionTeamRead), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	return router
}

// The coarse gate refuses a principal holding the permission nowhere with
// the same envelope a service check produces, details included, and lets
// a principal holding it at any scope through to the handler.
func TestRequiresGatesOnHoldingThePermissionAnywhere(t *testing.T) {
	principal := domain.Principal{ActorType: domain.ActorHuman, ActorID: "user-1", OrganizationID: "org-1"}

	recorder := httptest.NewRecorder()
	requiresTestRouter(principal.WithGrants(domain.OrganizationGrants([]domain.Permission{domain.PermissionTeamWrite}))).
		ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/teams", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("without team.read = %d, want 403", recorder.Code)
	}
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Details struct {
				Permission   string `json:"permission"`
				ResourceKind string `json:"resource_kind"`
				ResourceID   string `json:"resource_id"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Type != "authorization" || body.Error.Code != "forbidden" {
		t.Fatalf("envelope = %+v, want authorization/forbidden", body.Error)
	}
	if body.Error.Details.Permission != "team.read" || body.Error.Details.ResourceKind != "organization" || body.Error.Details.ResourceID != "" {
		t.Fatalf("details = %+v", body.Error.Details)
	}

	recorder = httptest.NewRecorder()
	requiresTestRouter(principal.WithGrants([]domain.Grant{{Permission: domain.PermissionTeamRead, ScopeKind: domain.ScopeTeam, ScopeID: "team-1"}})).
		ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/teams", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("with a team-scoped team.read = %d, want 204", recorder.Code)
	}
}
