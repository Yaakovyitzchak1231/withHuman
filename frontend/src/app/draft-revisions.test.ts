import { afterEach, describe, expect, it, vi } from "vitest";
import { api, RequestError } from "../api";
import type {
  EscalationPathRevisionWire,
  Team,
  TeamEscalationPolicyRevisionWire,
  WebhookEndpointRevision,
} from "../types";
import { loadEscalationPathPage } from "./escalation-path";
import { loadTeamEscalationPolicyPage } from "./team-escalation-policy";
import { loadWebhookEndpointPage } from "./webhook-endpoint";

/* A key whose only revisions were never activated opens its latest
 * draft, so the editor changes it in place instead of starting over. */

const notFound = () => new RequestError("Not found", 404, "not_found");

afterEach(() => {
  vi.restoreAllMocks();
});

function pathRevision(revision: number, fields: Partial<EscalationPathRevisionWire> = {}): EscalationPathRevisionWire {
  return {
    id: `path-${revision}`,
    path_key: "payments",
    revision,
    is_active: false,
    name: `Payments ${revision}`,
    document_version: 2,
    created_by_actor_id: "author",
    created_at: "2026-10-01T09:00:00Z",
    working_hours: [],
    nodes: [],
    warnings: [],
    ...fields,
  };
}

function endpointRevision(revision: number, fields: Partial<WebhookEndpointRevision> = {}): WebhookEndpointRevision {
  return {
    id: `endpoint-${revision}`,
    endpoint_key: "fraud",
    revision,
    is_active: false,
    name: `Fraud ${revision}`,
    url: `https://fraud.example.com/${revision}`,
    created_by_actor_id: "author",
    created_at: "2026-10-01T09:00:00Z",
    ...fields,
  };
}

describe("escalation path drafts", () => {
  function mockPath(revisions: EscalationPathRevisionWire[], active: EscalationPathRevisionWire | null) {
    vi.spyOn(api, "members").mockResolvedValue({ members: [] } as never);
    vi.spyOn(api, "teams").mockResolvedValue({ teams: [] } as never);
    vi.spyOn(api, "escalationPathUses").mockResolvedValue({ uses: [] });
    vi.spyOn(api, "escalationPathRevisions").mockResolvedValue({ revisions, total_count: revisions.length });
    vi.spyOn(api, "activeEscalationPath").mockImplementation(async () => {
      if (!active) throw notFound();
      return active;
    });
    vi.spyOn(api, "escalationPathRevision").mockImplementation(async (_key, number) => {
      const found = revisions.find((revision) => revision.revision === number);
      if (!found) throw notFound();
      return found;
    });
  }

  it("opens the latest draft when nothing is active", async () => {
    mockPath([pathRevision(2), pathRevision(1)], null);
    const page = await loadEscalationPathPage("payments");
    expect(page.draftRevision).toBe(2);
    expect(page.saved?.name).toBe("Payments 2");
    expect(page.archivedRevision).toBeNull();
  });

  it("opens the active revision, never a newer draft", async () => {
    const active = pathRevision(1, { is_active: true, activated_at: "2026-10-01T10:00:00Z" });
    mockPath([pathRevision(2), active], active);
    const page = await loadEscalationPathPage("payments");
    expect(page.draftRevision).toBeNull();
    expect(page.saved?.name).toBe("Payments 1");
  });

  it("treats an archived path's revisions as history, not drafts", async () => {
    const archived = pathRevision(1, { archived_at: "2026-10-02T09:00:00Z" });
    mockPath([archived], null);
    const page = await loadEscalationPathPage("payments");
    expect(page.draftRevision).toBeNull();
    expect(page.archivedRevision?.revision).toBe(1);
  });
});

describe("webhook endpoint drafts", () => {
  function mockEndpoint(revisions: WebhookEndpointRevision[], active: WebhookEndpointRevision | null) {
    vi.spyOn(api, "activeWebhookEndpoint").mockImplementation(async () => {
      if (!active) throw notFound();
      return active;
    });
    vi.spyOn(api, "webhookEndpointRevisions").mockResolvedValue({ revisions, total_count: revisions.length });
    vi.spyOn(api, "webhookEndpointUses").mockResolvedValue({ uses: [] });
    vi.spyOn(api, "webhookEndpoints").mockResolvedValue({ webhook_endpoints: [] });
  }

  it("opens the latest draft when nothing is active", async () => {
    mockEndpoint([endpointRevision(2), endpointRevision(1)], null);
    const page = await loadWebhookEndpointPage("fraud");
    expect(page.draftRevision).toBe(2);
    expect(page.saved).toEqual({ name: "Fraud 2", url: "https://fraud.example.com/2" });
  });

  it("does not treat a revision that was once active as a draft", async () => {
    mockEndpoint([endpointRevision(1, { activated_at: "2026-10-01T10:00:00Z" })], null);
    const page = await loadWebhookEndpointPage("fraud");
    expect(page.draftRevision).toBeNull();
  });
});

describe("team escalation policy drafts", () => {
  function team(active: number | null, latest: number | null): Team {
    return {
      archived_at: null,
      id: "team-1",
      name: "Payments",
      directory_managed: false,
      member_count: 0,
      assignments: [],
      escalation_policy: { active_revision: active, latest_revision: latest },
      created_at: "2026-10-01T09:00:00Z",
    } as Team;
  }

  function policyRevision(revision: number, fields: Partial<TeamEscalationPolicyRevisionWire> = {}): TeamEscalationPolicyRevisionWire {
    return {
      id: `policy-${revision}`,
      team_id: "team-1",
      revision,
      is_active: false,
      created_by_actor_id: "author",
      created_at: "2026-10-01T09:00:00Z",
      document_version: 2,
      working_hours: [],
      nodes: [],
      warnings: [],
      ...fields,
    };
  }

  it("opens the latest draft when nothing is active", async () => {
    vi.spyOn(api, "getTeam").mockResolvedValue({ team: team(null, 2), members: [] });
    vi.spyOn(api, "teamEscalationPolicy").mockRejectedValue(notFound());
    vi.spyOn(api, "teamEscalationPolicyRevisions").mockResolvedValue({ revisions: [policyRevision(2), policyRevision(1)] });
    const read = vi.spyOn(api, "teamEscalationPolicyRevision").mockResolvedValue(policyRevision(2));
    const page = await loadTeamEscalationPolicyPage("team-1");
    expect(read).toHaveBeenCalledWith("team-1", 2);
    expect(page.draft?.revision).toBe(2);
    expect(page.policy).toBeNull();
  });

  it("loads no draft while a revision is active", async () => {
    const active = policyRevision(1, { is_active: true, activated_at: "2026-10-01T10:00:00Z" });
    vi.spyOn(api, "getTeam").mockResolvedValue({ team: team(1, 2), members: [] });
    vi.spyOn(api, "teamEscalationPolicy").mockResolvedValue(active);
    vi.spyOn(api, "teamEscalationPolicyRevisions").mockResolvedValue({ revisions: [policyRevision(2), active] });
    const read = vi.spyOn(api, "teamEscalationPolicyRevision");
    const page = await loadTeamEscalationPolicyPage("team-1");
    expect(read).not.toHaveBeenCalled();
    expect(page.draft).toBeNull();
    expect(page.policy?.revision).toBe(1);
  });
});
