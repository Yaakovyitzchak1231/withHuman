import { describe, expect, it } from "vitest";
import { registerExtensions } from "./extensions";
import { catalogServers } from "./tool-catalog";
import type { ToolCatalogEntry, ToolCatalogSource } from "../types";

registerExtensions({ toolSources: [{ source: "gateway", label: "Gateway", external: true }] });

let next = 0;
function source(fields: Partial<ToolCatalogSource>): ToolCatalogSource {
  next += 1;
  return {
    id: `source-${next}`, agent_slug: "support", runtime: "claude-code", discovery_context: "project-a", source: "mcp",
    available: true, last_attempt_at: "2026-10-02T10:00:00Z", last_success_at: null, ...fields,
  };
}
function tool(fields: Partial<ToolCatalogEntry>): ToolCatalogEntry {
  return {
    id: `tool-${fields.name}`, name: "tool", agent_slug: "support", discovery_context: "project-a", runtime: "claude-code", source: "mcp",
    available: true, stale: false, schema_fingerprint: "", last_seen_at: "2026-10-02T10:00:00Z", last_attempt_at: "2026-10-02T10:00:00Z", ...fields,
  };
}

describe("catalogServers", () => {
  it("folds one server's sources across instances and projects", () => {
    const failed = "The server did not respond in time.";
    const servers = catalogServers([
      source({ server: "linear", instance_name: "Laptop", error: failed }),
      source({ server: "linear", instance_name: "Laptop", discovery_context: "project-b", error: failed, last_attempt_at: "2026-10-02T11:00:00Z" }),
      source({ server: "linear", instance_name: "Build box", runtime: "codex", last_success_at: "2026-10-01T09:00:00Z" }),
      source({ server: "github", instance_name: "Laptop", last_success_at: "2026-10-02T09:00:00Z" }),
    ], [tool({ name: "create_issue", server: "linear" }), tool({ name: "create_issue", server: "linear", discovery_context: "project-b" }), tool({ name: "list_prs", server: "github" })]);
    expect(servers.map((server) => server.name)).toEqual(["linear", "github"]);
    expect(servers[0]).toMatchObject({ instances: ["Build box", "Laptop"], tools: 1, lastSuccessAt: "2026-10-01T09:00:00Z" });
    expect(servers[0].failures).toEqual([{ instance: "Laptop", runtime: "claude-code", error: failed, lastAttemptAt: "2026-10-02T11:00:00Z" }]);
    expect(servers[1].failures).toEqual([]);
  });

  it("leaves out sources from instances that can no longer report", () => {
    const servers = catalogServers([source({ server: "posthog", instance_name: "Old laptop", available: false, error: "The server could not be reached." })], []);
    expect(servers).toEqual([]);
  });

  it("keeps an edition's failing external source and folds built-in tools per runtime", () => {
    const servers = catalogServers([
      source({ source: "gateway", runtime: "mcp-gateway", discovery_context: "gateway", server: "ahrefs", available: false, error: "No credential is connected." }),
      source({ source: "builtin", instance_name: "Laptop", last_success_at: "2026-10-02T09:00:00Z" }),
    ], [tool({ name: "Bash", source: "builtin" })]);
    expect(servers.map((server) => [server.name, server.external, server.failures.length, server.tools])).toEqual([["ahrefs", true, 1, 0], ["claude-code", false, 0, 1]]);
  });
});
