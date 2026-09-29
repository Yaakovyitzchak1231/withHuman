package singlebinary

import (
	"net/http"
	"testing"
	"time"
)

// Exercise the agent lifecycle through the full product: the slug is the
// address and is taken for good, the name is a label unique among live
// agents, archiving answers 204 and is reversible, and restore brings the
// agent and its approval pipeline back.
func testAgentLifecycleAPI(t *testing.T, api *apiClient) {
	t.Helper()
	type agentRecord struct {
		Slug       string     `json:"slug"`
		Name       string     `json:"name"`
		Status     string     `json:"status"`
		ArchivedAt *time.Time `json:"archived_at"`
	}
	refuse := func(method, path string, body any, code string) {
		t.Helper()
		var refused struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		api.request(method, path, body, http.StatusConflict, &refused)
		if refused.Error.Code != code {
			t.Fatalf("%s %s refused with %q, want %q", method, path, refused.Error.Code, code)
		}
	}

	var created agentRecord
	api.postJSON("/api/v1/agents", map[string]any{"slug": "lifecycle", "name": "Lifecycle bot"}, http.StatusCreated, &created)
	if created.Slug != "lifecycle" || created.Name != "Lifecycle bot" || created.Status != "active" || created.ArchivedAt != nil {
		t.Fatalf("created agent = %+v", created)
	}
	api.postJSON("/api/v1/agents", map[string]any{"slug": "lifecycle-sibling", "name": "Lifecycle sibling"}, http.StatusCreated, nil)
	refuse(http.MethodPost, "/api/v1/agents", map[string]any{"slug": "lifecycle", "name": "Another name"}, "agent_slug_taken")
	refuse(http.MethodPost, "/api/v1/agents", map[string]any{"slug": "lifecycle-two", "name": "Lifecycle bot"}, "agent_name_taken")
	api.postJSON("/api/v1/agents", map[string]any{"slug": "Not A Slug", "name": "Bad slug"}, http.StatusBadRequest, nil)
	refuse(http.MethodPost, "/api/v1/agents/lifecycle/restore", nil, "agent_not_archived")

	// Renaming moves the name alone; the slug stays the address.
	path := "/api/v1/agents/lifecycle"
	var renamed agentRecord
	api.request(http.MethodPatch, path, map[string]any{"name": "Lifecycle robot"}, http.StatusOK, &renamed)
	if renamed.Slug != "lifecycle" || renamed.Name != "Lifecycle robot" || renamed.Status != "active" {
		t.Fatalf("renamed agent = %+v", renamed)
	}
	refuse(http.MethodPatch, path, map[string]any{"name": "Lifecycle sibling"}, "agent_name_taken")
	var detail agentRecord
	api.request(http.MethodGet, path, nil, http.StatusOK, &detail)
	if detail.Slug != "lifecycle" || detail.Name != "Lifecycle robot" {
		t.Fatalf("agent by slug = %+v", detail)
	}
	api.request(http.MethodGet, "/api/v1/agents/never-created", nil, http.StatusNotFound, nil)

	// Archiving keeps the agent readable by slug, refuses every write but
	// restore, keeps the slug taken and frees the name.
	api.request(http.MethodDelete, path, nil, http.StatusNoContent, nil)
	var archived agentRecord
	api.request(http.MethodGet, path, nil, http.StatusOK, &archived)
	if archived.Status != "archived" || archived.ArchivedAt == nil || archived.Slug != "lifecycle" {
		t.Fatalf("archived agent = %+v", archived)
	}
	refuse(http.MethodDelete, path, nil, "agent_archived")
	refuse(http.MethodPatch, path, map[string]any{"status": "disabled"}, "agent_archived")
	refuse(http.MethodPost, "/api/v1/agents", map[string]any{"slug": "lifecycle", "name": "Lifecycle again"}, "agent_slug_taken")
	api.postJSON("/api/v1/agents", map[string]any{"slug": "lifecycle-heir", "name": "Lifecycle robot"}, http.StatusCreated, nil)
	for _, listing := range []struct {
		status string
		want   []string
	}{
		{"live", []string{"lifecycle-heir", "lifecycle-sibling"}},
		{"archived", []string{"lifecycle"}},
		{"all", []string{"lifecycle-heir", "lifecycle-sibling", "lifecycle"}},
	} {
		var listed struct {
			Agents []agentRecord `json:"agents"`
		}
		api.request(http.MethodGet, "/api/v1/agents?status="+listing.status, nil, http.StatusOK, &listed)
		var slugs []string
		for _, agent := range listed.Agents {
			slugs = append(slugs, agent.Slug)
		}
		if len(slugs) != len(listing.want) {
			t.Fatalf("%s agents = %v, want %v", listing.status, slugs, listing.want)
		}
		for index, want := range listing.want {
			if slugs[index] != want {
				t.Fatalf("%s agents = %v, want %v", listing.status, slugs, listing.want)
			}
		}
	}

	// Restore needs the name back: the heir holds it until renamed. Then
	// the agent is active again.
	refuse(http.MethodPost, "/api/v1/agents/lifecycle/restore", nil, "agent_name_taken")
	api.request(http.MethodPatch, "/api/v1/agents/lifecycle-heir", map[string]any{"name": "Lifecycle heir"}, http.StatusOK, nil)
	var restored agentRecord
	api.request(http.MethodPost, "/api/v1/agents/lifecycle/restore", nil, http.StatusOK, &restored)
	if restored.Slug != "lifecycle" || restored.Name != "Lifecycle robot" || restored.Status != "active" || restored.ArchivedAt != nil {
		t.Fatalf("restored agent = %+v", restored)
	}
	refuse(http.MethodPost, "/api/v1/agents/lifecycle/restore", nil, "agent_not_archived")
	var none struct {
		Agents []agentRecord `json:"agents"`
	}
	api.request(http.MethodGet, "/api/v1/agents?status=archived", nil, http.StatusOK, &none)
	if len(none.Agents) != 0 {
		t.Fatalf("archived agents after restore = %+v", none.Agents)
	}
}
