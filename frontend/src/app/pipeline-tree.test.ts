import { describe, expect, it } from "vitest";
import type { BlockDraft, BranchDraft } from "./pipeline-blocks";
import {
  allConditions,
  appendToRail,
  changedBlockCount,
  countDrafts,
  findDraft,
  insertAfter,
  moveIntoBranchAbove,
  moveOutOfBranch,
  pipelineReachability,
  positionLabel,
  railOf,
  removeDraft,
  reorderRail,
  unwrapBranch,
  updateDraft,
  walkDrafts,
} from "./pipeline-tree";

function cel(id: string): BlockDraft {
  return {
    kind: "cel",
    id,
    name: id,
    enabled: true,
    outcome: "approve",
    when: { id: `${id}-when`, all: [{ id: `${id}-leaf`, field: "/request/arguments/amount_cents", operator: "less_than", value: 1000 }] },
    reason: "Small amounts are fine.",
    target: null,
  };
}

function always(id: string): BlockDraft {
  return { kind: "always", id, name: id, enabled: true, outcome: "human", reason: "Everything else waits.", target: null };
}

function branch(id: string, blocks: BlockDraft[]): BranchDraft {
  return {
    kind: "branch",
    id,
    name: id,
    enabled: true,
    when: { id: `${id}-when`, all: [{ id: `${id}-leaf`, field: "/request/tool", operator: "equals", value: "stripe.refund" }] },
    blocks,
    reason: "Refunds need a person when no check decides.",
    target: null,
  };
}

/* [a, refunds [small, large [huge], tiny], b] */
function tree(): BlockDraft[] {
  return [cel("a"), branch("refunds", [cel("small"), branch("large", [cel("huge")]), cel("tiny")]), cel("b")];
}

const ids = (drafts: BlockDraft[]) => walkDrafts(drafts).map(({ block }) => block.id);

/* The tree as nested ids, so a structural change reads at a glance. */
function shape(drafts: BlockDraft[]): unknown[] {
  return drafts.map((block) => (block.kind === "branch" ? { [block.id]: shape(block.blocks) } : block.id));
}

describe("walking the tree", () => {
  it("visits blocks in pre-order with their positions, parents and ancestors", () => {
    const visits = walkDrafts(tree());
    expect(visits.map(({ block, path, parentId }) => [block.id, path.join("."), parentId])).toEqual([
      ["a", "1", null],
      ["refunds", "2", null],
      ["small", "2.1", "refunds"],
      ["large", "2.2", "refunds"],
      ["huge", "2.2.1", "large"],
      ["tiny", "2.3", "refunds"],
      ["b", "3", null],
    ]);
    expect(visits[4].ancestors.map((ancestor) => ancestor.id)).toEqual(["refunds", "large"]);
    expect(countDrafts(tree())).toBe(7);
  });

  it("labels positions across sections and finds blocks at any depth", () => {
    expect(positionLabel([2, 2, 1])).toBe("2.2.1");
    expect(findDraft(tree(), "huge")?.path).toEqual([2, 2, 1]);
    expect(findDraft(tree(), "missing")).toBeUndefined();
    expect(railOf(tree(), "large").map((block) => block.id)).toEqual(["huge"]);
    expect(railOf(tree(), "a")).toEqual([]);
  });

  it("collects every condition the tree tests", () => {
    expect(allConditions(tree()).map((node) => node.id)).toEqual([
      "a-when",
      "refunds-when",
      "small-when",
      "large-when",
      "huge-when",
      "tiny-when",
      "b-when",
    ]);
  });
});

describe("structural edits", () => {
  it("replaces a nested block and shares every rail it did not touch", () => {
    const before = tree();
    const after = updateDraft(before, "huge", (block) => ({ ...block, name: "Huge refunds" }));
    expect(findDraft(after, "huge")?.block.name).toBe("Huge refunds");
    expect(after[0]).toBe(before[0]);
    expect(after[2]).toBe(before[2]);
    const [refundsBefore, refundsAfter] = [before[1], after[1]];
    if (refundsBefore.kind !== "branch" || refundsAfter.kind !== "branch") throw new Error("expected branches");
    expect(refundsAfter.blocks[0]).toBe(refundsBefore.blocks[0]);
    expect(updateDraft(before, "missing", (block) => block)).toBe(before);
  });

  it("removes, inserts and appends at depth", () => {
    expect(shape(removeDraft(tree(), "large"))).toEqual(["a", { refunds: ["small", "tiny"] }, "b"]);
    expect(shape(insertAfter(tree(), "huge", cel("new")))).toEqual(["a", { refunds: ["small", { large: ["huge", "new"] }, "tiny"] }, "b"]);
    expect(shape(appendToRail(tree(), "refunds", cel("new")))).toEqual(["a", { refunds: ["small", { large: ["huge"] }, "tiny", "new"] }, "b"]);
    expect(shape(appendToRail(tree(), null, cel("new")))).toEqual(["a", { refunds: ["small", { large: ["huge"] }, "tiny"] }, "b", "new"]);
  });

  it("reorders within one rail only", () => {
    expect(shape(reorderRail(tree(), "refunds", 2, 0))).toEqual(["a", { refunds: ["tiny", "small", { large: ["huge"] }] }, "b"]);
    expect(shape(reorderRail(tree(), null, 0, 2))).toEqual([{ refunds: ["small", { large: ["huge"] }, "tiny"] }, "b", "a"]);
    const drafts = tree();
    expect(reorderRail(drafts, "refunds", 0, 9)).toBe(drafts);
    expect(reorderRail(drafts, "refunds", 1, 1)).toBe(drafts);
  });

  it("moves the block after a branch into it as its last block", () => {
    expect(shape(moveIntoBranchAbove(tree(), "b"))).toEqual(["a", { refunds: ["small", { large: ["huge"] }, "tiny", "b"] }]);
    expect(shape(moveIntoBranchAbove(tree(), "tiny"))).toEqual(["a", { refunds: ["small", { large: ["huge", "tiny"] }] }, "b"]);
    const drafts = tree();
    expect(moveIntoBranchAbove(drafts, "refunds")).toBe(drafts);
    expect(moveIntoBranchAbove(drafts, "a")).toBe(drafts);
  });

  it("moves a block out of its branch to right after it", () => {
    expect(shape(moveOutOfBranch(tree(), "small"))).toEqual(["a", { refunds: [{ large: ["huge"] }, "tiny"] }, "small", "b"]);
    expect(shape(moveOutOfBranch(tree(), "huge"))).toEqual(["a", { refunds: ["small", { large: [] }, "huge", "tiny"] }, "b"]);
    const drafts = tree();
    expect(moveOutOfBranch(drafts, "a")).toBe(drafts);
  });

  it("unwraps a branch into its blocks where it stood", () => {
    expect(shape(unwrapBranch(tree(), "refunds"))).toEqual(["a", "small", { large: ["huge"] }, "tiny", "b"]);
    expect(shape(unwrapBranch(tree(), "large"))).toEqual(["a", { refunds: ["small", "huge", "tiny"] }, "b"]);
    const drafts = tree();
    expect(unwrapBranch(drafts, "a")).toBe(drafts);
  });
});

describe("counting changes", () => {
  it("counts each nested block changed, added, removed or moved once", () => {
    const baseline = tree();
    expect(changedBlockCount(baseline, tree())).toBe(0);
    expect(changedBlockCount(baseline, updateDraft(tree(), "huge", (block) => ({ ...block, reason: "Other." })))).toBe(1);
    expect(changedBlockCount(baseline, updateDraft(tree(), "refunds", (block) => ({ ...block, name: "Refunds" })))).toBe(1);
    expect(changedBlockCount(baseline, appendToRail(tree(), "large", cel("new")))).toBe(1);
    expect(changedBlockCount(baseline, removeDraft(tree(), "large"))).toBe(2);
    expect(changedBlockCount(baseline, moveIntoBranchAbove(tree(), "b"))).toBe(1);
    expect(changedBlockCount(baseline, reorderRail(tree(), "refunds", 0, 2))).toBe(1);
  });
});

describe("reachability", () => {
  it("lets an Always inside a branch block its later siblings and the branch end, not what follows the branch", () => {
    const stop = always("inner-stop");
    const drafts = [branch("refunds", [cel("small"), stop, cel("after-stop")]), cel("after-branch")];
    const reachability = pipelineReachability(drafts);
    expect([...reachability.blockedBy.keys()]).toEqual(["after-stop"]);
    expect(reachability.blockedBy.get("after-stop")).toBe(stop);
    expect(reachability.endBlockedBy.get("refunds")).toBe(stop);
    expect(reachability.stoppedBy).toBeUndefined();
  });

  it("keeps a nested branch's Always inside that branch", () => {
    const stop = always("deep-stop");
    const drafts = [branch("outer", [branch("inner", [stop, cel("dead")]), cel("alive")])];
    const reachability = pipelineReachability(drafts);
    expect([...reachability.blockedBy.keys()]).toEqual(["dead"]);
    expect([...reachability.endBlockedBy.keys()]).toEqual(["inner"]);
  });

  it("passes a top-level Always to a later branch's blocks and end", () => {
    const stop = always("stop");
    const reachability = pipelineReachability([stop, branch("refunds", [cel("small")]), cel("b")]);
    expect([...reachability.blockedBy.keys()]).toEqual(["refunds", "small", "b"]);
    expect(reachability.endBlockedBy.get("refunds")).toBe(stop);
    expect(reachability.stoppedBy).toBe(stop);
  });

  it("leaves a branch alone when nothing before it stops the rail", () => {
    const reachability = pipelineReachability([branch("refunds", [cel("small")])]);
    expect(reachability.blockedBy.size).toBe(0);
    expect(reachability.stoppedBy).toBeUndefined();
  });
});
