package singlebinary

import (
	"net/http"
	"testing"
	"time"
)

// Exercise the archive contract through the full product, including the
// embedded teams page, authenticated HTTP handlers, and real database.
func testTeamArchiveAPI(t *testing.T, api *apiClient) {
	t.Helper()
	type teamRecord struct {
		ID         string     `json:"id"`
		Name       string     `json:"name"`
		ArchivedAt *time.Time `json:"archived_at"`
	}
	var created struct {
		Team teamRecord `json:"team"`
	}
	api.postJSON("/api/v1/teams", map[string]any{"name": "Archive test"}, http.StatusCreated, &created)
	if created.Team.ID == "" || created.Team.ArchivedAt != nil {
		t.Fatalf("created team = %+v", created.Team)
	}
	path := "/api/v1/teams/" + created.Team.ID
	api.request(http.MethodDelete, path, nil, http.StatusNoContent, nil)
	var archived struct {
		Team teamRecord `json:"team"`
	}
	api.request(http.MethodGet, path, nil, http.StatusOK, &archived)
	if archived.Team.ArchivedAt == nil || archived.Team.Name != created.Team.Name {
		t.Fatalf("archived team = %+v", archived.Team)
	}
	api.request(http.MethodDelete, path, nil, http.StatusNoContent, nil)
	var repeated struct {
		Team teamRecord `json:"team"`
	}
	api.request(http.MethodGet, path, nil, http.StatusOK, &repeated)
	if repeated.Team.ArchivedAt == nil || !repeated.Team.ArchivedAt.Equal(*archived.Team.ArchivedAt) {
		t.Fatalf("repeat archive changed timestamp: %+v", repeated.Team)
	}
	for _, status := range []string{"live", "archived", "all"} {
		var listing struct {
			Teams []teamRecord `json:"teams"`
		}
		api.request(http.MethodGet, "/api/v1/teams?status="+status, nil, http.StatusOK, &listing)
		want := 1
		if status == "live" {
			want = 0
		}
		if len(listing.Teams) != want {
			t.Fatalf("%s teams = %+v", status, listing.Teams)
		}
	}
	api.request(http.MethodGet, "/api/v1/teams?status=invalid", nil, http.StatusBadRequest, nil)
	api.request(http.MethodPatch, path, map[string]any{"name": "Cannot rename"}, http.StatusConflict, nil)
	api.request(http.MethodPut, path+"/members", map[string]any{"membership_ids": []string{}}, http.StatusConflict, nil)
	// The open edition offers predefined roles alone, so a team policy
	// write is refused before the archived team is even looked at.
	api.request(http.MethodPost, path+"/permission_policies", map[string]any{"scope_kind": "organization", "resource_ids": []string{}, "permissions": []string{"request.read"}}, http.StatusForbidden, nil, "Idempotency-Key", "archived-team-policy")
	api.request(http.MethodGet, "/settings/teams?status=archived", nil, http.StatusOK, nil)
}
