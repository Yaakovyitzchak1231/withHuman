import { describe, expect, it } from "vitest";
import type {
  RequestApprovalPipelinePreview,
  RequestApprovalPipelinePreviewBlock,
} from "../types";
import type { BlockDraft, BranchDraft } from "./pipeline-blocks";
import {
  foldDelegations,
  runFromPreview,
  statusAt,
  stepAt,
  stepKey,
  takenExit,
  type TestRun,
} from "./pipeline-replay";
import type { DelegateDraft } from "./pipeline-blocks";

function evaluated(
  blockId: string,
  outcome: RequestApprovalPipelinePreviewBlock["outcome"],
  extra: Partial<RequestApprovalPipelinePreviewBlock> = {},
): RequestApprovalPipelinePreviewBlock {
  return { block_id: blockId, pipeline_key: "agent", position: 0, outcome, cost: 1, trace: [], ...extra };
}

function preview(
  blocks: RequestApprovalPipelinePreviewBlock[],
  result: Pick<RequestApprovalPipelinePreview, "outcome" | "reason_code" | "terminal_block_id">,
): RequestApprovalPipelinePreview {
  return { blocks, ...result };
}

const refunds: BranchDraft = {
  kind: "branch",
  id: "refunds",
  name: "Refunds",
  enabled: true,
  when: { id: "refunds-when", all: [{ id: "refunds-leaf", field: "/request/tool", operator: "equals", value: "stripe.refund" }] },
  blocks: [],
  reason: "Refunds need a person when no check decides.",
  target: null,
};

const small: BlockDraft = {
  kind: "outcome",
  id: "small",
  name: "Approve small refunds",
  enabled: true,
  outcome: "approve",
  reason: "Small refunds are fine.",
  target: null,
};

/* Where each key stands once the replay has settled. */
function settled(run: TestRun, keys: string[]): Record<string, string> {
  return Object.fromEntries(keys.map((key) => [key, statusAt(run, run.steps.length, key)]));
}

describe("replaying a branch", () => {
  const keys = ["agent/refunds", "agent/small", "agent/refunds#end", "agent/later"];

  it("greys a skipped branch's blocks and end, and takes the skip row", () => {
    const run = runFromPreview(preview([evaluated("refunds", "next"), evaluated("later", "approve")], {
        outcome: "approve",
        reason_code: "block_outcome",
        terminal_block_id: "later",
      }));
    expect(settled(run, keys)).toEqual({
      "agent/refunds": "passed",
      "agent/small": "unreached",
      "agent/refunds#end": "unreached",
      "agent/later": "terminal",
    });
    expect(takenExit(stepAt(run, run.steps.length, "agent/refunds"), refunds)).toBe("continue");
  });

  it("lights the elbow when the branch is entered and a block inside decides", () => {
    const run = runFromPreview(preview([evaluated("refunds", "enter"), evaluated("small", "approve")], {
        outcome: "approve",
        reason_code: "block_outcome",
        terminal_block_id: "small",
      }));
    expect(run.terminalKey).toBe("agent/small");
    expect(settled(run, keys)).toEqual({
      "agent/refunds": "passed",
      "agent/small": "terminal",
      "agent/refunds#end": "unreached",
      "agent/later": "unreached",
    });
    expect(takenExit(stepAt(run, run.steps.length, "agent/refunds"), refunds)).toBe("enter");
    expect(takenExit(stepAt(run, run.steps.length, "agent/small"), small)).toBe("match");
  });

  it("ends at the branch's end card when nothing inside decides", () => {
    /* A webhook inside the branch answered next. */
    const run = runFromPreview(preview([
        evaluated("refunds", "enter"),
        evaluated("fraud", "next"),
        evaluated("refunds", "human", { end_of_branch: true, reason: refunds.reason }),
      ], {
        outcome: "human",
        reason_code: "end_of_branch",
        terminal_block_id: "refunds",
      }));
    expect(run.reasonCode).toBe("end_of_branch");
    expect(run.terminalKey).toBe("agent/refunds#end");
    expect(run.steps.map((step) => step.key)).toEqual(["agent/refunds", "agent/fraud", "agent/refunds#end"]);
    expect(settled(run, ["agent/refunds", "agent/fraud", "agent/refunds#end", "agent/later"])).toEqual({
      "agent/refunds": "passed",
      "agent/fraud": "passed",
      "agent/refunds#end": "terminal",
      "agent/later": "unreached",
    });
  });

  it("makes the branch card terminal when its condition fails", () => {
    const run = runFromPreview(preview([evaluated("refunds", "human", { error_code: "evaluation_error" })], {
        outcome: "human",
        reason_code: "block_error",
        terminal_block_id: "refunds",
      }));
    expect(run.terminalKey).toBe("agent/refunds");
    expect(settled(run, keys)["agent/refunds"]).toBe("terminal");
    expect(settled(run, keys)["agent/refunds#end"]).toBe("unreached");
    expect(takenExit(stepAt(run, run.steps.length, "agent/refunds"), refunds)).toBeUndefined();
  });

  it("advances one step at a time, evaluating the cursor's step", () => {
    const run = runFromPreview(preview([evaluated("refunds", "enter"), evaluated("small", "approve")], {
        outcome: "approve",
        reason_code: "block_outcome",
        terminal_block_id: "small",
      }));
    expect(statusAt(run, 0, "agent/refunds")).toBe("evaluating");
    expect(statusAt(run, 0, "agent/small")).toBe("pending");
    expect(statusAt(run, 0, "agent/later")).toBe("pending");
    expect(stepAt(run, 0, "agent/refunds")).toBeUndefined();
    expect(statusAt(run, 1, "agent/refunds")).toBe("passed");
    expect(statusAt(run, 1, "agent/small")).toBe("evaluating");
    expect(statusAt(null, 0, "agent/small")).toBe("idle");
  });
});

const toBilling: DelegateDraft = {
  kind: "delegate",
  id: "to-billing",
  name: "Billing requests",
  enabled: true,
  when: { id: "billing-when", all: [{ id: "billing-leaf", field: "/agent/slug", operator: "equals", value: "billing" }] },
  pipelineKey: "billing",
};

describe("replaying a delegate", () => {
  const keys = ["entry/to-billing", "billing/small", "entry/to-billing#end", "entry/later"];
  const step = (blockId: string, pipelineKey: string, outcome: RequestApprovalPipelinePreviewBlock["outcome"], extra: Partial<RequestApprovalPipelinePreviewBlock> = {}) =>
    evaluated(blockId, outcome, { pipeline_key: pipelineKey, ...extra });

  it("keeps blocks of the delegated pipeline under its own key", () => {
    const run = runFromPreview(preview([
      step("to-billing", "entry", "enter"),
      step("small", "billing", "approve"),
    ], { outcome: "approve", reason_code: "block_outcome", terminal_block_id: "small" }));
    expect(run.steps.map((entry) => entry.key)).toEqual(["entry/to-billing", "billing/small"]);
    expect(run.terminalKey).toBe("billing/small");
    expect(settled(run, keys)).toEqual({
      "entry/to-billing": "passed",
      "billing/small": "terminal",
      "entry/to-billing#end": "unreached",
      "entry/later": "unreached",
    });
    expect(takenExit(stepAt(run, run.steps.length, "entry/to-billing"), toBilling)).toBe("enter");
  });

  it("ends at the delegate's end when the target runs out, keyed to the delegating pipeline", () => {
    const run = runFromPreview(preview([
      step("to-billing", "entry", "enter"),
      step("small", "billing", "next"),
      step("to-billing", "entry", "human", { end_of_branch: true }),
    ], { outcome: "human", reason_code: "end_of_branch", terminal_block_id: "to-billing" }));
    expect(run.terminalKey).toBe("entry/to-billing#end");
    expect(settled(run, keys)["entry/to-billing#end"]).toBe("terminal");
  });

  it("tells two pipelines' blocks with the same key apart", () => {
    const run = runFromPreview(preview([
      step("shared", "entry", "next"),
      step("to-billing", "entry", "enter"),
      step("shared", "billing", "approve"),
    ], { outcome: "approve", reason_code: "block_outcome", terminal_block_id: "shared" }));
    expect(run.terminalKey).toBe("billing/shared");
    expect(settled(run, ["entry/shared", "billing/shared"])).toEqual({ "entry/shared": "passed", "billing/shared": "terminal" });
  });

  it("skips the delegate when its condition does not match", () => {
    const run = runFromPreview(preview([
      step("to-billing", "entry", "next"),
      step("later", "entry", "approve"),
    ], { outcome: "approve", reason_code: "block_outcome", terminal_block_id: "later" }));
    expect(takenExit(stepAt(run, run.steps.length, "entry/to-billing"), toBilling)).toBe("continue");
    expect(settled(run, keys)["billing/small"]).toBe("unreached");
  });

  it("folds what ran inside the target onto the delegate that entered it", () => {
    const delegates = new Set(["to-billing"]);
    const decided = foldDelegations(runFromPreview(preview([
      step("first", "entry", "next"),
      step("to-billing", "entry", "enter"),
      step("to-payments", "billing", "enter"),
      step("small", "payments", "approve"),
    ], { outcome: "approve", reason_code: "block_outcome", terminal_block_id: "small" })), "entry", delegates);
    expect(decided.steps.map((entry) => entry.key)).toEqual(["entry/first", "entry/to-billing"]);
    expect(decided.terminalKey).toBe("entry/to-billing");
    expect(settled(decided, keys)["entry/to-billing"]).toBe("terminal");

    const ranOut = foldDelegations(runFromPreview(preview([
      step("to-billing", "entry", "enter"),
      step("small", "billing", "next"),
      step("to-billing", "entry", "human", { end_of_branch: true }),
    ], { outcome: "human", reason_code: "end_of_branch", terminal_block_id: "to-billing" })), "entry", delegates);
    expect(ranOut.steps.map((entry) => entry.key)).toEqual(["entry/to-billing"]);
    expect(ranOut.terminalKey).toBe("entry/to-billing");
  });

  it("leaves a run that never entered a delegate, or entered only a branch, alone", () => {
    const run = runFromPreview(preview([
      step("refunds", "entry", "enter"),
      step("small", "entry", "next"),
      step("refunds", "entry", "human", { end_of_branch: true }),
    ], { outcome: "human", reason_code: "end_of_branch", terminal_block_id: "refunds" }));
    expect(foldDelegations(run, "entry", new Set(["to-billing"]))).toBe(run);
  });
});
