import { sampleValueSuggestions } from "./request-suggestions";
import { api } from "../api";
import type { ApprovalRequest, ToolCatalogEntry, ToolCatalogPage } from "../types";
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
export function catalogSchemas(entries: ToolCatalogEntry[], requests: ApprovalRequest[]): ToolSchema[] {
  const schemas: ToolSchema[] = entries.map((entry) => ({
    name: entry.name,
    source: `${entry.server || entry.runtime}${entry.instance_name ? ` · ${entry.instance_name}` : ""}${entry.available ? "" : " · unavailable"}${entry.stale ? " · stale" : ""}`,
    description: entry.description,
    inputSchema: entry.input_schema,
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
export interface AgentCatalogData extends CatalogData {
  selected?: ToolCatalogEntry;
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
    next_offset: offset + 25 < filtered.length ? offset + 25 : null,
    selected: search.tool ? catalog.tools.find((tool) => tool.id === search.tool) : undefined,
  };
}
