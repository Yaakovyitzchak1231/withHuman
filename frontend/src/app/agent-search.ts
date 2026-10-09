import type { ApprovalStatus } from "../types";
import { agentTab } from "./extensions";

export const AGENT_PAGE_SIZE = 25;
export interface AgentSearch {
  /* One of core's tabs (instances, provisioners, requests, tools) or the
   * id of a tab the edition registered. Validation fills in instances. */
  tab?: string;
  q?: string;
  /* A tool source's name: the runtime's own or one the edition adds. */
  source?: string;
  tool?: string;
  status?: ApprovalStatus | "active" | "disabled" | "revoked" | "available" | "unavailable" | "stale";
  page?: number;
}

/* A source name as the catalogue's contract spells one. */
const TOOL_SOURCE_PATTERN = /^[a-z][a-z0-9_-]{0,31}$/;

function coreTab(value: unknown): value is "provisioners" | "instances" | "requests" | "tools" {
  return value === "tools" || value === "requests" || value === "instances" || value === "provisioners";
}

export function validateAgentSearch(raw: Record<string, unknown>): AgentSearch {
  const editionTab = typeof raw.tab === "string" ? agentTab(raw.tab) : undefined;
  const tab = coreTab(raw.tab) ? raw.tab : editionTab ? editionTab.id : "instances";
  const search: AgentSearch = { tab };
  if (editionTab) {
    /* An edition tab filters client side, so its search is kept exactly
     * as typed and nothing else carries over from the lists. */
    if (typeof raw.q === "string" && raw.q) search.q = raw.q.slice(0, 200);
    return search;
  }
  if (typeof raw.q === "string" && raw.q.trim())
    search.q = raw.q.trim().slice(0, 200);
  if (tab === "tools") {
    if (typeof raw.source === "string" && TOOL_SOURCE_PATTERN.test(raw.source)) search.source = raw.source;
    if (typeof raw.tool === "string" && raw.tool.length <= 128) search.tool = raw.tool;
  }
  const statuses =
    tab === "tools" ? ["available", "unavailable", "stale"] :
    tab === "instances"
      ? ["active", "disabled"]
      : tab === "provisioners"
        ? ["active", "expired", "revoked"]
        : ["pending", "approved", "denied", "expired", "cancelled"];
  if (typeof raw.status === "string" && statuses.includes(raw.status)) {
    search.status = raw.status as AgentSearch["status"];
  }
  const page = Number(raw.page);
  if (Number.isSafeInteger(page) && page > 0 && page <= 1_000_000)
    search.page = page;
  return search;
}
