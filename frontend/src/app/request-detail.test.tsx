// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import type { ApprovalRequest } from "../types";
import { humanizeTool } from "./format";
import { requestMoments, requestTitle } from "./request-detail";

const approved = {
  id: "request-1",
  agent_name: "Test agent",
  agent_slug: "test-agent",
  agent_instance_name: "Test instance",
  tool: "refund",
  arguments: {},
  status: "approved",
  created_at: "2026-09-21T10:00:00Z",
  deadline_at: "2026-09-21T11:00:00Z",
  decision: { id: "decision-1", status: "approved", channel: "web", decided_at: "2026-09-21T10:02:00Z" },
} as ApprovalRequest;

describe("requestMoments", () => {
  const now = new Date("2026-09-21T12:00:00Z").getTime();

  it("leaves out use and result until the agent reports them", () => {
    const keys = requestMoments(approved, null, now).map((moment) => moment.key);
    expect(keys).toEqual(["requested", "decided"]);
  });

  it("adds the use and the result in time order", () => {
    const moments = requestMoments(
      {
        ...approved,
        consumption: {
          consumed_at: "2026-09-21T10:02:05Z",
          outcome: { status: "failed", note: "Card declined", reported_at: "2026-09-21T10:02:07Z" },
        },
      },
      null,
      now,
    );
    expect(moments.map((moment) => moment.key)).toEqual(["requested", "decided", "used", "result"]);
    const result = moments[3];
    expect(result.tone).toBe("deny");
    expect(result.title).toBe("Call failed");
    expect(result.detail).toBe("Card declined");
  });
});

describe("requestTitle", () => {
  const refund = {
    ...approved,
    tool: "stripe.refunds.create",
    arguments: { amount: 4900 },
    presentation: { source: "built_in", title: "Refund {{/amount}}", fields: [] },
  } as ApprovalRequest;

  it("names the request by its summary when it has one", () => {
    const summarized = {
      ...refund,
      summary: { text: "Refund $49.00 on charge ch_3N1x for a faulty bag", generated_at: "2026-09-21T10:00:03Z" },
    };
    expect(requestTitle(summarized)).toBe("Refund $49.00 on charge ch_3N1x for a faulty bag");
  });

  it("falls back to the presentation title, then the tool", () => {
    expect(requestTitle(refund)).toBe("Refund 4900");
    expect(requestTitle({ ...refund, presentation: undefined })).toBe(humanizeTool("stripe.refunds.create"));
  });
});
