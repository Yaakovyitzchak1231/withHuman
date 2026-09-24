import { describe, expect, it } from "vitest";
import { describeField, summarizeCondition } from "./condition-builder";

describe("describeField", () => {
  it("names the fixed fields the way a reviewer says them", () => {
    expect(describeField("/request/tool")).toEqual({ path: "/request/tool", label: "Tool name", argument: false });
    expect(describeField("/agent/slug").label).toBe("Agent slug");
    expect(describeField("/agent/name").label).toBe("Agent name");
    expect(describeField("/request/server")).toEqual({ path: "/request/server", label: "MCP server", argument: false });
    expect(describeField("/request/arguments").label).toBe("Arguments");
  });

  it("shows an argument as its dotted key and unescapes pointer tokens", () => {
    expect(describeField("/request/arguments/customer/email")).toEqual({
      path: "/request/arguments/customer/email",
      label: "customer.email",
      argument: true,
    });
    expect(describeField("/request/arguments/a~1b/c~0d").label).toBe("a/b.c~d");
  });

  it("shows an unknown pointer exactly as typed", () => {
    expect(describeField("/previous_metadata/org/block/score").label).toBe("/previous_metadata/org/block/score");
    expect(describeField("").label).toBe("field");
  });
});

describe("summarizeCondition", () => {
  it("reads as a sentence with the field names", () => {
    const summary = summarizeCondition({
      id: "root",
      all: [
        { id: "a", field: "/request/tool", operator: "in", value: ["deploy.production"] },
        { id: "b", field: "/request/arguments/amount_cents", operator: "greater_than_or_equal", value: 1000 },
      ],
    });
    expect(summary).toBe("Tool name is one of deploy.production and amount_cents at least 1000");
  });
});
