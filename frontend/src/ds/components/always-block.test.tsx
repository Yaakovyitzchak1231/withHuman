// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { AlwaysBlockCard, AlwaysBlockSummaryCard, PipelineExits, type AlwaysBlockDraft } from "./policy-block";

afterEach(cleanup);
const block: AlwaysBlockDraft = { id: "remaining", name: "Remaining", enabled: true, outcome: "human", reason: "Review everything else." };

describe("Always card", () => {
  it("edits a named outcome without a condition builder", () => {
    const onChange = vi.fn();
    render(<AlwaysBlockCard block={block} position={1} expanded onExpandedChange={() => undefined} onChange={onChange} onRemove={() => undefined} onDuplicate={() => undefined} />);
    expect(screen.getByText("Applies to every request that reaches this block.")).toBeTruthy();
    expect(screen.queryByRole("group", { name: "Group match mode" })).toBeNull();
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Catch all" } });
    expect(onChange).toHaveBeenCalledWith({ ...block, name: "Catch all" });
  });

  it("shows the saved explanation and one terminal exit", () => {
    render(<><AlwaysBlockSummaryCard block={block} position={1} expanded onExpandedChange={() => undefined} />
      <PipelineExits exits={[{ key: "match", label: "Always", outcome: "human" }]} />
    </>);
    expect(screen.getByText("Always · Every remaining request")).toBeTruthy();
    expect(screen.getByText(block.reason)).toBeTruthy();
    expect(screen.queryByText(/Otherwise/)).toBeNull();
  });
});
