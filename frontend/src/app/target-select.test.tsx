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

  it("stays listed but disabled, saying why, when there is no path to fall back to", () => {
    open([]);
    const owner = screen.getByRole("option", { name: /The agent's owner/ });
    expect(owner.getAttribute("aria-disabled")).toBe("true");
    expect(owner.textContent).toContain("Create an escalation path first");
  });

  it("is not offered in a picker of people and teams", () => {
    open([{ type: "team", id: "team-1", name: "Payments" }]);
    expect(screen.queryByRole("option", { name: /The agent's owner/ })).toBeNull();
  });
});
