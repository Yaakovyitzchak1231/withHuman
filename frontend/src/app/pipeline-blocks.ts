import { FIXED_FIELDS, type ConditionNode } from "@/ds/components/condition-builder";
import type { BranchBlockDraft } from "@/ds/components/branch-block";
import type { DelegateBlockDraft } from "@/ds/components/delegate-block";
import type { OutcomeBlockDraft } from "@/ds/components/outcome-block";
import type { ReviewerTarget } from "@/ds/components/reviewer-target";
import type { WebhookBlockDraft } from "@/ds/components/webhook-block";
import type {
  ApprovalRequest,
  BranchBlockDefinition,
  DelegateBlockDefinition,
  OutcomeBlockDefinition,
  WebhookBlockDefinition,
  RequestApprovalBlockDefinition,
  RequestApprovalPipelineDocument,
  RequestApprovalPipelinePreviewSample,
  StoredRequestApprovalBlock,
} from "../types";
import { ENTRY_PIPELINE_KEY } from "../types";
import { api } from "../api";
import {
  blockKind,
  extensions,
  requireBlockKind,
  type BlockDraftBase,
  type EditionBlockDefinition,
  type EditionBlockDraft,
} from "./extensions";
import { positionLabel, walkDrafts } from "./pipeline-tree";

/*
 * Mapping between the editor's block drafts and the wire pipeline
 * document. Disabled blocks do not exist on the wire (the schema pins
 * enabled to true and disabled blocks are omitted), so drafts are
 * always enabled. Drafts are a discriminated union on kind so one rail
 * can hold every block type: core's own, a kind the edition registered
 * (kind "edition", read through the registry), and a kind this bundle
 * does not know (kind "unknown", shown read-only and written back as
 * stored). A branch draft holds drafts of its own, to any depth; a
 * delegate draft names another pipeline instead.
 */

export interface BranchDraft extends BranchBlockDraft<BlockDraft> {
  kind: "branch";
}

export interface DelegateDraft extends DelegateBlockDraft {
  kind: "delegate";
}

/* A stored block of a type no kind in this bundle knows: an edition's
 * kind read by the open edition, say. Core shows what every block has
 * and keeps the definition to write back unchanged. */
export interface UnknownBlockDraft extends BlockDraftBase {
  kind: "unknown";
  type: string;
  definition: EditionBlockDefinition;
}

export type BlockDraft =
  | ({ kind: "outcome" } & OutcomeBlockDraft)
  | ({ kind: "webhook" } & WebhookBlockDraft)
  | BranchDraft
  | DelegateDraft
  | EditionBlockDraft
  | UnknownBlockDraft;

/* The wire type a draft maps to. */
export function blockTypeOf(draft: BlockDraft): string {
  return draft.kind === "edition" || draft.kind === "unknown" ? draft.type : draft.kind;
}

/* Every block type this bundle can read: core's kinds and the
 * registered ones, in the order a message lists them. */
export function knownBlockTypes(): string[] {
  return ["branch", "delegate", "outcome", "webhook", ...extensions.blockKinds.map((kind) => kind.type)].sort();
}

/* Envelope defaults per core block type; the server enforces the
 * matching per-type limits. A registered kind carries its own. */
export const BLOCK_ENVELOPE_DEFAULTS = {
  outcome: { timeout: "50ms", max_attempts: 1 },
  webhook: { timeout: "5s", max_attempts: 3 },
  branch: { timeout: "50ms", max_attempts: 1 },
  delegate: { timeout: "50ms", max_attempts: 1 },
} as const;

/* The pipeline key grammar, shared with escalation path and agent
 * keys. "new" is the create page's route, never a key. */
export const PIPELINE_KEY_PATTERN = /^[a-z0-9][a-z0-9._-]{0,62}$/;
export const RESERVED_PIPELINE_KEY = "new";

export function validPipelineKey(key: string): boolean {
  return PIPELINE_KEY_PATTERN.test(key) && key !== RESERVED_PIPELINE_KEY;
}

/* A key suggested from a name: lowercase, runs of anything else
 * collapsed to one dash, trimmed to the pattern's length. */
export function suggestPipelineKey(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, "-")
    .replace(/^[^a-z0-9]+/, "")
    .replace(/-+$/, "")
    .slice(0, 63);
}

/* The organization's escalation paths as reviewer targets, the choices
 * every default and human-exit picker offers. Path details are loaded by
 * the shared reference hover card on demand. A failed read is an empty
 * directory: the keys still render as their own names. */
export async function loadPathDirectory(): Promise<ReviewerTarget[]> {
  const paths = await api.escalationPaths().catch(() => ({ escalation_paths: [] }));
  return paths.escalation_paths.map(
    (path): ReviewerTarget => ({
      type: "escalation_path",
      id: path.path_key,
      name: path.name,
      detail: path.active_revision ? undefined : "no active revision",
    }),
  );
}

/* An escalation path key as the editor's target, with its name looked
 * up in the directory of paths. A key the directory no longer knows
 * keeps the key as its name, so it stays visible rather than silently
 * vanishing. Asking the agent's owner makes the path the fallback of an
 * agent_owner target. */
export function pathTarget(
  key: string | undefined | null,
  directory: ReviewerTarget[],
  askAgentOwner?: boolean,
): ReviewerTarget | null {
  if (!key) return null;
  const known = directory.find((entry) => entry.type === "escalation_path" && entry.id === key);
  const path = known ?? { type: "escalation_path" as const, id: key, name: key };
  return askAgentOwner ? { type: "agent_owner", id: path.id, name: path.name } : path;
}

/* The path a target names: the path itself, or an owner route's
 * fallback. */
export function pathKeyOf(target: ReviewerTarget | null | undefined): string | undefined {
  return target?.type === "escalation_path" || target?.type === "agent_owner" ? target.id : undefined;
}

/* A route as one comparable string: the path, and whether the owner is
 * asked first. */
export function routeKeyOf(target: ReviewerTarget | null | undefined): string {
  return `${asksAgentOwner(target) ? "owner:" : ""}${pathKeyOf(target) ?? ""}`;
}

/* Whether a target asks the agent's owner first. */
export function asksAgentOwner(target: ReviewerTarget | null | undefined): boolean {
  return target?.type === "agent_owner";
}

/* The route keys a human exit's config carries for target. */
function routeConfig(target: ReviewerTarget | null | undefined): { escalation_path?: string; ask_agent_owner?: true } {
  const key = pathKeyOf(target);
  if (!key) return {};
  return asksAgentOwner(target) ? { escalation_path: key, ask_agent_owner: true } : { escalation_path: key };
}

/* A kind the switch does not know: the union grew and this mapping did
 * not. The compiler catches it. */
function unknownVariant(value: never): never {
  const tag = value as { kind?: unknown };
  throw new Error(`Unknown block kind ${String(tag.kind)}.`);
}

/* The builder edits groups; a bare leaf or quantifier root is wrapped in
 * an implicit all so it renders as one row. */
function editableCondition(when: Record<string, unknown>): ConditionNode {
  const node = when as unknown as ConditionNode;
  return node.all || node.any ? node : { id: crypto.randomUUID(), all: [node] };
}

export function draftsFromBlocks(
  blocks: (RequestApprovalBlockDefinition | StoredRequestApprovalBlock)[],
  directory: ReviewerTarget[] = [],
): BlockDraft[] {
  return blocks.map((block) => draftFromBlock(block, directory));
}

/* A block of a kind core's wire union does not name, as it arrived:
 * only the definition fields, whether it came stored or not. */
function editionDefinition(block: RequestApprovalBlockDefinition | StoredRequestApprovalBlock): EditionBlockDefinition {
  const { snapshot_id: _snapshot, config_version: _version, content_hash: _hash, ...definition } =
    block as unknown as EditionBlockDefinition & Partial<Pick<StoredRequestApprovalBlock, "snapshot_id" | "config_version" | "content_hash">>;
  return definition;
}

function draftFromBlock(
  block: RequestApprovalBlockDefinition | StoredRequestApprovalBlock,
  directory: ReviewerTarget[],
): BlockDraft {
  if (block.type === "delegate") {
    return {
      kind: "delegate",
      id: block.id,
      name: block.name,
      enabled: true,
      when: editableCondition(block.config.when),
      pipelineKey: block.config.pipeline_key,
    };
  }
  const config = block.config as { escalation_path?: string; ask_agent_owner?: boolean };
  const target = pathTarget(config.escalation_path, directory, config.ask_agent_owner);
  switch (block.type) {
    case "outcome":
      return { kind: "outcome", id: block.id, name: block.name, enabled: true,
        outcome: block.config.outcome, reason: block.config.reason, target };
    case "webhook":
      return {
        kind: "webhook",
        id: block.id,
        name: block.name,
        enabled: true,
        endpointKey: block.config.endpoint_key,
        allowedOutcomes: [...block.config.allowed_outcomes],
        timeout: block.timeout,
        maxAttempts: block.max_attempts,
        include: {
          agentReasoning: block.config.include.agent_reasoning,
          context: block.config.include.context,
          previousMetadata: block.config.include.previous_metadata,
        },
        reason: block.config.reason,
        target,
      };
    case "branch":
      return {
        kind: "branch",
        id: block.id,
        name: block.name,
        enabled: true,
        when: editableCondition(block.config.when),
        blocks: draftsFromBlocks(block.config.blocks, directory),
        reason: block.config.reason,
        target,
      };
    default: {
      /* A kind the edition registered, else one this bundle cannot edit. */
      const definition = editionDefinition(block);
      const kind = blockKind(definition.type);
      if (kind) return kind.fromDefinition(definition, target);
      return {
        kind: "unknown",
        type: definition.type,
        id: definition.id,
        name: definition.name,
        enabled: true,
        reason: typeof definition.config.reason === "string" ? definition.config.reason : "",
        target,
        definition,
      };
    }
  }
}

function definitionFromDraft(draft: BlockDraft): RequestApprovalBlockDefinition {
  if (draft.kind === "delegate") {
    const definition: DelegateBlockDefinition = {
      id: draft.id,
      name: draft.name,
      type: "delegate",
      enabled: true,
      timeout: BLOCK_ENVELOPE_DEFAULTS.delegate.timeout,
      max_attempts: BLOCK_ENVELOPE_DEFAULTS.delegate.max_attempts,
      config: {
        when: draft.when as unknown as Record<string, unknown>,
        pipeline_key: draft.pipelineKey,
      },
    };
    return definition;
  }
  const escalationPath = pathKeyOf(draft.target);
  const route = routeConfig(draft.target);
  switch (draft.kind) {
    case "outcome": {
      const definition: OutcomeBlockDefinition = {
        id: draft.id,
        name: draft.name,
        type: "outcome",
        enabled: true,
        ...BLOCK_ENVELOPE_DEFAULTS.outcome,
        config: {
          outcome: draft.outcome,
          reason: draft.reason,
          /* Only a human outcome has a "where"; the server rejects a path
           * on an automatic one. */
          ...(draft.outcome === "human" ? route : {}),
        },
      };
      return definition;
    }
    case "webhook": {
      const definition: WebhookBlockDefinition = {
        id: draft.id,
        name: draft.name,
        type: "webhook",
        enabled: true,
        timeout: draft.timeout,
        max_attempts: draft.maxAttempts,
        config: {
          endpoint_key: draft.endpointKey,
          allowed_outcomes: [...draft.allowedOutcomes],
          include: {
            agent_reasoning: draft.include.agentReasoning,
            context: draft.include.context,
            previous_metadata: draft.include.previousMetadata,
          },
          reason: draft.reason,
          ...route,
        },
      };
      return definition;
    }
    case "branch": {
      /* A branch's path names who takes its end, whatever the blocks
       * inside do, so it is always allowed. */
      const definition: BranchBlockDefinition = {
        id: draft.id,
        name: draft.name,
        type: "branch",
        enabled: true,
        timeout: BLOCK_ENVELOPE_DEFAULTS.branch.timeout,
        max_attempts: BLOCK_ENVELOPE_DEFAULTS.branch.max_attempts,
        config: {
          when: draft.when as unknown as Record<string, unknown>,
          blocks: draft.blocks.map(definitionFromDraft),
          reason: draft.reason,
          ...route,
        },
      };
      return definition;
    }
    case "edition": {
      /* The kind writes its own definition; the document's array is
       * typed as core's kinds, which is where it rides along. Asking the
       * agent's owner rides beside the path the kind wrote. */
      const definition = requireBlockKind(draft.type).toDefinition(draft, escalationPath);
      if (asksAgentOwner(draft.target) && definition.config.escalation_path) {
        definition.config = { ...definition.config, ask_agent_owner: true };
      }
      return definition as unknown as RequestApprovalBlockDefinition;
    }
    case "unknown":
      return { ...draft.definition, id: draft.id, name: draft.name } as unknown as RequestApprovalBlockDefinition;
    default:
      return unknownVariant(draft);
  }
}

export function documentFromDrafts(
  drafts: BlockDraft[],
  defaultPath: ReviewerTarget | null = null,
): RequestApprovalPipelineDocument {
  const key = pathKeyOf(defaultPath);
  return {
    blocks: drafts.map(definitionFromDraft),
    ...(key ? { default_escalation_path: key } : {}),
    ...(key && asksAgentOwner(defaultPath) ? { default_ask_agent_owner: true } : {}),
  };
}

/* A stored revision's blocks as a document to save again, exactly as
 * they were: only the snapshot fields go, at every depth. Unlike a trip
 * through drafts, nothing the editor does not model (an Outcome block's
 * timeout, say) is reset to a default. */
export function definitionsFromStored(blocks: StoredRequestApprovalBlock[]): RequestApprovalBlockDefinition[] {
  return blocks.map((block): RequestApprovalBlockDefinition => {
    const { snapshot_id: _snapshot, config_version: _version, content_hash: _hash, ...definition } = block;
    if (definition.type === "branch") {
      return { ...definition, config: { ...definition.config, blocks: definitionsFromStored(definition.config.blocks) } };
    }
    return definition;
  });
}

/* Slug-shaped and unique from birth, so keys, previews, and the stored
 * document all agree without a rename step. */
function newBlockId(): string {
  return `block-${crypto.randomUUID().slice(0, 8)}`;
}

function newCondition(): ConditionNode {
  return {
    id: crypto.randomUUID(),
    all: [{ id: crypto.randomUUID(), field: "", operator: "equals", value: "" }],
  };
}

/* A new block of a type: core's own, or a registered kind's. */
export function newDraftBlock(type: string): BlockDraft {
  switch (type) {
    case "outcome":
      return { kind: "outcome", id: newBlockId(), name: "", enabled: true,
        outcome: "human", reason: "", target: null };
    case "webhook":
      return {
        kind: "webhook",
        id: newBlockId(),
        name: "",
        enabled: true,
        endpointKey: "",
        /* Enrichment or escalation only until an administrator grants
         * automatic outcomes. */
        allowedOutcomes: ["next", "human"],
        timeout: BLOCK_ENVELOPE_DEFAULTS.webhook.timeout,
        maxAttempts: BLOCK_ENVELOPE_DEFAULTS.webhook.max_attempts,
        include: { agentReasoning: false, context: false, previousMetadata: true },
        reason: "",
        target: null,
      };
    case "branch":
      /* Empty until its first block is added; saving refuses an empty
       * branch. */
      return {
        kind: "branch",
        id: newBlockId(),
        name: "",
        enabled: true,
        when: newCondition(),
        blocks: [],
        reason: "",
        target: null,
      };
    case "delegate":
      /* No target until one is picked; saving refuses it. */
      return {
        kind: "delegate",
        id: newBlockId(),
        name: "",
        enabled: true,
        when: newCondition(),
        pipelineKey: "",
      };
    default:
      return requireBlockKind(type).draft(newBlockId());
  }
}

/* A condition tree with fresh ids throughout, quantifier bodies
 * included: condition ids are unique across the whole pipeline. */
function copyCondition(node: ConditionNode): ConditionNode {
  const copy: ConditionNode = { ...node, id: crypto.randomUUID() };
  if (Array.isArray(node.value)) copy.value = [...node.value];
  if (node.all) copy.all = node.all.map(copyCondition);
  if (node.any) copy.any = node.any.map(copyCondition);
  if (node.some) copy.some = { ...node.some, where: copyCondition(node.some.where) };
  if (node.every) copy.every = { ...node.every, where: copyCondition(node.every.where) };
  return copy;
}

/* A deep copy with a fresh block id, and fresh ids for every block and
 * condition inside it, so nothing is shared with the original. A
 * registered kind's draft is copied shallowly: its editor replaces what
 * it changes and never mutates a draft in place. */
function copyDraft(draft: BlockDraft): BlockDraft {
  const id = newBlockId();
  switch (draft.kind) {
    case "outcome":
    case "edition":
    case "unknown":
      return { ...draft, id };
    case "webhook":
      return { ...draft, id, allowedOutcomes: [...draft.allowedOutcomes], include: { ...draft.include } };
    case "branch":
      return { ...draft, id, when: copyCondition(draft.when), blocks: draft.blocks.map(copyDraft) };
    case "delegate":
      return { ...draft, id, when: copyCondition(draft.when) };
    default:
      return unknownVariant(draft);
  }
}

/* A copy of a draft for the duplicate action: the whole subtree is new,
 * and only the copy itself is named as one. */
export function duplicateDraft(draft: BlockDraft): BlockDraft {
  return { ...copyDraft(draft), name: `${draft.name} (copy)` };
}

function conditionProblem(node: ConditionNode, blockName: string): string | null {
  if (node.all || node.any) {
    for (const child of node.all ?? node.any ?? []) {
      const problem = conditionProblem(child, blockName);
      if (problem) return problem;
    }
    return null;
  }
  if (node.some || node.every) return null;
  const field = (node.field ?? "").trim();
  if (!field.startsWith("/") || field.length < 2) {
    return `${blockName} has a condition without a field path.`;
  }
  return null;
}

/* First client-side problem that would make the server reject the
 * document, phrased for a toast; null when the document can ship. The
 * whole tree is checked in execution order, and an unnamed block is
 * called by its position chip ("Block 3.2"). pipelineKey is the
 * pipeline being edited, which a delegate may not name. */
export function validateDrafts(drafts: BlockDraft[], { pipelineKey = "" }: { pipelineKey?: string } = {}): string | null {
  const keys = new Set<string>();
  for (const { block, path } of walkDrafts(drafts)) {
    const position = `Block ${positionLabel(path)}`;
    const label = block.name.trim() || position;
    if (keys.has(block.id)) {
      return `${label} reuses the block key “${block.id}”. Every block needs its own key, including blocks inside branches.`;
    }
    keys.add(block.id);
    if (!block.name.trim()) return `${position} needs a name.`;
    if (block.kind === "delegate") {
      const problem = conditionProblem(block.when, label);
      if (problem) return problem;
      if (!block.pipelineKey) return `${label} needs a pipeline to hand requests to.`;
      if (block.pipelineKey === ENTRY_PIPELINE_KEY) return `${label} cannot hand requests to the entry pipeline.`;
      if (block.pipelineKey === pipelineKey) return `${label} cannot hand requests to this pipeline itself.`;
      continue;
    }
    /* A branch's reason is optional: without one the server sends a
     * generated explanation. */
    if (block.kind !== "branch" && !block.reason.trim()) return `${label} needs a reason.`;
    switch (block.kind) {
      case "outcome":
        break;
      case "webhook":
        if (!block.endpointKey) return `${label} needs an endpoint.`;
        if (block.allowedOutcomes.length === 0) return `${label} needs at least one allowed outcome.`;
        break;
      case "branch": {
        const problem = conditionProblem(block.when, label);
        if (problem) return problem;
        if (block.blocks.length === 0) return `${label} needs at least one block.`;
        break;
      }
      case "edition": {
        const problem = requireBlockKind(block.type).validate(block, label);
        if (problem) return problem;
        break;
      }
      case "unknown":
        return `${label} is a "${block.type}" block, which this deployment cannot run. Remove it to create a revision.`;
      default:
        return unknownVariant(block);
    }
  }
  return null;
}

/* The placeholder sample, naming an example agent by slug. */
export function defaultSample(agentSlug = "example"): RequestApprovalPipelinePreviewSample {
  return {
    request: {
      tool: "stripe.refund",
      arguments: {
        amount_cents: 900,
        customer: { email: "buyer@example.com", labels: ["verified"] },
      },
      agent_reasoning: "The customer was charged twice for the same order.",
    },
    agent: { slug: agentSlug, name: "Preview agent" },
  };
}

/* A real request as the test sample: the tool and arguments as sent, the
 * agent that sent them. The reasoning and context ride along when the
 * agent gave them, because blocks that share the request with a service
 * or a model read them. */
export function sampleFromRequest(
  request: Pick<ApprovalRequest, "tool" | "server" | "arguments" | "agent_reasoning" | "context" | "agent_slug" | "agent_name">,
): RequestApprovalPipelinePreviewSample {
  const record = (value: unknown): Record<string, unknown> | undefined =>
    value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
  const context = record(request.context);
  return {
    request: {
      tool: request.tool,
      ...(request.server ? { server: request.server } : {}),
      arguments: record(request.arguments) ?? {},
      ...(request.agent_reasoning ? { agent_reasoning: request.agent_reasoning } : {}),
      ...(context ? { context } : {}),
    },
    agent: { slug: request.agent_slug, name: request.agent_name },
  };
}

/* Every key of an arguments object as a JSON Pointer, nested objects
 * included, in document order. Arrays are values, not paths. */
export function argumentPaths(value: unknown): string[] {
  const found: string[] = [];
  const walk = (node: unknown, prefix: string) => {
    if (!node || typeof node !== "object" || Array.isArray(node)) return;
    for (const [key, child] of Object.entries(node)) {
      const path = `${prefix}/${key.replace(/~/g, "~0").replace(/\//g, "~1")}`;
      found.push(path);
      walk(child, path);
    }
  };
  walk(value, "/request/arguments");
  return found;
}

/* Candidate field paths learned from the sample, plus the fixed fields
 * the validator always allows. */
export function suggestPaths(sampleText: string): string[] {
  const fixed = FIXED_FIELDS.map((field) => field.path);
  try {
    const sample = JSON.parse(sampleText) as {
      request?: { arguments?: unknown };
    };
    return [...fixed, ...argumentPaths(sample.request?.arguments)];
  } catch {
    return fixed;
  }
}

/*
 * A sample handed from the overview to a pipeline view: the overview's
 * Test request opens the entry pipeline, which picks the sample up on
 * mount and replays it on its rail. Held in module state because the
 * sample is transient and never belongs in the address.
 */
let handedOffSample: { pipelineKey: string; text: string } | null = null;

export function handOffTestSample(pipelineKey: string, text: string): void {
  handedOffSample = { pipelineKey, text };
}

/* The sample handed to this pipeline, or null. Taking it clears it. */
export function takeTestSample(pipelineKey: string): string | null {
  if (!handedOffSample || handedOffSample.pipelineKey !== pipelineKey) return null;
  const { text } = handedOffSample;
  handedOffSample = null;
  return text;
}
