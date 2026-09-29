import { describe, expect, it } from "vitest";
import type { ConditionNode } from "@/ds/components/condition-builder";
import type {
  RequestApprovalBlockDefinition,
  StoredRequestApprovalBlock,
} from "../types";
import {
  definitionsFromStored,
  documentFromDrafts,
  draftsFromBlocks,
  duplicateDraft,
  newDraftBlock,
  validateDrafts,
  type BlockDraft,
  type BranchDraft,
} from "./pipeline-blocks";
import { pipelineReachability, walkDrafts } from "./pipeline-tree";
import { parseDocument } from "./pipeline-view";

function always(id = "remaining"): Extract<BlockDraft, { kind: "always" }> {
  const block = newDraftBlock("always");
  if (block.kind !== "always") throw new Error("expected an Always block");
  return { ...block, id, name: "Review remaining requests", reason: "Review everything else." };
}

describe("Always blocks", () => {
  it("defaults to human review and requires a name and reason", () => {
    expect(newDraftBlock("always")).toMatchObject({ kind: "always", outcome: "human", target: null });
    expect(validateDrafts([newDraftBlock("always")])).toContain("needs a name");
    expect(validateDrafts([{ ...always(), reason: "" }])).toContain("needs a reason");
    expect(validateDrafts([always()])).toBeNull();
  });

  it.each(["approve", "deny", "human"] as const)("round trips %s through the JSON editor without conditions", (outcome) => {
    const block = { ...always(), kind: "always" as const, outcome, when: undefined };
    const document = documentFromDrafts([block]);
    expect(document.blocks[0]).toMatchObject({ type: "always", config: { outcome, reason: block.reason }, timeout: "50ms", max_attempts: 1 });
    expect(document.blocks[0].config).not.toHaveProperty("when");
    expect(document.blocks[0].config).not.toHaveProperty("on_match");
    const parsed = parseDocument(JSON.stringify(document));
    expect(documentFromDrafts(draftsFromBlocks(parsed.blocks))).toEqual(document);
  });

  it("retains human reviewers and excludes them from automatic outcomes", () => {
    const block = { ...always(), kind: "always" as const, when: undefined, outcome: "human" as const, target: { type: "escalation_path" as const, id: "payments", name: "Payments" } };
    const document = documentFromDrafts([block]);
    const restored = draftsFromBlocks(document.blocks, [block.target])[0];
    expect(restored.kind === "always" ? restored.target : undefined).toEqual(block.target);
    expect(documentFromDrafts([{ ...block, outcome: "approve" }]).blocks[0].config).not.toHaveProperty("escalation_path");
    expect(duplicateDraft(block)).toMatchObject({ kind: "always", outcome: "human", target: block.target });
    expect(duplicateDraft(block).id).not.toBe(block.id);
  });

  it("rejects a nonterminal Always outcome in JSON mode", () => {
    const document = documentFromDrafts([always()]);
    const invalid = { ...document, blocks: [{ ...document.blocks[0], config: { outcome: "next", reason: "r" } }] };
    expect(() => parseDocument(JSON.stringify(invalid))).toThrow("config.outcome");
  });
});

/* A block of a kind another edition adds, as a stored revision holds it. */
const RISK_SCORE: StoredRequestApprovalBlock = {
  id: "risk",
  name: "Score refund risk",
  type: "risk_score",
  enabled: true,
  timeout: "10s",
  max_attempts: 2,
  config: { model: "refund-risk-v2", threshold: 0.8, reason: "Scored against the risk model.", escalation_path: "payments" },
  snapshot_id: "snapshot-risk",
  config_version: 1,
  content_hash: "hash-risk",
} as unknown as StoredRequestApprovalBlock;

describe("blocks of a kind this bundle does not know", () => {
  it("reads them read-only with what every block has, keeping the definition", () => {
    const [draft] = draftsFromBlocks([RISK_SCORE], [{ type: "escalation_path", id: "payments", name: "Payments" }]);
    expect(draft).toMatchObject({
      kind: "unknown",
      type: "risk_score",
      id: "risk",
      name: "Score refund risk",
      reason: "Scored against the risk model.",
      target: { type: "escalation_path", id: "payments", name: "Payments" },
    });
  });

  it("writes them back exactly as stored, without the snapshot fields", () => {
    const { snapshot_id: _snapshot, config_version: _version, content_hash: _hash, ...definition } = RISK_SCORE;
    const [draft] = draftsFromBlocks([RISK_SCORE]);
    expect(documentFromDrafts([draft]).blocks).toEqual([definition]);
    const copy = duplicateDraft(draft);
    expect(copy).toMatchObject({ kind: "unknown", type: "risk_score", name: "Score refund risk (copy)" });
    expect(documentFromDrafts([copy]).blocks[0]).toMatchObject({ ...definition, id: copy.id, name: copy.name });
  });

  it("refuses a revision that keeps one, and a JSON document that names one", () => {
    const [draft] = draftsFromBlocks([RISK_SCORE]);
    expect(validateDrafts([cel("first"), draft])).toBe(
      'Score refund risk is a "risk_score" block, which this deployment cannot run. Remove it to create a revision.',
    );
    expect(() => newDraftBlock("risk_score")).toThrow('No block kind "risk_score" is registered.');
    expect(() => parseDocument(JSON.stringify(documentFromDrafts([draft])))).toThrow(
      'Block 1 must have type "always", "branch", "cel", "delegate", or "webhook".',
    );
  });
});

describe("pipeline reachability", () => {
  const condition = { ...newDraftBlock(), id: "conditional" };
  const catchAll = always();
  const later = always("later");

  it("updates when Always is reordered or removed", () => {
    expect([...pipelineReachability([condition, catchAll, later]).blockedBy]).toEqual([["later", catchAll]]);
    expect([...pipelineReachability([catchAll, condition, later]).blockedBy]).toEqual([
      ["conditional", catchAll],
      ["later", catchAll],
    ]);
    expect(pipelineReachability([condition]).stoppedBy).toBeUndefined();
    expect(pipelineReachability([{ ...catchAll, enabled: false }, condition]).stoppedBy).toBeUndefined();
  });

  it("treats a delegate as a leaf that never stops the rail", () => {
    const delegate = { ...newDraftBlock("delegate"), id: "to-billing", pipelineKey: "billing" };
    const reachability = pipelineReachability([delegate, condition]);
    expect(reachability.blockedBy.size).toBe(0);
    expect(reachability.stoppedBy).toBeUndefined();
  });

  it("derives warnings from edited JSON", () => {
    const text = JSON.stringify(documentFromDrafts([catchAll, later]));
    const drafts = draftsFromBlocks(parseDocument(text).blocks);
    expect(pipelineReachability(drafts).blockedBy.get(drafts[1].id)?.id).toBe(catchAll.id);
  });
});

function leaf(id: string, field: string, value: unknown, operator = "equals"): ConditionNode {
  return { id, field, operator, value };
}

type CelDraft = Extract<BlockDraft, { kind: "cel" }>;

function cel(id: string, name = id): CelDraft {
  return {
    kind: "cel",
    id,
    name,
    enabled: true,
    outcome: "approve",
    when: { id: `${id}-when`, all: [leaf(`${id}-leaf`, "/request/arguments/amount_cents", 1000, "less_than")] },
    reason: "Small amounts are fine.",
    target: null,
  };
}

function webhook(id: string): BlockDraft {
  const draft = newDraftBlock("webhook");
  if (draft.kind !== "webhook") throw new Error("expected a webhook draft");
  return { ...draft, id, name: "Fraud", reason: "Fraud checked.", endpointKey: "fraud-service" };
}

function branch(id: string, blocks: BlockDraft[], name = id): BranchDraft {
  return {
    kind: "branch",
    id,
    name,
    enabled: true,
    when: { id: `${id}-when`, all: [leaf(`${id}-leaf`, "/request/tool", "stripe.refund")] },
    blocks,
    reason: "Refunds need a person when no check decides.",
    target: null,
  };
}

describe("branch blocks", () => {
  it("start empty with the synchronous envelope and a path the document always keeps", () => {
    const draft = newDraftBlock("branch");
    expect(draft).toMatchObject({ kind: "branch", blocks: [], target: null });
    const named: BranchDraft = {
      ...branch("refunds", [cel("small")]),
      target: { type: "escalation_path", id: "refund-reviewers", name: "Refund reviewers" },
    };
    const [definition] = documentFromDrafts([named]).blocks;
    expect(definition).toMatchObject({
      type: "branch",
      timeout: "50ms",
      max_attempts: 1,
      config: { reason: named.reason, escalation_path: "refund-reviewers" },
    });
    expect(Object.keys(definition.config)).toEqual(["when", "blocks", "reason", "escalation_path"]);
  });

  it("round trips a nested document through drafts and the JSON editor", () => {
    const document = documentFromDrafts([
      cel("first"),
      branch("refunds", [
        webhook("fraud"),
        branch("large", [always("large-human"), cel("never")]),
        cel("small"),
      ]),
      always("remaining"),
    ]);
    const parsed = parseDocument(JSON.stringify(document));
    expect(documentFromDrafts(draftsFromBlocks(parsed.blocks))).toEqual(document);
    const inner = document.blocks[1];
    expect(inner.type === "branch" && inner.config.blocks.map((block) => block.id)).toEqual(["fraud", "large", "small"]);
  });

  it("reads stored blocks at every depth and strips only their snapshot fields", () => {
    const stored = (definition: RequestApprovalBlockDefinition, id: string) => ({
      ...definition,
      snapshot_id: `snapshot-${id}`,
      config_version: 1 as const,
      content_hash: `hash-${id}`,
    });
    const fraud: RequestApprovalBlockDefinition = {
      id: "fraud",
      name: "Fraud service review",
      type: "webhook",
      enabled: true,
      timeout: "10s",
      max_attempts: 2,
      config: {
        endpoint_key: "fraud-service",
        allowed_outcomes: ["next", "human"],
        include: { agent_reasoning: true, context: false, previous_metadata: true },
        reason: "Fraud checked.",
      },
    };
    const [smallDefinition] = documentFromDrafts([cel("small")]).blocks;
    const refunds = documentFromDrafts([branch("refunds", [])]).blocks[0];
    const inner = documentFromDrafts([branch("inner", [])]).blocks[0];
    if (refunds.type !== "branch" || inner.type !== "branch") throw new Error("expected branches");
    const storedInner = {
      ...stored(inner, "inner"),
      config: { ...inner.config, blocks: [stored(fraud, "fraud")] },
    } as StoredRequestApprovalBlock;
    const storedRefunds = {
      ...stored(refunds, "refunds"),
      config: { ...refunds.config, blocks: [stored(smallDefinition, "small"), storedInner] },
    } as StoredRequestApprovalBlock;

    const definitions = definitionsFromStored([storedRefunds]);
    expect(definitions).toEqual([
      { ...refunds, config: { ...refunds.config, blocks: [smallDefinition, { ...inner, config: { ...inner.config, blocks: [fraud] } }] } },
    ]);
    expect(JSON.stringify(definitions)).not.toMatch(/snapshot_id|config_version|content_hash/);

    const drafts = draftsFromBlocks([storedRefunds]);
    expect(walkDrafts(drafts).map(({ block }) => `${block.kind}:${block.id}`)).toEqual([
      "branch:refunds",
      "cel:small",
      "branch:inner",
      "webhook:fraud",
    ]);
  });

  it("duplicates a branch deeply with fresh block and condition ids", () => {
    const quantified: CelDraft = {
      ...cel("labels"),
      when: {
        id: "labels-when",
        all: [{ id: "some", some: { field: "/request/arguments/labels", where: { id: "where", field: "/value", operator: "equals", value: "fraud" } } }],
      },
    };
    const original = branch("refunds", [cel("small"), branch("inner", [quantified])], "Refunds");
    const copy = duplicateDraft(original);
    if (copy.kind !== "branch") throw new Error("expected a branch");
    expect(copy.name).toBe("Refunds (copy)");
    expect(walkDrafts(copy.blocks).map(({ block }) => block.name)).toEqual(["small", "inner", "labels"]);

    const blockIds = (drafts: BlockDraft[]) => walkDrafts(drafts).map(({ block }) => block.id);
    const conditionIds = (node: ConditionNode): string[] => [
      node.id,
      ...(node.all ?? node.any ?? []).flatMap(conditionIds),
      ...(node.some ? conditionIds(node.some.where) : []),
      ...(node.every ? conditionIds(node.every.where) : []),
    ];
    const allConditionIds = (drafts: BlockDraft[]) =>
      walkDrafts(drafts).flatMap(({ block }) => (block.kind === "cel" || block.kind === "branch" ? conditionIds(block.when) : []));
    const before = new Set([...blockIds([original]), ...allConditionIds([original])]);
    const after = [...blockIds([copy]), ...allConditionIds([copy])];
    expect(after).toHaveLength(before.size);
    expect(after.filter((id) => before.has(id))).toEqual([]);
    expect(new Set(after).size).toBe(after.length);
  });

  it("gives a duplicated Conditions block its own condition ids", () => {
    const original = cel("small");
    const copy = duplicateDraft(original);
    if (copy.kind !== "cel" || original.kind !== "cel") throw new Error("expected Conditions blocks");
    expect(copy.when.id).not.toBe(original.when.id);
    expect(copy.when.all?.[0].id).not.toBe(original.when.all?.[0].id);
    expect(copy.when.all?.[0]).toMatchObject({ field: "/request/arguments/amount_cents", value: 1000 });
  });

  it("validates the whole tree with the labels the position chips show", () => {
    const refunds = branch("refunds", [cel("small"), { ...cel("unnamed"), name: "" }], "Refunds");
    expect(validateDrafts([cel("first"), refunds])).toBe("Block 2.2 needs a name.");
    const delegate = { ...newDraftBlock("delegate"), name: "Billing", when: { id: "dw", all: [leaf("dl", "/agent/slug", "billing")] } };
    if (delegate.kind !== "delegate") throw new Error("expected a delegate");
    expect(validateDrafts([delegate])).toBe("Billing needs a pipeline to hand requests to.");
    expect(validateDrafts([{ ...delegate, pipelineKey: "entry" }])).toBe("Billing cannot hand requests to the entry pipeline.");
    expect(validateDrafts([{ ...delegate, pipelineKey: "payments" }], { pipelineKey: "payments" })).toBe("Billing cannot hand requests to this pipeline itself.");
    expect(validateDrafts([{ ...delegate, pipelineKey: "payments" }], { pipelineKey: "entry" })).toBeNull();
    expect(validateDrafts([branch("empty", [], "Empty")])).toBe("Empty needs at least one block.");
    expect(validateDrafts([{ ...branch("refunds", [cel("small")], "Refunds"), reason: " " }])).toBe("Refunds needs a reason.");
    expect(validateDrafts([{ ...branch("refunds", [cel("small")], "Refunds"), when: { id: "w", all: [leaf("l", "", "x")] } }]))
      .toBe("Refunds has a condition without a field path.");
    expect(validateDrafts([branch("outer", [branch("inner", [{ ...cel("deep"), reason: "" }])])])).toBe("deep needs a reason.");
    expect(validateDrafts([cel("same"), branch("refunds", [cel("same")], "Refunds")])).toContain("reuses the block key “same”");
    expect(validateDrafts([cel("first"), branch("refunds", [cel("small")])])).toBeNull();
  });
});

describe("JSON documents with branches", () => {
  function text(blocks: unknown[]): string {
    return JSON.stringify({ blocks });
  }
  const refunds = documentFromDrafts([branch("refunds", [cel("small")])]).blocks[0];
  if (refunds.type !== "branch") throw new Error("expected a branch");

  it("names nested problems by their place in the document", () => {
    const missingWhen = { ...refunds, config: { ...refunds.config, blocks: [{ ...refunds.config.blocks[0], config: { on_match: "approve", reason: "r" } }] } };
    expect(() => parseDocument(text([documentFromDrafts([cel("first")]).blocks[0], missingWhen]))).toThrow("Block 2.1 is missing config.when.");
    const unknown = { ...refunds, config: { ...refunds.config, blocks: [{ id: "x", type: "group", config: {} }] } };
    expect(() => parseDocument(text([unknown]))).toThrow('Block 1.1 must have type "always", "branch", "cel", "delegate", or "webhook".');
    expect(() => parseDocument(text([{ ...refunds, config: { ...refunds.config, blocks: undefined } }]))).toThrow("Block 1 is missing config.blocks");
    expect(() => parseDocument(text([{ ...refunds, config: { ...refunds.config, when: undefined } }]))).toThrow("Block 1 is missing config.when.");
  });

  it("refuses a block key used twice anywhere in the tree", () => {
    const first = documentFromDrafts([cel("small")]).blocks[0];
    expect(() => parseDocument(text([first, refunds]))).toThrow("Block 2.1 reuses the key “small” of Block 1.");
    const twice = { ...refunds, config: { ...refunds.config, blocks: [refunds.config.blocks[0], refunds.config.blocks[0]] } };
    expect(() => parseDocument(text([twice]))).toThrow("Block 1.2 reuses the key “small” of Block 1.1.");
    expect(parseDocument(text([refunds])).blocks).toEqual([refunds]);
  });
});
