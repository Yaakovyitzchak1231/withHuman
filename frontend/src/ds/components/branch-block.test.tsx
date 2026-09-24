// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { blockMenuActions } from "./block-card";
import {
  BranchBlockCard,
  BranchBlockSummaryCard,
  summarizeBranch,
  type BranchBlockDraft,
} from "./branch-block";
import { PipelineBranchEndCard, PipelineBranchWell } from "./policy-block";

beforeEach(() => vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} }));
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const branch: BranchBlockDraft<string> = {
  id: "refunds",
  name: "Refunds",
  enabled: true,
  when: { id: "when", all: [{ id: "tool", field: "/request/tool", operator: "equals", value: "stripe.refund" }] },
  blocks: ["small-refunds"],
  reason: "Refunds need a person when no check decides.",
  target: null,
};

function renderCard(overrides: Partial<Parameters<typeof BranchBlockCard<string>>[0]> = {}) {
  const onChange = vi.fn();
  render(
    <BranchBlockCard
      block={branch}
      position="3.2"
      expanded
      onExpandedChange={() => undefined}
      onChange={onChange}
      onRemove={() => undefined}
      onDuplicate={() => undefined}
      directory={[{ type: "escalation_path", id: "refund-reviewers", name: "Refund reviewers" }]}
      {...overrides}
    />,
  );
  return onChange;
}

describe("Branch card", () => {
  it("edits the branch's own fields and keeps its blocks", () => {
    const onChange = renderCard();
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Stripe refunds" } });
    expect(onChange).toHaveBeenCalledWith({ ...branch, name: "Stripe refunds" });
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "A person decides." } });
    expect(onChange).toHaveBeenLastCalledWith({ ...branch, reason: "A person decides." });
    expect(screen.getByRole("group", { name: "Group match mode" })).toBeTruthy();
    expect(screen.getByLabelText("Reviewers")).toBeTruthy();
  });

  it("says what happens when nothing inside decides, and numbers its place", () => {
    renderCard();
    expect(
      screen.getByText("If no block in this branch decides, the request goes to a human and the pipeline stops."),
    ).toBeTruthy();
    expect(screen.getByText("3.2")).toBeTruthy();
    expect(screen.getByText("Refunds")).toBeTruthy();
  });

  it("summarizes as the kind and the condition", () => {
    expect(summarizeBranch(branch)).toBe("Branch · Tool name equals stripe.refund");
    render(<BranchBlockSummaryCard block={branch} position="3" expanded onExpandedChange={() => undefined} />);
    expect(screen.getByText(/^Branch ·/).textContent).toBe("Branch · Tool name equals stripe.refund");
    expect(screen.getByText("If no block in this branch decides")).toBeTruthy();
    expect(screen.getByText(branch.reason)).toBeTruthy();
  });
});

describe("block menu", () => {
  const handlers = { onDuplicate: () => undefined, onRemove: () => undefined };

  it("lists the moves across a branch's edge only where they apply", () => {
    expect(blockMenuActions(handlers).map((action) => action.id)).toEqual(["move-up", "move-down", "duplicate", "remove"]);
    const actions = blockMenuActions({ ...handlers, onMoveIntoBranch: () => undefined, onMoveOutOfBranch: () => undefined });
    expect(actions.map((action) => action.label)).toEqual([
      "Move up",
      "Move down",
      "Move into the branch above",
      "Move out of this branch",
      "Duplicate block",
      "Remove block",
    ]);
    expect(actions.find((action) => action.id === "move-up")?.disabled).toBe(true);
  });

  it("names the branch in Duplicate and Remove and offers unwrapping", () => {
    const actions = blockMenuActions({
      ...handlers,
      noun: "branch",
      extra: [{ id: "unwrap", label: "Remove branch, keep its blocks", action: () => undefined }],
    });
    expect(actions.map((action) => action.label)).toEqual([
      "Move up",
      "Move down",
      "Duplicate branch",
      "Remove branch, keep its blocks",
      "Remove branch",
    ]);
  });
});

describe("branch well", () => {
  it("groups the branch's blocks under its name and skips past them", () => {
    render(
      <PipelineBranchWell name="Refunds" depth={1} taken="skip">
        <PipelineBranchEndCard branchName="Refunds" reason={branch.reason} />
      </PipelineBranchWell>,
    );
    const well = screen.getByRole("group", { name: "Blocks in “Refunds”" });
    expect(well.textContent).toContain("No block in “Refunds” decided.");
    expect(well.textContent).toContain("Ask a human");
    expect(well.textContent).toContain(branch.reason);
    expect(screen.getByText("Check the blocks in this branch")).toBeTruthy();
    expect(screen.getByText("Otherwise, skip this branch and check the next block")).toBeTruthy();
  });

  it("stops the rail at an unreachable branch", () => {
    render(
      <PipelineBranchWell name="Refunds" depth={2} blocked>
        <span>inside</span>
      </PipelineBranchWell>,
    );
    expect(screen.queryByText(/Otherwise/)).toBeNull();
    expect(screen.getByRole("group", { name: "Blocks in “Refunds”" }).className).toContain("bg-canvas");
  });
});
