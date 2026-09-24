// @vitest-environment jsdom
import { useState } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { ConditionBuilder, type ConditionNode } from "./condition-builder";

beforeEach(() => vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} }));
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });
const tools = [{ name: "refund", source: "payments", inputSchema: { properties: { amount: { type: "number" }, notify: { type: "boolean" }, currency: { type: "string", enum: ["GBP", "USD"] } } } }];
function Editor({ field, value }: { field: string; value: unknown }) {
  const [condition, setCondition] = useState<ConditionNode>({ id: "root", all: [{ id: "name", field: "/request/tool", operator: "equals", value: "refund" }, { id: "argument", field: `/request/arguments/${field}`, operator: "equals", value }] });
  return <><ConditionBuilder value={condition} onChange={setCondition} tools={tools} /><output data-testid="value">{JSON.stringify(condition.all![1].value)}</output></>;
}
it("edits numeric and boolean values as JSON primitives", () => {
  const view = render(<Editor field="amount" value="" />);
  const input = screen.getAllByLabelText("Comparison value")[1];
  fireEvent.change(input, { target: { value: "-" } });
  expect((input as HTMLInputElement).value).toBe("-");
  fireEvent.change(input, { target: { value: "-12.5" } });
  expect(screen.getByTestId("value").textContent).toBe("-12.5");
  fireEvent.change(input, { target: { value: "" } });
  expect(screen.getByTestId("value").textContent).toBe('""');
  view.unmount();
  render(<Editor field="notify" value="" />);
  fireEvent.click(screen.getByText("True", { exact: true }));
  expect(screen.getByTestId("value").textContent).toBe("true");
});
it("retains saved comparisons and offers enum choices without coercion", () => {
  const view = render(<Editor field="amount" value="100" />);
  expect(screen.getByTestId("value").textContent).toBe('"100"');
  expect(screen.getByText(/different type from the current argument schema/)).toBeTruthy();
  view.unmount();
  render(<Editor field="currency" value="" />);
  fireEvent.focus(screen.getAllByLabelText("Comparison value")[1]);
  fireEvent.click(screen.getByText("GBP", { exact: true }));
  expect(screen.getByTestId("value").textContent).toBe('"GBP"');
});
