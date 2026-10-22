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

type OutcomeDraft = Extract<BlockDraft, { kind: "outcome" }>;

function outcome(id = "remaining"): OutcomeDraft {
  const block = newDraftBlock("outcome");
  if (block.kind !== "outcome") throw new Error("expected an Outcome block");
  return { ...block, id, name: "Review remaining requests", reason: "Review everything else." };
}

/* An Outcome block that approves, the usual body of a gate. */
function approve(id: string, name = id): OutcomeDraft {
  return { ...outcome(id), name, outcome: "approve", reason: "Small amounts are fine." };
}

describe("Outcome blocks", () => {
  it("defaults to human review and requires a name and reason", () => {
    expect(newDraftBlock("outcome")).toMatchObject({ kind: "outcome", outcome: "human", target: null });
    expect(validateDrafts([newDraftBlock("outcome")])).toContain("needs a name");
    expect(validateDrafts([{ ...outcome(), reason: "" }])).toContain("needs a reason");
    expect(validateDrafts([outcome()])).toBeNull();
  });

  it.each(["approve", "deny", "human"] as const)("round trips %s through the JSON editor without conditions", (decision) => {
    const block = { ...outcome(), outcome: decision };
    const document = documentFromDrafts([block]);
    expect(document.blocks[0]).toMatchObject({ type: "outcome", config: { outcome: decision, reason: block.reason }, timeout: "50ms", max_attempts: 1 });
    expect(document.blocks[0].config).not.toHaveProperty("when");
    const parsed = parseDocument(JSON.stringify(document));
    expect(documentFromDrafts(draftsFromBlocks(parsed.blocks))).toEqual(document);
  });

  it("retains human reviewers and excludes them from automatic outcomes", () => {
    const block = { ...outcome(), outcome: "human" as const, target: { type: "escalation_path" as const, id: "payments", name: "Payments" } };
    const document = documentFromDrafts([block]);
    const restored = draftsFromBlocks(document.blocks, [block.target])[0];
    expect(restored.kind === "outcome" ? restored.target : undefined).toEqual(block.target);
    expect(documentFromDrafts([{ ...block, outcome: "approve" }]).blocks[0].config).not.toHaveProperty("escalation_path");
    expect(duplicateDraft(block)).toMatchObject({ kind: "outcome", outcome: "human", target: block.target });
    expect(duplicateDraft(block).id).not.toBe(block.id);
  });

  it("rejects a nonterminal outcome in JSON mode", () => {
    const document = documentFromDrafts([outcome()]);
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
    expect(validateDrafts([approve("first"), draft])).toBe(
      'Score refund risk is a "risk_score" block, which this deployment cannot run. Remove it to create a revision.',
    );
    expect(() => newDraftBlock("risk_score")).toThrow('No block kind "risk_score" is registered.');
    expect(() => parseDocument(JSON.stringify(documentFromDrafts([draft])))).toThrow(
      'Block 1 must have type "branch", "delegate", "outcome", or "webhook".',
    );
  });
});

describe("pipeline reachability", () => {
  const inner = approve("conditional-approve");
  const condition = branch("conditional", [inner]);
  const catchAll = outcome();
  const later = outcome("later");

  it("updates when an Outcome block is reordered or removed", () => {
    expect([...pipelineReachability([condition, catchAll, later]).blockedBy]).toEqual([["later", catchAll]]);
    expect([...pipelineReachability([catchAll, condition, later]).blockedBy]).toEqual([
      ["conditional", catchAll],
      ["conditional-approve", catchAll],
      ["later", catchAll],
    ]);
    expect(pipelineReachability([condition]).stoppedBy).toBeUndefined();
    expect(pipelineReachability([{ ...catchAll, enabled: false }, condition]).stoppedBy).toBeUndefined();
  });

  it("leaves a gate's end unreached without stopping the rail", () => {
    const reachability = pipelineReachability([condition, later]);
    expect([...reachability.endBlockedBy]).toEqual([["conditional", inner]]);
    expect(reachability.blockedBy.size).toBe(0);
    expect(reachability.stoppedBy).toBe(later);
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
      ...branch("refunds", [approve("small")]),
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
      approve("first"),
      branch("refunds", [
        webhook("fraud"),
        branch("large", [outcome("large-human"), approve("never")]),
        approve("small"),
      ]),
      outcome("remaining"),
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
    const [smallDefinition] = documentFromDrafts([approve("small")]).blocks;
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
      "outcome:small",
      "branch:inner",
      "webhook:fraud",
    ]);
  });

  it("duplicates a branch deeply with fresh block and condition ids", () => {
    const quantified: BranchDraft = {
      ...branch("labels", [approve("labels-approve")]),
      when: {
        id: "labels-when",
        all: [{ id: "some", some: { field: "/request/arguments/labels", where: { id: "where", field: "/value", operator: "equals", value: "fraud" } } }],
      },
    };
    const original = branch("refunds", [approve("small"), branch("inner", [quantified])], "Refunds");
    const copy = duplicateDraft(original);
    if (copy.kind !== "branch") throw new Error("expected a branch");
    expect(copy.name).toBe("Refunds (copy)");
    expect(walkDrafts(copy.blocks).map(({ block }) => block.name)).toEqual(["small", "inner", "labels", "labels-approve"]);

    const blockIds = (drafts: BlockDraft[]) => walkDrafts(drafts).map(({ block }) => block.id);
    const conditionIds = (node: ConditionNode): string[] => [
      node.id,
      ...(node.all ?? node.any ?? []).flatMap(conditionIds),
      ...(node.some ? conditionIds(node.some.where) : []),
      ...(node.every ? conditionIds(node.every.where) : []),
    ];
    const allConditionIds = (drafts: BlockDraft[]) =>
      walkDrafts(drafts).flatMap(({ block }) => (block.kind === "branch" || block.kind === "delegate" ? conditionIds(block.when) : []));
    const before = new Set([...blockIds([original]), ...allConditionIds([original])]);
    const after = [...blockIds([copy]), ...allConditionIds([copy])];
    expect(after).toHaveLength(before.size);
    expect(after.filter((id) => before.has(id))).toEqual([]);
    expect(new Set(after).size).toBe(after.length);
  });

  it("gives a duplicated delegate its own condition ids", () => {
    const original = newDraftBlock("delegate");
    if (original.kind !== "delegate") throw new Error("expected a delegate");
    const delegate = { ...original, when: { id: "dw", all: [leaf("dl", "/request/arguments/amount_cents", 1000, "less_than")] } };
    const copy = duplicateDraft(delegate);
    if (copy.kind !== "delegate") throw new Error("expected a delegate");
    expect(copy.when.id).not.toBe(delegate.when.id);
    expect(copy.when.all?.[0].id).not.toBe(delegate.when.all?.[0].id);
    expect(copy.when.all?.[0]).toMatchObject({ field: "/request/arguments/amount_cents", value: 1000 });
  });

  it("validates the whole tree with the labels the position chips show", () => {
    const refunds = branch("refunds", [approve("small"), { ...approve("unnamed"), name: "" }], "Refunds");
    expect(validateDrafts([approve("first"), refunds])).toBe("Block 2.2 needs a name.");
    const delegate = { ...newDraftBlock("delegate"), name: "Billing", when: { id: "dw", all: [leaf("dl", "/agent/slug", "billing")] } };
    if (delegate.kind !== "delegate") throw new Error("expected a delegate");
    expect(validateDrafts([delegate])).toBe("Billing needs a pipeline to hand requests to.");
    expect(validateDrafts([{ ...delegate, pipelineKey: "entry" }])).toBe("Billing cannot hand requests to the entry pipeline.");
    expect(validateDrafts([{ ...delegate, pipelineKey: "payments" }], { pipelineKey: "payments" })).toBe("Billing cannot hand requests to this pipeline itself.");
    expect(validateDrafts([{ ...delegate, pipelineKey: "payments" }], { pipelineKey: "entry" })).toBeNull();
    expect(validateDrafts([branch("empty", [], "Empty")])).toBe("The branch Empty needs at least one block under it.");
    /* A branch's reason is optional; the server generates one. */
    expect(validateDrafts([{ ...branch("refunds", [approve("small")], "Refunds"), reason: " " }])).toBeNull();
    expect(validateDrafts([{ ...branch("refunds", [approve("small")], "Refunds"), when: { id: "w", all: [leaf("l", "", "x")] } }]))
      .toBe("Refunds has a condition without a field path.");
    expect(validateDrafts([branch("outer", [branch("inner", [{ ...approve("deep"), reason: "" }])])])).toBe("deep needs a reason.");
    expect(validateDrafts([approve("same"), branch("refunds", [approve("same")], "Refunds")])).toContain("reuses the block key “same”");
    expect(validateDrafts([approve("first"), branch("refunds", [approve("small")])])).toBeNull();
  });
});

describe("JSON documents with branches", () => {
  function text(blocks: unknown[]): string {
    return JSON.stringify({ blocks });
  }
  const refunds = documentFromDrafts([branch("refunds", [approve("small")])]).blocks[0];
  if (refunds.type !== "branch") throw new Error("expected a branch");

  it("names nested problems by their place in the document", () => {
    const missingOutcome = { ...refunds, config: { ...refunds.config, blocks: [{ ...refunds.config.blocks[0], config: { reason: "r" } }] } };
    expect(() => parseDocument(text([documentFromDrafts([approve("first")]).blocks[0], missingOutcome]))).toThrow("Block 2.1 needs config.outcome");
    const unknown = { ...refunds, config: { ...refunds.config, blocks: [{ id: "x", type: "group", config: {} }] } };
    expect(() => parseDocument(text([unknown]))).toThrow('Block 1.1 must have type "branch", "delegate", "outcome", or "webhook".');
    expect(() => parseDocument(text([{ ...refunds, config: { ...refunds.config, blocks: undefined } }]))).toThrow("Block 1 is missing config.blocks");
    expect(() => parseDocument(text([{ ...refunds, config: { ...refunds.config, when: undefined } }]))).toThrow("Block 1 is missing config.when.");
  });

  it("refuses a block key used twice anywhere in the tree", () => {
    const first = documentFromDrafts([approve("small")]).blocks[0];
    expect(() => parseDocument(text([first, refunds]))).toThrow("Block 2.1 reuses the key “small” of Block 1.");
    const twice = { ...refunds, config: { ...refunds.config, blocks: [refunds.config.blocks[0], refunds.config.blocks[0]] } };
    expect(() => parseDocument(text([twice]))).toThrow("Block 1.2 reuses the key “small” of Block 1.1.");
    expect(parseDocument(text([refunds])).blocks).toEqual([refunds]);
  });
});

describe("the agent-owner route", () => {
  const payments = { type: "escalation_path" as const, id: "payments", name: "Payments" };
  const ownerRoute = { type: "agent_owner" as const, id: "payments", name: "Payments" };

  it("writes ask_agent_owner beside its fallback path and reads it back", () => {
    const block = { ...outcome(), outcome: "human" as const, target: ownerRoute };
    const document = documentFromDrafts([block], ownerRoute);
    expect(document.blocks[0].config).toMatchObject({ escalation_path: "payments", ask_agent_owner: true });
    expect(document).toMatchObject({ default_escalation_path: "payments", default_ask_agent_owner: true });
    const restored = draftsFromBlocks(document.blocks, [payments])[0];
    expect(restored.kind === "outcome" ? restored.target : undefined).toEqual(ownerRoute);
    const parsed = parseDocument(JSON.stringify(document));
    expect(parsed.default_ask_agent_owner).toBe(true);
    expect(documentFromDrafts(draftsFromBlocks(parsed.blocks, [payments]), ownerRoute)).toEqual(document);
  });

  it("leaves a plain path without the flag", () => {
    const document = documentFromDrafts([{ ...outcome(), outcome: "human" as const, target: payments }], payments);
    expect(document.blocks[0].config).not.toHaveProperty("ask_agent_owner");
    expect(document).not.toHaveProperty("default_ask_agent_owner");
  });

  it("asks the owner without a path, falling back to the pipeline default", () => {
    const unpathed = { type: "agent_owner" as const, id: "", name: "" };
    const block = { ...outcome(), outcome: "human" as const, target: unpathed };
    const document = documentFromDrafts([block], unpathed);
    expect(document.blocks[0].config).toMatchObject({ ask_agent_owner: true });
    expect(document.blocks[0].config).not.toHaveProperty("escalation_path");
    expect(document).toMatchObject({ default_ask_agent_owner: true });
    expect(document).not.toHaveProperty("default_escalation_path");
    const restored = draftsFromBlocks(document.blocks, [payments])[0];
    expect(restored.kind === "outcome" ? restored.target : undefined).toEqual(unpathed);
    const parsed = parseDocument(JSON.stringify({ blocks: [], default_ask_agent_owner: true }));
    expect(parsed.default_ask_agent_owner).toBe(true);
  });
});
