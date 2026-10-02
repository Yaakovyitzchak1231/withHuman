import { useSyncExternalStore } from "react";
import { sampleValueSuggestions } from "./request-suggestions";
export { sampleValueSuggestions } from "./request-suggestions";
import { catalogSchemas, toolSourceLabel } from "./tool-catalog";
import { currentSession } from "./session-store";
import type { ToolSchema } from "@/ds/lib/tool-schema";
import type { ConditionNode, ConditionValueDetails, ConditionValueSuggestions } from "@/ds/components/condition-builder";
import { api } from "../api";
import { extensions, type ServerDirectoryEntry } from "./extensions";
import type { ApprovalRequest, RequestApprovalPipelinePreviewSample } from "../types";
import { argumentPaths, sampleFromRequest } from "./pipeline-blocks";

export function mergeValueSuggestions(...sources: ConditionValueSuggestions[]): ConditionValueSuggestions {
  const result: ConditionValueSuggestions = {};
  for (const source of sources) {
    for (const [path, values] of Object.entries(source)) {
      result[path] = [...new Set([...(result[path] ?? []), ...values])];
    }
  }
  return result;
}

export function conditionValueSuggestions(nodes: ConditionNode[]): ConditionValueSuggestions {
  return mergeValueSuggestions(...nodes.map((node) => {
    if (node.all || node.any) return conditionValueSuggestions(node.all ?? node.any ?? []);
    if (!node.field) return {};
    const values = Array.isArray(node.value) ? node.value : [node.value];
    return { [node.field]: values.filter((value): value is string | number => (typeof value === "string" && value !== "") || typeof value === "number") };
  }));
}

/* The fields saved conditions already compare, in tree order. */
export function conditionFieldPaths(nodes: ConditionNode[]): string[] {
  return nodes.flatMap((node) => {
    if (node.all || node.any) return conditionFieldPaths(node.all ?? node.any ?? []);
    const field = node.field ?? node.some?.field ?? node.every?.field;
    return field ? [field] : [];
  });
}

/* Every argument key the requests carried, most common first; ties keep
 * the order of the newest request that has them. */
export function argumentPathSuggestions(requests: Pick<ApprovalRequest, "arguments">[]): string[] {
  const counts = new Map<string, number>();
  for (const request of requests) {
    for (const path of new Set(argumentPaths(request.arguments))) {
      counts.set(path, (counts.get(path) ?? 0) + 1);
    }
  }
  return [...counts.entries()].sort((a, b) => b[1] - a[1]).map(([path]) => path);
}

/* Every agent's recent history, automatic decisions included, in one
 * listing: each agent contributes at most its newest rows, so a busy
 * agent cannot crowd the quieter ones out of the total. */
const HISTORY_PER_AGENT = 20;
const HISTORY_ROWS = 200;

function loadRecentRequests(): Promise<ApprovalRequest[]> {
  return api.recentApprovals({ perAgent: HISTORY_PER_AGENT, limit: HISTORY_ROWS }).then((page) => page.requests, () => []);
}

/* The arguments a registered server's requests always carry, as a schema
 * that applies to every tool, so the editors offer them as fields with
 * their values even for a server whose tools were never discovered. */
export function serverArgumentSchemas(directory: ServerDirectoryEntry[]): ToolSchema[] {
  return directory
    .filter((entry) => (entry.arguments ?? []).length > 0)
    .map((entry) => ({
      name: entry.name,
      source: entry.source,
      everyTool: true,
      inputSchema: {
        type: "object",
        properties: Object.fromEntries(entry.arguments!.map((argument) => [
          argument.name,
          { type: "string", description: argument.description, ...(argument.values.length > 0 ? { enum: argument.values } : {}) },
        ])),
      },
    }));
}

/* The edition's centrally registered servers, when it has any; a
 * refused or failed read is an empty directory. */
function loadServerDirectory(): Promise<ServerDirectoryEntry[]> {
  if (!extensions.serverDirectory) return Promise.resolve([]);
  return extensions.serverDirectory().then((entries) => entries, () => []);
}

export interface PipelineSuggestions {
  toolSchemas: ToolSchema[];
  valueSuggestions: ConditionValueSuggestions;
  /* What the editor can say under a suggested server: its name and
   * source, for the edition's registered ones. */
  valueDetails: ConditionValueDetails;
  /* Argument fields the agents have actually sent, most common first. */
  argumentPaths: string[];
  /* The newest real request as the test sample, when there is one. */
  sample: RequestApprovalPipelinePreviewSample | null;
}

/* What the editors offer before the suggestions arrive. */
export const NO_SUGGESTIONS: PipelineSuggestions = { toolSchemas: [], valueSuggestions: {}, valueDetails: {}, argumentPaths: [], sample: null };

/* Optional, permission-scoped reads, all at once. Failure of one
 * directory must not prevent editing or discard the other sources. */
export async function loadPipelineSuggestions(): Promise<PipelineSuggestions> {
  const [agents, definitions, directory, requests] = await Promise.all([
    api.agents().then((page) => page.agents, () => []),
    api.toolDefinitions().catch(() => ({ tool_definitions: [], servers: [] })),
    loadServerDirectory(),
    loadRecentRequests(),
  ]);
  const tools = [
    ...definitions.tool_definitions.map((tool) => tool.name),
    ...requests.map((request) => request.tool),
  ];
  /* Registered servers first: a rule is usually written before the
   * first call through one. The catalogue names a source's server even
   * when discovery found no tools there. */
  const servers = [
    ...directory.map((entry) => entry.server),
    ...definitions.servers,
    ...requests.map((request) => request.server ?? ""),
  ].filter(Boolean);
  const newest = requests[0];
  return {
    toolSchemas: [...catalogSchemas(definitions.tool_definitions, requests), ...serverArgumentSchemas(directory)],
    valueSuggestions: mergeValueSuggestions({
      "/request/tool": tools,
      "/request/server": servers,
      "/agent/slug": agents.map((agent) => agent.slug),
      "/agent/name": agents.map((agent) => agent.name),
    }, ...requests.map((request) => sampleValueSuggestions(JSON.stringify({ request: { arguments: request.arguments, context: request.context } })))),
    valueDetails: {
      "/request/server": Object.fromEntries(directory.map((entry) => [entry.server, `${toolSourceLabel(entry.source)}: ${entry.name}`])),
    },
    argumentPaths: argumentPathSuggestions(requests),
    sample: newest ? sampleFromRequest(newest) : null,
  };
}

/*
 * The suggestions are the same for every pipeline and only the editors
 * and the test sample use them, so a pipeline page never waits for them:
 * they load when someone starts editing or testing (or opens the page
 * already editing) and arrive a moment later. One snapshot lives at
 * module scope for the person signed in, so moving between pipelines
 * offers it at once; an older snapshot is still offered while a fresh
 * one loads.
 */
const KEEP_MS = 60_000;

interface Snapshot {
  owner: string;
  at: number;
  value: PipelineSuggestions;
}

let snapshot: Snapshot | null = null;
let inflight: { owner: string; promise: Promise<void> } | null = null;
const listeners = new Set<() => void>();

function owner(): string {
  const principal = currentSession()?.principal;
  return principal ? `${principal.organization_id}:${principal.actor_id}` : "";
}

/* Starts a load unless a fresh snapshot or a load for the same person
 * is already in hand; settles when the snapshot is current. */
export function requestPipelineSuggestions(): Promise<void> {
  const who = owner();
  if (inflight?.owner === who) return inflight.promise;
  if (snapshot?.owner === who && Date.now() - snapshot.at < KEEP_MS) return Promise.resolve();
  const promise = loadPipelineSuggestions().then((value) => {
    /* A load for someone who has since signed out is dropped. */
    if (owner() !== who) return;
    snapshot = { owner: who, at: Date.now(), value };
    for (const listener of listeners) listener();
  }).finally(() => {
    if (inflight?.promise === promise) inflight = null;
  });
  inflight = { owner: who, promise };
  return promise;
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/* The signed-in person's suggestions, or null until they arrive. */
export function usePipelineSuggestions(): PipelineSuggestions | null {
  const current = useSyncExternalStore(subscribe, () => snapshot);
  return current?.owner === owner() ? current.value : null;
}

/* Tests start from an empty store. */
export function resetPipelineSuggestions(): void {
  snapshot = null;
  inflight = null;
}
