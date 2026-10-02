import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import { extensions, registerExtensions } from "./extensions";
import type { AgentSummary, ApprovalRequest, Session } from "../types";
import {
  argumentPathSuggestions,
  conditionFieldPaths,
  conditionValueSuggestions,
  loadPipelineSuggestions,
  mergeValueSuggestions,
  requestPipelineSuggestions,
  resetPipelineSuggestions,
  sampleValueSuggestions,
} from "./pipeline-suggestions";
import { setSession } from "./session-store";
import { sampleFromRequest, suggestPaths } from "./pipeline-blocks";

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
  extensions.serverDirectory = null;
  extensions.toolSources.length = 0;
  resetPipelineSuggestions();
  setSession(null);
});

describe("condition value suggestions", () => {
  it("matches escaped field paths, flattens array choices, and preserves numeric types", () => {
    const sample = JSON.stringify({ request: { arguments: { "a/b~c": ["verified", "priority", "verified"], amount: 900, nested: { empty: null, enabled: true } } } });
    const suggestions = sampleValueSuggestions(sample);
    expect(suggestions).toEqual({ "/request/arguments/a~1b~0c": ["verified", "priority"], "/request/arguments/amount": [900] });
    expect(suggestPaths(sample)).toContain("/request/arguments/a~1b~0c");
    expect(sampleValueSuggestions("{")).toEqual({});
    expect(sampleValueSuggestions("null")).toEqual({});
  });

  it("retains saved choices from nested conditions and merges sources without duplicates", () => {
    const saved = conditionValueSuggestions([{ id: "group", any: [
      { id: "tool", field: "/request/tool", value: ["Bash", "Read"] },
      { id: "nested", all: [{ id: "amount", field: "/request/arguments/amount", value: 900 }] },
    ] }]);
    expect(mergeValueSuggestions(saved, { "/request/tool": ["Read", "Write"] })).toEqual({
      "/request/tool": ["Bash", "Read", "Write"], "/request/arguments/amount": [900],
    });
  });

  /* Every agent's recent history, newest first, as the listing returns
   * it. */
  const history = [
    { agent_slug: "support", agent_name: "Support agent", tool: "refund", arguments: { currency: "GBP", order: { id: "o-2" } }, created_at: "2026-09-15T10:00:00Z", agent_reasoning: "Duplicate charge." },
    { agent_slug: "support", agent_name: "Support agent", tool: "refund", arguments: { currency: "EUR", note: "late" }, created_at: "2026-09-14T10:00:00Z" },
    { agent_slug: "release", agent_name: "Release agent", tool: "deploy", arguments: { environment: "production" }, created_at: "2026-09-13T10:00:00Z" },
  ] as ApprovalRequest[];

  function mockDirectory() {
    vi.spyOn(api, "agents").mockResolvedValue({ agents: [
      { slug: "support", name: "Support agent", last_seen_at: "2026-09-15T10:00:00Z" },
      { slug: "release", name: "Release agent", last_seen_at: "2026-09-13T10:00:00Z" },
    ] as AgentSummary[] });
    vi.spyOn(api, "recentApprovals").mockResolvedValue({ requests: history });
    vi.spyOn(api, "toolDefinitions").mockResolvedValue({
      tool_definitions: [{
        name: "merge",
        server: "github",
        input_schema: { type: "object", properties: { branch: { type: "string" } } },
        uses: [
          { runtime: "claude-code", instance_name: "alice-laptop", available: true, stale: false },
          { runtime: "claude-code", instance_name: "bob-desktop", available: false, stale: false },
        ],
      }],
      servers: ["github"],
    });
  }

  it("uses the whole catalogue and every agent's history with actual approval-request names", async () => {
    mockDirectory();
    const page = await loadPipelineSuggestions();
    expect(vi.mocked(api.recentApprovals).mock.calls).toEqual([[{ perAgent: 20, limit: 200 }]]);
    expect(page.toolSchemas.find((tool) => tool.name === "merge")).toMatchObject({
      inputSchema: { type: "object", properties: { branch: { type: "string" } } },
      source: "github · alice-laptop, github · bob-desktop · unavailable",
    });
    expect(page.valueSuggestions["/agent/slug"]).toEqual(["support", "release"]);
    expect(page.valueSuggestions["/agent/name"]).toEqual(["Support agent", "Release agent"]);
    expect(page.valueSuggestions["/request/tool"]).toEqual(["merge", "refund", "deploy"]);
    expect(page.valueSuggestions["/request/server"]).toEqual(["github"]);
    expect(page.valueSuggestions["/request/arguments/currency"]).toEqual(["GBP", "EUR"]);
  });

  it("learns argument fields from the histories, most common first, and seeds the sample from the newest request", async () => {
    mockDirectory();
    const page = await loadPipelineSuggestions();
    expect(page.argumentPaths).toEqual([
      "/request/arguments/currency",
      "/request/arguments/order",
      "/request/arguments/order/id",
      "/request/arguments/note",
      "/request/arguments/environment",
    ]);
    expect(page.sample).toEqual({
      request: { tool: "refund", arguments: { currency: "GBP", order: { id: "o-2" } }, agent_reasoning: "Duplicate charge." },
      agent: { slug: "support", name: "Support agent" },
    });
  });

  it("keeps the history when agents cannot be listed and leaves the sample to the placeholder without requests", async () => {
    mockDirectory();
    vi.mocked(api.agents).mockRejectedValue(new Error("Forbidden"));
    vi.mocked(api.recentApprovals).mockResolvedValue({ requests: history.slice(2) });
    const page = await loadPipelineSuggestions();
    expect(page.argumentPaths).toEqual(["/request/arguments/environment"]);
    expect(page.sample?.agent).toEqual({ slug: "release", name: "Release agent" });
    expect(page.valueSuggestions["/agent/slug"]).toEqual([]);

    vi.mocked(api.recentApprovals).mockResolvedValue({ requests: [] });
    const empty = await loadPipelineSuggestions();
    expect(empty.argumentPaths).toEqual([]);
    expect(empty.sample).toBeNull();
  });

  it("keeps available choices when optional directories are forbidden or unavailable", async () => {
    mockDirectory();
    vi.mocked(api.toolDefinitions).mockRejectedValue(new Error("Forbidden"));
    const page = await loadPipelineSuggestions();
    expect(page.valueSuggestions["/request/tool"]).toEqual(["refund", "deploy"]);
    expect(page.valueSuggestions["/agent/name"]).toEqual(["Support agent", "Release agent"]);
  });

  it("offers the edition's registered servers first, named by their source, and catalogue sources without tools", async () => {
    mockDirectory();
    vi.mocked(api.toolDefinitions).mockResolvedValue({ tool_definitions: [], servers: ["linear"] });
    registerExtensions({
      toolSources: [{ source: "gateway", label: "Gateway", external: true }],
      serverDirectory: async () => [{ server: "github-prod", name: "GitHub production", source: "gateway" }],
    });
    const page = await loadPipelineSuggestions();
    expect(page.valueSuggestions["/request/server"]).toEqual(["github-prod", "linear"]);
    expect(page.valueDetails).toEqual({ "/request/server": { "github-prod": "Gateway: GitHub production" } });
  });

  it("offers a registered server's arguments as fields on every tool, before any discovery", async () => {
    mockDirectory();
    registerExtensions({
      serverDirectory: async () => [
        { server: "cloudflare", name: "Cloudflare", source: "gateway", arguments: [{ name: "withhuman_scope", description: "The scope the call runs with.", values: ["personal_read", "personal_write"] }] },
        { server: "linear", name: "Linear", source: "gateway" },
      ],
    });
    const page = await loadPipelineSuggestions();
    const schema = page.toolSchemas.find((tool) => tool.everyTool);
    expect(schema).toEqual({
      name: "Cloudflare",
      source: "gateway",
      everyTool: true,
      inputSchema: { type: "object", properties: { withhuman_scope: { type: "string", description: "The scope the call runs with.", enum: ["personal_read", "personal_write"] } } },
    });
    expect(page.toolSchemas.filter((tool) => tool.everyTool)).toHaveLength(1);
  });

  it("keeps the observed servers when the edition's directory refuses the reader", async () => {
    mockDirectory();
    registerExtensions({ serverDirectory: () => Promise.reject(new Error("Forbidden")) });
    const page = await loadPipelineSuggestions();
    expect(page.valueSuggestions["/request/server"]).toEqual(["github"]);
    expect(page.valueDetails).toEqual({ "/request/server": {} });
  });
});

describe("the shared suggestions", () => {
  function signIn(organization: string) {
    setSession({ principal: { actor_type: "human", actor_id: "user-1", organization_id: organization, permissions: [], grants: [] }, assignments: [] } as unknown as Session);
  }

  function mockReads() {
    vi.spyOn(api, "agents").mockResolvedValue({ agents: [] });
    vi.spyOn(api, "recentApprovals").mockResolvedValue({ requests: [] });
    vi.spyOn(api, "toolDefinitions").mockResolvedValue({ tool_definitions: [], servers: [] });
  }

  it("loads once for every pipeline while fresh, and again once it ages", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    signIn("org-1");
    mockReads();
    void requestPipelineSuggestions();
    await requestPipelineSuggestions();
    await requestPipelineSuggestions();
    expect(api.toolDefinitions).toHaveBeenCalledTimes(1);
    vi.setSystemTime(Date.now() + 61_000);
    void requestPipelineSuggestions();
    expect(api.toolDefinitions).toHaveBeenCalledTimes(2);
  });

  it("loads again for someone else signed in", async () => {
    signIn("org-1");
    mockReads();
    await requestPipelineSuggestions();
    signIn("org-2");
    void requestPipelineSuggestions();
    expect(api.toolDefinitions).toHaveBeenCalledTimes(2);
  });
});

describe("argument field learning", () => {
  it("counts a field once per request, escapes pointer tokens, and treats arrays as values", () => {
    expect(argumentPathSuggestions([
      { arguments: { "a/b": 1, tags: ["x", "y"], meta: { meta: 2 } } },
      { arguments: { meta: { meta: 3 } } },
      { arguments: "not an object" },
    ])).toEqual(["/request/arguments/meta", "/request/arguments/meta/meta", "/request/arguments/a~1b", "/request/arguments/tags"]);
  });

  it("collects the fields saved conditions compare, nested groups and array predicates included", () => {
    expect(conditionFieldPaths([{ id: "g", all: [
      { id: "a", field: "/request/tool" },
      { id: "b", any: [{ id: "c", field: "/request/arguments/amount" }, { id: "d", some: { field: "/request/arguments/items", where: { id: "e" } } }] },
      { id: "f" },
    ] }])).toEqual(["/request/tool", "/request/arguments/amount", "/request/arguments/items"]);
  });

  it("builds a sample from a request, dropping context that is not an object", () => {
    expect(sampleFromRequest({ tool: "deploy", arguments: ["odd"], agent_slug: "release", agent_name: "Release", context: "n/a" })).toEqual({
      request: { tool: "deploy", arguments: {} },
      agent: { slug: "release", name: "Release" },
    });
  });
});
