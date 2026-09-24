import type {
  PipelineScope,
  PreviewBlockOutcome,
  RequestApprovalPipelinePreview,
  RequestApprovalPipelinePreviewBlock,
} from "../types";
import { blockKind } from "./extensions";
import type { BlockDraft } from "./pipeline-blocks";

/*
 * A test run replayed on the rail. A preview lists every evaluation in
 * execution order as one flat list, whatever the block's depth, so the
 * rail finds a block's step by key: the section it sits in (the
 * organization and an agent may share a block key) and its block key,
 * with "#end" for the closing entry of a branch the request ran out of.
 */

/* Which section of the effective pipeline a rail draws. */
export type RailSection = PipelineScope;

/* Where one block stands inside a replayed test run. */
export type TestStatus = "idle" | "pending" | "evaluating" | "passed" | "terminal" | "unreached";

/* A test run's result: a terminal outcome, or "continue" when the
 * organization section alone falls through to the agent's pipeline. */
export type TestOutcome = "approve" | "deny" | "human" | "continue";

/* A webhook step's evidence: what was called and what came back. */
export interface DeliveryStepEvidence {
  endpointHost: string;
  httpStatus?: number;
  durationMs: number;
  reason: string;
}

export interface TestStep {
  key: string;
  blockId: string;
  section: RailSection;
  outcome: PreviewBlockOutcome;
  /* The closing entry of a branch no block inside decided. */
  endOfBranch: boolean;
  errorCode?: string;
  /* What the block returned for later blocks and the reader: a kind
   * the edition adds keeps its evidence here and reads it back itself. */
  metadata: Record<string, unknown>;
  delivery?: DeliveryStepEvidence;
}

export interface TestRun {
  steps: TestStep[];
  outcome: TestOutcome;
  reasonCode: RequestApprovalPipelinePreview["reason_code"] | "end_of_section";
  /* The step that decided, when a block did: the terminal block's key,
   * or its "#end" key when the request ran out of that branch. */
  terminalKey?: string;
}

export function stepKey(section: RailSection, blockId: string, end = false): string {
  return `${section}/${blockId}${end ? "#end" : ""}`;
}

export function testStep(block: RequestApprovalPipelinePreviewBlock, section: RailSection): TestStep {
  const delivery: DeliveryStepEvidence | undefined = block.delivery
    ? {
        endpointHost: block.delivery.endpoint_host,
        httpStatus: block.delivery.http_status,
        durationMs: block.delivery.duration_ms,
        reason: block.delivery.reason ?? "",
      }
    : undefined;
  const endOfBranch = block.end_of_branch === true;
  return {
    key: stepKey(section, block.block_id, endOfBranch),
    blockId: block.block_id,
    section,
    outcome: block.outcome,
    endOfBranch,
    errorCode: block.error_code,
    metadata: block.metadata ?? {},
    delivery,
  };
}

export function stepsFromPreview(preview: RequestApprovalPipelinePreview, section: RailSection): TestStep[] {
  return preview.blocks.map((block) => testStep(block, section));
}

/* The step that ended the run: the block the preview names, or that
 * branch's end when the request ran out of it. None when nothing
 * decided. */
export function terminalKeyOf(preview: RequestApprovalPipelinePreview, section: RailSection): string | undefined {
  if (!preview.terminal_block_id) return undefined;
  return stepKey(section, preview.terminal_block_id, preview.reason_code === "end_of_branch");
}

/* What the blocks of a section that ran through returned, for the next
 * section's blocks to read, keyed by block key as at runtime. */
export function carriedMetadata(preview: RequestApprovalPipelinePreview): Record<string, Record<string, unknown>> {
  const carried: Record<string, Record<string, unknown>> = {};
  for (const block of preview.blocks) {
    if (block.outcome === "next" && block.metadata && Object.keys(block.metadata).length) {
      carried[block.block_id] = block.metadata;
    }
  }
  return carried;
}

/* One run from the previews a test made, in execution order: the
 * organization section, then the agent's when the organization's ran
 * through. The last preview decides. On the organization pipeline's own
 * page (continues), running through goes on to the agent's pipeline. */
export function runFromPreviews(
  previews: { section: RailSection; preview: RequestApprovalPipelinePreview }[],
  continues = false,
): TestRun {
  const steps = previews.flatMap(({ section, preview }) => stepsFromPreview(preview, section));
  const last = previews[previews.length - 1];
  if (continues && last.preview.reason_code === "end_of_pipeline") {
    return { steps, outcome: "continue", reasonCode: "end_of_section" };
  }
  return {
    steps,
    outcome: last.preview.outcome,
    reasonCode: last.preview.reason_code,
    terminalKey: terminalKeyOf(last.preview, last.section),
  };
}

/* The replayed step for a key, once the replay has reached it. */
export function stepAt(run: TestRun | null, cursor: number, key: string): TestStep | undefined {
  if (!run) return undefined;
  const index = run.steps.findIndex((step) => step.key === key);
  if (index === -1 || index >= cursor) return undefined;
  return run.steps[index];
}

/* Where a key stands as the replay advances: waiting, being evaluated,
 * passed, or terminal once reached; unreached when the run settled
 * without it. */
export function statusAt(run: TestRun | null, cursor: number, key: string): TestStatus {
  if (!run) return "idle";
  const settled = cursor >= run.steps.length;
  const index = run.steps.findIndex((step) => step.key === key);
  if (index === -1) return settled ? "unreached" : "pending";
  if (index === cursor && !settled) return "evaluating";
  if (index >= cursor) return "pending";
  return key === run.terminalKey ? "terminal" : "passed";
}

/* Which exit of a block a test step took: the answer for a webhook,
 * "match" for conditions and Always, "enter" when a branch's conditions
 * were met, "continue" when it fell through, and whatever a registered
 * kind reads from its own metadata. A failed evaluation took none. */
export function takenExit(step: TestStep | undefined, block: BlockDraft): string | undefined {
  if (!step) return undefined;
  if (block.kind === "branch") {
    if (step.errorCode) return undefined;
    if (step.outcome === "enter") return "enter";
    return step.outcome === "next" ? "continue" : undefined;
  }
  if (step.outcome === "next") return "continue";
  if (step.errorCode) return undefined;
  switch (block.kind) {
    case "edition":
      return blockKind(block.type)?.takenExit(step);
    case "unknown":
      return undefined;
    case "webhook":
      /* The endpoint's answer is the exit. */
      return step.outcome;
    case "cel":
    case "always":
      return "match";
  }
}
