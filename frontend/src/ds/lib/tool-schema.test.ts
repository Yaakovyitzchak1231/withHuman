import { describe, expect, it } from "vitest";
import { applicableToolNames, fieldHints, schemaFields, schemaWarning, suitableOperators, type ToolSchema } from "./tool-schema";

const tools: ToolSchema[] = [
 { name: "refund", source: "payments", inputSchema: { type: "object", required: ["amount"], properties: { amount: { type: "integer", description: "Minor units" }, currency: { type: "string", enum: ["GBP", "USD"] }, approved: { type: "boolean" } } } },
 { name: "deploy", source: "hosting", inputSchema: { type: "object", properties: { environment: { type: "string" }, amount: { type: "string" } } } },
];
const tool = (name: string) => ({ id: `tool-${name}`, field: "/request/tool", operator: "equals", value: name });
const field = (id: string, path: string) => ({ id, field: `/request/arguments/${path}`, operator: "equals", value: "" });

describe("tool schemas", () => {
 it("preserves branch boundaries and inherited constraints", () => {
  const root = { id: "root", any: [{ id: "a", all: [tool("refund"), field("amount", "amount")] }, { id: "b", all: [tool("deploy"), field("env", "environment")] }] };
  expect([...applicableToolNames(root, "amount")!]).toEqual(["refund"]);
  expect(Object.keys(fieldHints(root, "amount", tools).fields)).toEqual(["/request/arguments/amount", "/request/arguments/currency", "/request/arguments/approved"]);
  expect([...applicableToolNames(root, "env")!]).toEqual(["deploy"]);
  const alternatives = { id: "root", all: [{ id: "choices", any: [tool("refund"), tool("deploy")] }, field("amount", "amount")] };
  expect([...applicableToolNames(alternatives, "amount")!]).toEqual(["refund", "deploy"]);
  const unbounded = { id: "root", all: [{ id: "choices", any: [tool("refund"), field("other", "anything")] }, field("amount", "amount")] };
  expect(applicableToolNames(unbounded, "amount")).toBeNull();
 });
 it("supports in selections and never claims conflicting definitions are universal", () => {
  const root = { id: "root", all: [{ id: "t", field: "/request/tool", operator: "in", value: ["refund", "deploy"] }, field("a", "amount")] };
  const hints = fieldHints(root, "a", tools);
  expect(hints.fields["/request/arguments/amount"].types).toEqual(["integer", "string"]);
  expect(hints.fields["/request/arguments/currency"].partial).toBe(true);
  expect(suitableOperators(hints.fields["/request/arguments/amount"])).toBeNull();
  expect(schemaWarning({ ...field("a", "amount"), value: "text" }, hints)).toBeUndefined();
  expect(schemaWarning({ ...field("a", "amount"), value: 10 }, hints)).toBeUndefined();
 });
 it("resolves local references, escapes pointers, stops at arrays, and bounds recursion", () => {
  const result = schemaFields({ type: "object", required: ["parent"], $defs: { amount: { type: "number" }, recursive: { type: "object", properties: { self: { $ref: "#/$defs/recursive" } } } }, properties: { parent: { type: "object", required: ["x/y~z"], properties: { "x/y~z": { $ref: "#/$defs/amount" } } }, list: { type: "array", items: { type: "object", properties: { hidden: { type: "string" } } } }, cycle: { $ref: "#/$defs/recursive" }, remote: { $ref: "https://example.com/schema" } } });
  expect(result.find((item) => item.path.endsWith("x~1y~0z"))).toMatchObject({ types: ["number"], required: true });
  expect(result.some((item) => item.path.includes("hidden"))).toBe(false);
  expect(result.length).toBeLessThan(10);
  expect(result.find((item) => item.path.endsWith("remote"))?.types).toEqual([]);
  expect(schemaFields({ properties: { count: { enum: [1, 2] }, enabled: { const: true } } }).map((item) => item.types)).toEqual([["number"], ["boolean"]]);
 });
 it("keeps complex and unknown schemas advisory", () => {
  expect(schemaFields({ properties: { value: { anyOf: [{ type: "string" }, { type: "number" }] } } })[0].types).toEqual([]);
  const root = { id: "root", all: [tool("refund"), field("a", "amount")] };
  const hints = fieldHints(root, "a", tools);
  expect(suitableOperators(hints.fields["/request/arguments/amount"])).toContain("greater_than");
  expect(suitableOperators(hints.fields["/request/arguments/amount"])).not.toContain("in"); // The engine's in operator accepts strings only.
  expect(schemaWarning({ ...field("a", "amount"), value: "100" }, hints)).toContain("different type");
  expect(schemaWarning({ ...field("a", "missing") }, hints)).toContain("not declared");
  expect(schemaWarning({ ...field("a", "currency"), value: "EUR" }, hints)).toContain("outside the allowed values");
  expect(schemaWarning({ ...field("a", "currency"), value: "GBP" }, hints)).toBeUndefined();
  expect(schemaWarning({ ...field("a", "amount"), operator: "greater_than", value: "100" }, hints)).toContain("different type");
  expect(schemaWarning(field("a", "amount"), fieldHints(root, "a", [{ name: "refund", source: "Observed", observedPaths: ["/request/arguments/amount"] }]))).toBeUndefined();
 });
});
