import { sampleValueSuggestions } from "./request-suggestions";
import { api } from "../api";
import type { ApprovalRequest, CatalogToolDefinition, ToolCatalogEntry, ToolCatalogPage, ToolCatalogSource } from "../types";
import type { ToolSchema } from "@/ds/lib/tool-schema";
import { argumentPaths } from "./pipeline-blocks";
import type { AgentSearch } from "./agent-search";
import { extensions, type ToolSourceEntry } from "./extensions";

/* The sources an agent runtime reports about itself: its built-in tools
 * and the MCP servers it is configured with. The edition's registered
 * sources sit beside them; a name neither knows shows as it is. */
export const RUNTIME_TOOL_SOURCES: ToolSourceEntry[] = [
  { source: "builtin", label: "Built-in", external: false },
  { source: "mcp", label: "MCP", external: false },
];
export function toolSources(): ToolSourceEntry[] {
  return [...RUNTIME_TOOL_SOURCES, ...extensions.toolSources];
}
export function toolSourceLabel(source: string): string {
  return toolSources().find((entry) => entry.source === source)?.label ?? source;
}
/* The edition's sources whose tools are discovered centrally and
 * granted to agents, which a catalogue refresh rediscovers. */
export function externalToolSources(): ToolSourceEntry[] {
  return extensions.toolSources.filter((entry) => entry.external);
}

export interface CatalogData extends ToolCatalogPage { error: string }
export async function loadToolCatalog(agentSlug?: string, refresh = false): Promise<CatalogData> {
  const tools: ToolCatalogEntry[] = [];
  let sources: ToolCatalogPage["sources"] = [];
  try {
    let offset: number | undefined = 0;
    while (offset !== undefined) {
      const page = await api.toolCatalog({ agent_slug: agentSlug || undefined, limit: 500, offset, refresh: refresh && offset === 0 });
      tools.push(...page.tools);
      sources = page.sources;
      const next = page.next_offset;
      if (next !== null && next <= offset) throw new Error("The tool catalogue returned an invalid page.");
      offset = next ?? undefined;
    }
    return { tools, sources, total: tools.length, next_offset: null, error: "" };
  } catch {
    return { tools, sources, total: tools.length, next_offset: null, error: "The tool catalogue could not be loaded. Manual conditions and visible request history remain available." };
  }
}
/* One schema per distinct definition, named by every place that offers
 * it. */
export function catalogSchemas(definitions: CatalogToolDefinition[], requests: ApprovalRequest[]): ToolSchema[] {
  const schemas: ToolSchema[] = definitions.map((definition) => ({
    name: definition.name,
    source: [...new Set(definition.uses.map((use) =>
      `${definition.server || use.runtime}${use.instance_name ? ` · ${use.instance_name}` : ""}${use.available ? "" : " · unavailable"}${use.stale ? " · stale" : ""}`,
    ))].join(", "),
    description: definition.description,
    inputSchema: definition.input_schema,
  }));
  for (const request of requests) {
    const matching = schemas.filter((tool) => tool.name === request.tool);
    const values = sampleValueSuggestions(JSON.stringify({ request: { arguments: request.arguments } }));
    // History supplies fields only where discovery has no schema. A sample
    // never claims requiredness, allowed values, or a complete type contract.
    if (!matching.length) schemas.push({ name: request.tool, source: "Observed in recent requests", observedPaths: argumentPaths(request.arguments), observedValues: values });
    else for (const tool of matching) {
      if (!tool.inputSchema) tool.observedPaths = [...new Set([...(tool.observedPaths ?? []), ...argumentPaths(request.arguments)])];
      for (const [path, choices] of Object.entries(values)) {
        tool.observedValues ??= {};
        tool.observedValues[path] = [...new Set([...(tool.observedValues[path] ?? []), ...choices])];
      }
    }
  }
  return schemas;
}
/* Why one place could not list a server's tools: the instance that
 * tried, when an instance did, its runtime, and the reason it gave. */
export interface CatalogServerFailure {
  instance?: string;
  runtime: string;
  error: string;
  lastAttemptAt: string;
}
/* One server as an agent's catalogue sees it, folded across the
 * instances and projects that report it. Built-in tools fold per
 * runtime. */
export interface CatalogServer {
  id: string;
  name: string;
  source: string;
  external: boolean;
  /* The instances that report it, failed or not. */
  instances: string[];
  /* Distinct tools listed now or retained from the last success. */
  tools: number;
  failures: CatalogServerFailure[];
  lastSuccessAt: string | null;
}
const newest = (a: string | null, b: string | null) => (!a ? b : !b ? a : a > b ? a : b);
/* The catalogue's servers, failing first. A source from an instance
 * that can no longer report is left out: its failure is history, not
 * something anyone can fix there. */
export function catalogServers(sources: ToolCatalogSource[], tools: ToolCatalogEntry[]): CatalogServer[] {
  const external = new Set(externalToolSources().map((entry) => entry.source));
  const keyOf = (item: { source: string; server?: string; runtime: string }) =>
    item.source === "builtin" ? `builtin:${item.runtime}` : `${item.source}:${item.server ?? ""}`;
  const servers = new Map<string, CatalogServer>();
  const names = new Map<string, Set<string>>();
  for (const source of sources) {
    const isExternal = external.has(source.source);
    if (!isExternal && !source.available) continue;
    const id = keyOf(source);
    let server = servers.get(id);
    if (!server) {
      server = { id, name: source.source === "builtin" ? source.runtime : source.server || source.runtime, source: source.source, external: isExternal, instances: [], tools: 0, failures: [], lastSuccessAt: null };
      servers.set(id, server);
      names.set(id, new Set());
    }
    if (source.instance_name && !server.instances.includes(source.instance_name)) server.instances.push(source.instance_name);
    server.lastSuccessAt = newest(server.lastSuccessAt, source.last_success_at);
    if (!source.error) continue;
    const same = server.failures.find((failure) => failure.instance === source.instance_name && failure.runtime === source.runtime && failure.error === source.error);
    if (same) same.lastAttemptAt = newest(same.lastAttemptAt, source.last_attempt_at) ?? same.lastAttemptAt;
    else server.failures.push({ instance: source.instance_name, runtime: source.runtime, error: source.error, lastAttemptAt: source.last_attempt_at });
  }
  for (const tool of tools) {
    if (tool.available) names.get(keyOf(tool))?.add(tool.name);
  }
  for (const server of servers.values()) {
    server.tools = names.get(server.id)?.size ?? 0;
    server.instances.sort();
  }
  return [...servers.values()].sort((a, b) => Number(b.failures.length > 0) - Number(a.failures.length > 0) || a.name.localeCompare(b.name));
}

export interface AgentCatalogData extends CatalogData {
  selected?: ToolCatalogEntry;
  servers: CatalogServer[];
}
export async function loadAgentCatalog(slug: string, search: AgentSearch): Promise<AgentCatalogData> {
  const catalog = await loadToolCatalog(slug);
  const filtered = catalog.tools.filter((tool) =>
    (!search.source || tool.source === search.source) &&
    (!search.status || (search.status === "stale" ? tool.stale : search.status === "available" ? tool.available : !tool.available)) &&
    (!search.q || `${tool.name} ${tool.description ?? ""} ${tool.server ?? ""}`.toLowerCase().includes(search.q.toLowerCase())),
  );
  const offset = (search.page ?? 0) * 25;
  return {
    ...catalog, tools: filtered.slice(offset, offset + 25), total: filtered.length,
    servers: catalogServers(catalog.sources, catalog.tools),
    next_offset: offset + 25 < filtered.length ? offset + 25 : null,
    selected: search.tool ? catalog.tools.find((tool) => tool.id === search.tool) : undefined,
  };
}
