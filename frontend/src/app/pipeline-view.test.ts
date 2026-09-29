import { afterEach, describe, expect, it, vi } from "vitest";
import { api, RequestError } from "../api";
import type { RequestApprovalPipelineRevision } from "../types";
import { loadPipelineView } from "./pipeline-view";
import { pipelineLink, pipelineSearch } from "./pipeline-link";

function revision(number: number, pipelineKey = "entry"): RequestApprovalPipelineRevision {
  return {
    id: `revision-${pipelineKey}-${number}`,
    pipeline_key: pipelineKey,
    name: pipelineKey === "entry" ? "Entry pipeline" : pipelineKey,
    revision: number,
    is_active: number === 3,
    block_count: 0,
    blocks: [],
    created_by_actor_id: "author",
    created_at: "2026-09-14T10:00:00Z",
  };
}

afterEach(() => vi.restoreAllMocks());

describe("pipeline review links", () => {
  it("preserves the pipeline key, historical revision, and exact block key", () => {
    const search = pipelineSearch({ revision: "2", block: "production-review" });
    expect(pipelineLink("entry", search)).toEqual({
      to: "/pipelines/$pipelineKey",
      params: { pipelineKey: "entry" },
      search: { revision: 2, block: "production-review" },
    });
    expect(pipelineLink("release", search)).toEqual({
      to: "/pipelines/$pipelineKey",
      params: { pipelineKey: "release" },
      search: { revision: 2, block: "production-review" },
    });
  });

  it("rejects invalid revision numbers and non-text block keys", () => {
    for (const revision of [0, -1, "2.5", "abc", true, [], Infinity]) {
      expect(pipelineSearch({ revision, block: {} })).toEqual({});
    }
    expect(pipelineSearch({ edit: "true", block: " " })).toEqual({ edit: true });
  });
});

describe("loading a pipeline from a review link", () => {
  function mockPage() {
    vi.spyOn(api, "requestApprovalPipelineRevisions").mockImplementation(async (pipelineKey) => ({
      revisions: [revision(3, pipelineKey)],
      total_count: 3,
    }));
    vi.spyOn(api, "activeRequestApprovalPipeline").mockImplementation(async (pipelineKey) => revision(3, pipelineKey));
    vi.spyOn(api, "requestApprovalPipelines").mockResolvedValue({ request_approval_pipelines: [] });
    vi.spyOn(api, "requestApprovalPipelineUses").mockResolvedValue({ uses: [] });
    vi.spyOn(api, "escalationPaths").mockResolvedValue({ escalation_paths: [] });
    vi.spyOn(api, "runtimeConfig").mockRejectedValue(new Error("No optional block configuration"));
    vi.spyOn(api, "agents").mockResolvedValue({ agents: [] });
    vi.spyOn(api, "approvals").mockResolvedValue({ requests: [] });
  }

  it.each(["entry", "release"])("loads the pinned revision separately from the active revision for pipeline %s", async (pipelineKey) => {
    mockPage();
    const readRevision = vi.spyOn(api, "requestApprovalPipelineRevision").mockResolvedValue(revision(2, pipelineKey));
    const page = await loadPipelineView(pipelineKey, 2);
    expect(readRevision).toHaveBeenCalledWith(pipelineKey, 2);
    expect(page.linkedRevision?.revision).toBe(2);
    expect(page.displayed?.revision).toBe(3);
    expect(page.error).toBe("");
  });

  it("asks who delegates to a pipeline, but never for the entry pipeline", async () => {
    mockPage();
    await loadPipelineView("release");
    expect(api.requestApprovalPipelineUses).toHaveBeenCalledWith("release");
    vi.mocked(api.requestApprovalPipelineUses).mockClear();
    await loadPipelineView("entry");
    expect(api.requestApprovalPipelineUses).not.toHaveBeenCalled();
  });

  it("reports a missing pinned revision instead of silently showing today's pipeline", async () => {
    mockPage();
    vi.spyOn(api, "requestApprovalPipelineRevision").mockRejectedValue(new RequestError("Not found", 404, "not_found"));
    const page = await loadPipelineView("entry", 2);
    expect(page.linkedRevision).toBeNull();
    expect(page.displayed).toBeNull();
    expect(page.missing).toBe(false);
    expect(page.error).toBe("Revision 2 could not be found.");
  });
});
