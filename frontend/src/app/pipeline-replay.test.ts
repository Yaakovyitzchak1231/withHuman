import { describe, expect, it } from "vitest";
import type {
  RequestApprovalPipelinePreview,
  RequestApprovalPipelinePreviewBlock,
} from "../types";
import type { BlockDraft, BranchDraft } from "./pipeline-blocks";
import {
  carriedMetadata,
  runFromPreviews,
  statusAt,
  stepAt,
  stepKey,
  takenExit,
  type TestRun,
} from "./pipeline-replay";

function evaluated(
  blockId: string,
  outcome: RequestApprovalPipelinePreviewBlock["outcome"],
  extra: Partial<RequestApprovalPipelinePreviewBlock> = {},
): RequestApprovalPipelinePreviewBlock {
  return { block_id: blockId, position: 0, outcome, cost: 1, trace: [], ...extra };
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
  kind: "cel",
  id: "small",
  name: "Approve small refunds",
  enabled: true,
  outcome: "approve",
  when: { id: "small-when", all: [] },
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
    const run = runFromPreviews([{
      section: "agent",
      preview: preview([evaluated("refunds", "next"), evaluated("later", "approve")], {
        outcome: "approve",
        reason_code: "block_outcome",
        terminal_block_id: "later",
      }),
    }]);
    expect(settled(run, keys)).toEqual({
      "agent/refunds": "passed",
      "agent/small": "unreached",
      "agent/refunds#end": "unreached",
      "agent/later": "terminal",
    });
    expect(takenExit(stepAt(run, run.steps.length, "agent/refunds"), refunds)).toBe("continue");
  });

  it("lights the elbow when the branch is entered and a block inside decides", () => {
    const run = runFromPreviews([{
      section: "agent",
      preview: preview([evaluated("refunds", "enter"), evaluated("small", "approve")], {
        outcome: "approve",
        reason_code: "block_outcome",
        terminal_block_id: "small",
      }),
    }]);
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
    const run = runFromPreviews([{
      section: "agent",
      preview: preview([
        evaluated("refunds", "enter"),
        evaluated("small", "next"),
        evaluated("refunds", "human", { end_of_branch: true, reason: refunds.reason }),
      ], {
        outcome: "human",
        reason_code: "end_of_branch",
        terminal_block_id: "refunds",
      }),
    }]);
    expect(run.reasonCode).toBe("end_of_branch");
    expect(run.terminalKey).toBe("agent/refunds#end");
    expect(run.steps.map((step) => step.key)).toEqual(["agent/refunds", "agent/small", "agent/refunds#end"]);
    expect(settled(run, keys)).toEqual({
      "agent/refunds": "passed",
      "agent/small": "passed",
      "agent/refunds#end": "terminal",
      "agent/later": "unreached",
    });
  });

  it("makes the branch card terminal when its condition fails", () => {
    const run = runFromPreviews([{
      section: "agent",
      preview: preview([evaluated("refunds", "human", { error_code: "evaluation_error" })], {
        outcome: "human",
        reason_code: "block_error",
        terminal_block_id: "refunds",
      }),
    }]);
    expect(run.terminalKey).toBe("agent/refunds");
    expect(settled(run, keys)["agent/refunds"]).toBe("terminal");
    expect(settled(run, keys)["agent/refunds#end"]).toBe("unreached");
    expect(takenExit(stepAt(run, run.steps.length, "agent/refunds"), refunds)).toBeUndefined();
  });

  it("advances one step at a time, evaluating the cursor's step", () => {
    const run = runFromPreviews([{
      section: "agent",
      preview: preview([evaluated("refunds", "enter"), evaluated("small", "approve")], {
        outcome: "approve",
        reason_code: "block_outcome",
        terminal_block_id: "small",
      }),
    }]);
    expect(statusAt(run, 0, "agent/refunds")).toBe("evaluating");
    expect(statusAt(run, 0, "agent/small")).toBe("pending");
    expect(statusAt(run, 0, "agent/later")).toBe("pending");
    expect(stepAt(run, 0, "agent/refunds")).toBeUndefined();
    expect(statusAt(run, 1, "agent/refunds")).toBe("passed");
    expect(statusAt(run, 1, "agent/small")).toBe("evaluating");
    expect(statusAt(null, 0, "agent/small")).toBe("idle");
  });
});

describe("replaying the effective pipeline", () => {
  it("keeps organization and agent blocks with the same key apart", () => {
    const organization = preview([evaluated("shared", "next", { metadata: { score: 3 } })], {
      outcome: "human",
      reason_code: "end_of_pipeline",
    });
    const agent = preview([evaluated("shared", "approve")], {
      outcome: "approve",
      reason_code: "block_outcome",
      terminal_block_id: "shared",
    });
    expect(carriedMetadata(organization)).toEqual({ shared: { score: 3 } });
    const run = runFromPreviews([
      { section: "organization", preview: organization },
      { section: "agent", preview: agent },
    ]);
    expect(run.steps.map((step) => step.key)).toEqual(["organization/shared", "agent/shared"]);
    expect(settled(run, ["organization/shared", "agent/shared"])).toEqual({
      "organization/shared": "passed",
      "agent/shared": "terminal",
    });
  });

  it("stops in the organization section when a branch there runs out", () => {
    const organization = preview([
      evaluated("refunds", "enter"),
      evaluated("refunds", "human", { end_of_branch: true }),
    ], { outcome: "human", reason_code: "end_of_branch", terminal_block_id: "refunds" });
    const run = runFromPreviews([{ section: "organization", preview: organization }]);
    expect(run.terminalKey).toBe(stepKey("organization", "refunds", true));
    expect(run.outcome).toBe("human");
  });

  it("continues to the agent's pipeline from the organization's own page", () => {
    const organization = preview([evaluated("refunds", "next")], { outcome: "human", reason_code: "end_of_pipeline" });
    const run = runFromPreviews([{ section: "organization", preview: organization }], true);
    expect(run).toMatchObject({ outcome: "continue", reasonCode: "end_of_section" });
    expect(run.terminalKey).toBeUndefined();
  });
});
