// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { OutcomeBlockCard, OutcomeBlockSummaryCard, type OutcomeBlockDraft } from "./outcome-block";
import { PipelineExits } from "./pipeline-flow";

afterEach(cleanup);
const block: OutcomeBlockDraft = { id: "remaining", name: "Remaining", enabled: true, outcome: "human", reason: "Review everything else." };

describe("Outcome card", () => {
  it("edits a named outcome without a condition builder", () => {
    const onChange = vi.fn();
    render(<OutcomeBlockCard block={block} position={1} expanded onExpandedChange={() => undefined} onChange={onChange} onRemove={() => undefined} onDuplicate={() => undefined} />);
    expect(screen.getByText("Decides every request that reaches this block.")).toBeTruthy();
    expect(screen.queryByRole("group", { name: "Group match mode" })).toBeNull();
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Catch all" } });
    expect(onChange).toHaveBeenCalledWith({ ...block, name: "Catch all" });
  });

  it("shows the saved explanation and one terminal exit", () => {
    render(<><OutcomeBlockSummaryCard block={block} position={1} expanded onExpandedChange={() => undefined} />
      <PipelineExits exits={[{ key: "match", label: "Every request", outcome: "human" }]} />
    </>);
    expect(screen.getByText("Outcome · Every request that reaches it")).toBeTruthy();
    expect(screen.getByText(block.reason)).toBeTruthy();
    expect(screen.queryByText(/Otherwise/)).toBeNull();
  });
});
