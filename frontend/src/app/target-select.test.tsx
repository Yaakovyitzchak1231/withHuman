// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { TargetSelect, type ReviewerTarget } from "@/ds/components/reviewer-target";

afterEach(cleanup);

function open(directory: ReviewerTarget[]) {
  render(<TargetSelect directory={directory} value={null} onChange={() => undefined} fallbackLabel="Pipeline default" aria-label="Escalation path" />);
  fireEvent.click(screen.getByRole("combobox", { name: "Escalation path" }));
}

describe("the route picker's agent-owner choice", () => {
  it("is offered beside the paths", () => {
    open([{ type: "escalation_path", id: "on-call", name: "On call" }]);
    const owner = screen.getByRole("option", { name: /The agent's owner/ });
    expect(owner.getAttribute("aria-disabled")).not.toBe("true");
  });

  it("is offered without any path, falling back to the pipeline default", () => {
    const changes: ReviewerTarget[] = [];
    render(<TargetSelect directory={[]} value={null} onChange={(next) => next && changes.push(next)} fallbackLabel="Pipeline default" aria-label="Escalation path" />);
    fireEvent.click(screen.getByRole("combobox", { name: "Escalation path" }));
    const owner = screen.getByRole("option", { name: /The agent's owner/ });
    expect(owner.getAttribute("aria-disabled")).not.toBe("true");
    fireEvent.click(owner);
    expect(changes).toEqual([{ type: "agent_owner", id: "", name: "" }]);
  });

  it("offers the pipeline default and the paths as the owner's fallback", () => {
    render(<TargetSelect directory={[{ type: "escalation_path", id: "on-call", name: "On call" }]} value={{ type: "agent_owner", id: "", name: "" }}
      onChange={() => undefined} fallbackLabel="Pipeline default" aria-label="Escalation path" />);
    fireEvent.click(screen.getByRole("combobox", { name: "Escalation path fallback" }));
    expect(screen.getByRole("option", { name: /Pipeline default/ })).toBeTruthy();
    expect(screen.getByRole("option", { name: /On call/ })).toBeTruthy();
  });

  it("is not offered in a picker of people and teams", () => {
    open([{ type: "team", id: "team-1", name: "Payments" }]);
    expect(screen.queryByRole("option", { name: /The agent's owner/ })).toBeNull();
  });
});
