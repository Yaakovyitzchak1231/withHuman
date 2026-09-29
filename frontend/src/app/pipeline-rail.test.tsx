// @vitest-environment jsdom
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { BlockDraft, BranchDraft } from "./pipeline-blocks";
import { dropIndex, PipelineRail, RailProvider, useRailEditor, type RailState } from "./pipeline-rail";
import { runFromPreview } from "./pipeline-replay";
import { pipelineReachability, walkDrafts } from "./pipeline-tree";

beforeEach(() => vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} }));
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function cel(id: string, name = id): BlockDraft {
  return {
    kind: "cel",
    id,
    name,
    enabled: true,
    outcome: "approve",
    when: { id: `${id}-when`, all: [{ id: `${id}-leaf`, field: "/request/arguments/amount_cents", operator: "less_than", value: 1000 }] },
    reason: "Small amounts are fine.",
    target: null,
  };
}

function branch(id: string, name: string, blocks: BlockDraft[]): BranchDraft {
  return {
    kind: "branch",
    id,
    name,
    enabled: true,
    when: { id: `${id}-when`, all: [{ id: `${id}-leaf`, field: "/request/tool", operator: "equals", value: "stripe.refund" }] },
    blocks,
    reason: "Refunds need a person when no check decides.",
    target: null,
  };
}

const blocks = (): BlockDraft[] => [
  cel("first", "Staging never waits"),
  branch("refunds", "Refunds", [cel("small", "Approve small refunds")]),
  cel("last", "Deny deletes"),
];

function state(drafts: BlockDraft[], overrides: Partial<RailState> = {}): RailState {
  return {
    mode: "view",
    section: "agent",
    reachability: pipelineReachability(drafts),
    directory: [],
    endpoints: [],
    defaultTarget: null,
    describeRoute: () => "",
    expandedKey: null,
    onExpandedKeyChange: () => undefined,
    run: null,
    cursor: 0,
    ...overrides,
  };
}

function Editor({ initial, onChange }: { initial: BlockDraft[]; onChange: (drafts: BlockDraft[]) => void }) {
  const [drafts, setDrafts] = useState(initial);
  const [expandedKey, setExpandedKey] = useState<string | null>(null);
  const { editor, dialog } = useRailEditor((next) => {
    setDrafts((current) => {
      const updated = typeof next === "function" ? next(current) : next;
      onChange(updated);
      return updated;
    });
  }, () => setExpandedKey(null));
  return (
    <RailProvider value={state(drafts, { mode: "edit", editor, expandedKey, onExpandedKeyChange: setExpandedKey })}>
      <PipelineRail blocks={drafts} lastLabel="Otherwise" />
      {dialog}
    </RailProvider>
  );
}

describe("the rail", () => {
  it("numbers blocks down the tree", () => {
    render(
      <RailProvider value={state(blocks())}>
        <PipelineRail blocks={blocks()} lastLabel="Otherwise" />
      </RailProvider>,
    );
    for (const chip of ["1", "2", "2.1", "3"]) expect(screen.getByText(chip)).toBeTruthy();
  });

  it("draws a branch's blocks and end in its well, and the skip row after it", () => {
    const { container } = render(
      <RailProvider value={state(blocks())}>
        <PipelineRail blocks={blocks()} lastLabel="Otherwise" />
      </RailProvider>,
    );
    const well = screen.getByRole("group", { name: "Blocks in “Refunds”" });
    expect(within(well).getByText("Approve small refunds")).toBeTruthy();
    expect(within(well).getByText("No block in “Refunds” decided.")).toBeTruthy();
    const skip = screen.getByText("Otherwise, skip this branch and check the next block");
    expect(well.contains(skip)).toBe(false);
    const text = container.textContent ?? "";
    expect(text.indexOf("No block in “Refunds” decided.")).toBeLessThan(text.indexOf("Otherwise, skip this branch"));
    expect(text.indexOf("Otherwise, skip this branch")).toBeLessThan(text.indexOf("Deny deletes"));
  });

  it("uses the rail's own last words under a branch that comes last", () => {
    const drafts = [cel("first"), branch("refunds", "Refunds", [cel("small")])];
    render(
      <RailProvider value={state(drafts)}>
        <PipelineRail blocks={drafts} lastLabel="Otherwise, ask the default reviewers" />
      </RailProvider>,
    );
    expect(screen.queryByText("Otherwise, skip this branch and check the next block")).toBeNull();
    expect(screen.getByText("Otherwise, ask the default reviewers")).toBeTruthy();
  });

  it("rings the end card when the test ran out of the branch", () => {
    const run = runFromPreview({
      outcome: "human",
      reason_code: "end_of_branch",
      terminal_block_id: "refunds",
      blocks: [
        { block_id: "first", pipeline_key: "agent", position: 0, outcome: "next", cost: 1, trace: [] },
        { block_id: "refunds", pipeline_key: "agent", position: 1, outcome: "enter", cost: 1, trace: [] },
        { block_id: "small", pipeline_key: "agent", position: 2, outcome: "next", cost: 1, trace: [] },
        { block_id: "refunds", pipeline_key: "agent", position: 1, outcome: "human", end_of_branch: true, cost: 0, trace: [] },
      ],
    });
    render(
      <RailProvider value={state(blocks(), { run, cursor: run.steps.length })}>
        <PipelineRail blocks={blocks()} lastLabel="Otherwise" />
      </RailProvider>,
    );
    const end = screen.getByText("No block in “Refunds” decided.").closest(".rounded-lg")?.parentElement;
    expect(end?.className).toContain("ring-warn");
    /* The elbow into the well is lit: the request entered the branch. */
    const elbow = screen.getByRole("group", { name: "Blocks in “Refunds”" }).parentElement?.previousElementSibling as HTMLElement;
    expect(within(elbow).getByText("If conditions are met").className.split(" ")).toContain("text-ink");
    expect(screen.getByText("Otherwise, skip this branch and check the next block").className.split(" ")).toContain("text-ink-muted");
  });
});

describe("a delegate on the rail", () => {
  const delegate = (): BlockDraft => ({
    kind: "delegate",
    id: "to-billing",
    name: "Billing requests",
    enabled: true,
    when: { id: "d-when", all: [{ id: "d-leaf", field: "/agent/slug", operator: "equals", value: "billing" }] },
    pipelineKey: "billing",
  });

  it("draws the target's active blocks read only in its well, under the target's key", () => {
    const drafts = [delegate(), cel("last", "Deny deletes")];
    const run = runFromPreview({
      outcome: "approve",
      reason_code: "block_outcome",
      terminal_block_id: "small",
      blocks: [
        { block_id: "to-billing", pipeline_key: "entry", position: 0, outcome: "enter", cost: 1, trace: [] },
        { block_id: "small", pipeline_key: "billing", position: 1, outcome: "approve", cost: 1, trace: [] },
      ],
    });
    render(
      <RailProvider
        value={state(drafts, {
          section: "entry",
          pipelines: [{ key: "billing", name: "Billing", active: true }],
          delegates: { billing: { name: "Billing", blocks: [cel("small", "Approve small refunds")], defaultTarget: null } },
          run,
          cursor: run.steps.length,
        })}
      >
        <PipelineRail blocks={drafts} lastLabel="Otherwise" />
      </RailProvider>,
    );
    const well = screen.getByRole("group", { name: "Blocks in “Billing”" });
    expect(within(well).getByText("Approve small refunds")).toBeTruthy();
    expect(within(well).getByText("No block in “Billing” decided.")).toBeTruthy();
    /* The target's block is terminal because the replay keyed it to the
     * target pipeline, not to the pipeline being viewed. */
    const card = within(well).getByText("Approve small refunds").closest("[data-block-card]") as HTMLElement;
    expect(card.className).toContain("ring-approve");
    expect(screen.getByText("Otherwise, skip this pipeline and check the next block")).toBeTruthy();
  });

  it("names a target it cannot show, and refuses to draw a cycle", () => {
    render(
      <RailProvider value={state([delegate()], { section: "entry" })}>
        <PipelineRail blocks={[delegate()]} lastLabel="Otherwise" />
      </RailProvider>,
    );
    expect(screen.getByText(/You cannot view that pipeline/)).toBeTruthy();
    cleanup();
    render(
      <RailProvider value={state([delegate()], { section: "billing", enclosing: ["entry"] })}>
        <PipelineRail blocks={[delegate()]} lastLabel="Otherwise" />
      </RailProvider>,
    );
    expect(screen.getByText("Cycle")).toBeTruthy();
  });
});

describe("editing the rail", () => {
  it("adds a block to the branch whose slot was used", async () => {
    const onChange = vi.fn();
    render(<Editor initial={blocks()} onChange={onChange} />);
    fireEvent.click(screen.getByRole("combobox", { name: "Add block to this branch" }));
    fireEvent.click(await screen.findByText("Conditions block"));
    await waitFor(() => expect(onChange).toHaveBeenCalled());
    const drafts = onChange.mock.lastCall![0] as BlockDraft[];
    const refunds = drafts[1];
    if (refunds.kind !== "branch") throw new Error("expected the branch");
    expect(refunds.blocks.map((block) => block.kind)).toEqual(["cel", "cel"]);
    expect(drafts).toHaveLength(3);
  });

  it("asks before removing a branch that holds blocks", async () => {
    const onChange = vi.fn();
    render(<Editor initial={blocks()} onChange={onChange} />);
    const card = screen.getByText("Refunds").closest("[data-block-card]") as HTMLElement;
    fireEvent.click(within(card).getByRole("combobox", { name: "Block actions" }));
    fireEvent.click(await screen.findByText("Remove branch"));
    expect(await screen.findByText("Remove “Refunds” and its 1 block?")).toBeTruthy();
    expect(onChange).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Remove branch" }));
    await waitFor(() => expect(onChange).toHaveBeenCalled());
    expect(walkDrafts(onChange.mock.lastCall![0] as BlockDraft[]).map(({ block }) => block.id)).toEqual(["first", "last"]);
  });
});

describe("dragging", () => {
  it("measures only the rail's own rows, each at its own card", () => {
    const container = document.createElement("div");
    const rect = (top: number, height: number) => () => ({ top, height, bottom: top + height, left: 0, right: 0, width: 0, x: 0, y: top, toJSON: () => ({}) }) as DOMRect;
    const row = (top: number, height: number, cardHeight: number) => {
      const element = document.createElement("div");
      element.setAttribute("data-block-row", "");
      element.getBoundingClientRect = rect(top, height);
      const card = document.createElement("div");
      card.setAttribute("data-block-card", "");
      card.getBoundingClientRect = rect(top, cardHeight);
      element.append(card);
      container.append(element);
      return element;
    };
    row(0, 40, 40);
    /* A branch: a 40px card over a 400px well holding rows of its own. */
    const branchRow = row(40, 440, 40);
    const well = document.createElement("div");
    for (const top of [100, 200, 300]) {
      const nested = document.createElement("div");
      nested.setAttribute("data-block-row", "");
      nested.getBoundingClientRect = rect(top, 40);
      well.append(nested);
    }
    branchRow.append(well);
    row(480, 40, 40);

    expect(dropIndex(container, 10)).toBe(0);
    /* Past the middle of the branch's card, though far above the middle
     * of its whole row: measuring the row would say 1. */
    expect(dropIndex(container, 70)).toBe(2);
    /* The rows inside the well never count: counting them would say 3. */
    expect(dropIndex(container, 150)).toBe(2);
    expect(dropIndex(container, 600)).toBe(2);
    expect(dropIndex(document.createElement("div"), 10)).toBeNull();
  });
});
