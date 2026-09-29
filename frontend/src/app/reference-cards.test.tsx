// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import type { BlockDraft, BranchDraft } from "./pipeline-blocks";
import { PipelineRailTimeline } from "./reference-cards";

afterEach(cleanup);

/* A block that tests a condition and lets everything else continue. */
function check(id: string, name: string): BlockDraft {
  return {
    kind: "delegate",
    id,
    name,
    enabled: true,
    when: { id: `${id}-when`, all: [{ id: `${id}-leaf`, field: "/request/arguments/amount_cents", operator: "less_than", value: 1000 }] },
    pipelineKey: "billing",
  };
}

function outcome(id: string, name: string): BlockDraft {
  return { kind: "outcome", id, name, enabled: true, outcome: "approve", reason: "Small amounts are fine.", target: null };
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

const blocks: BlockDraft[] = [
  check("staging", "Staging never waits"),
  branch("refunds", "Refunds", [
    check("small", "Small refunds"),
    branch("large", "Large refunds", [check("huge", "Ask finance")]),
  ]),
  check("deletes", "Deletes"),
];

/* The marker of each item directly on a timeline: the rail's own
 * numbering, or nothing for its end. */
function markers(list: Element): string[] {
  return [...list.children].map((item) => item.firstElementChild?.textContent ?? "");
}

function itemOf(text: string): HTMLElement {
  return screen.getByText(text).closest("li") as HTMLElement;
}

describe("the pipeline hover card's rail", () => {
  it("nests a branch's blocks as a timeline numbered from one, ending in the branch's end", () => {
    const { container } = render(
      <PipelineRailTimeline blocks={blocks} fallback={null} end={<li>No match</li>} />,
    );
    const top = container.querySelector("ol") as HTMLElement;
    expect(markers(top)).toEqual(["1", "2", "3", ""]);
    const refunds = itemOf("Refunds").querySelector("ol") as HTMLElement;
    expect(markers(refunds)).toEqual(["1", "2", ""]);
    const large = itemOf("Large refunds").querySelector("ol") as HTMLElement;
    expect(markers(large)).toEqual(["1", ""]);
    expect(screen.getAllByText("No decision: ask a human via")).toHaveLength(2);
    expect(screen.getAllByText(/^Branch ·/, { selector: "p" }).map((line) => line.textContent)).toEqual([
      "Branch · Tool name equals stripe.refund",
      "Branch · Tool name equals stripe.refund",
    ]);
  });

  it("ends a branch at its Outcome block when that decides everything", () => {
    const gate = [branch("refunds", "Refunds", [outcome("small", "Approve small refunds")])];
    const { container } = render(<PipelineRailTimeline blocks={gate} fallback={null} end={<li>No match</li>} />);
    const refunds = itemOf("Refunds").querySelector("ol") as HTMLElement;
    expect(markers(refunds)).toEqual(["1"]);
    expect(screen.queryByText("No decision: ask a human via")).toBeNull();
    expect(markers(container.querySelector("ol") as HTMLElement)).toEqual(["1", ""]);
  });

  it("picks out a block at any depth", () => {
    render(
      <PipelineRailTimeline
        blocks={blocks}
        fallback={null}
        end={<li>No match</li>}
        highlight={{ key: "huge", note: <p>The note for this block</p> }}
      />,
    );
    const note = screen.getByText("The note for this block");
    expect(note.closest("li")).toBe(itemOf("Ask finance"));
  });

  it("picks out a branch's end for a request that ran out of it", () => {
    render(
      <PipelineRailTimeline
        blocks={blocks}
        fallback={null}
        end={<li>No match</li>}
        highlight={{ key: "large", note: <p>Nothing inside decided</p>, end: true }}
      />,
    );
    const note = screen.getByText("Nothing inside decided");
    const end = note.closest("li") as HTMLElement;
    expect(end.textContent).toContain("No decision: ask a human via");
    expect(end.parentElement).toBe(itemOf("Large refunds").querySelector("ol"));
  });
});
