import { describe, expect, it } from "vitest";
import type { ReviewerTarget } from "@/ds/components/reviewer-target";
import type { EscalationDocumentBodyWire } from "../types";
import {
  countChanges,
  fromWire,
  newDraft,
  targetFromWire,
  targetToWire,
  toWire,
  type EscalationDocument,
} from "./escalation-document";

const TEAM_ID = "11111111-1111-4111-8111-111111111111";
const PRIYA_ID = "22222222-2222-4222-8222-222222222222";
const MARCUS_ID = "33333333-3333-4333-8333-333333333333";

const EVERYONE: ReviewerTarget = { type: "broadcast", id: TEAM_ID, name: "Everyone on Payments" };
const PRIYA: ReviewerTarget = { type: "user", id: PRIYA_ID, name: "Priya Natarajan" };
const PAYMENTS: ReviewerTarget = { type: "team", id: TEAM_ID, name: "Payments" };

const POLICY: EscalationDocumentBodyWire = {
  working_hours: [
    {
      id: "office",
      name: "Office hours",
      timezone: "Europe/London",
      intervals: [{ weekdays: ["mon", "tue", "wed", "thu", "fri"], start: "09:00", end: "17:30" }],
    },
  ],
  nodes: [
    { id: "n1", type: "defer", working_hours_id: "office", standard_only: true },
    { id: "n2", type: "level", targets: [{ type: "user", id: PRIYA_ID }], escalate_after: "15m" },
    {
      id: "n3",
      type: "if_else",
      conditions: [{ type: "urgency", urgency: "interrupt" }],
      then: [{ id: "n4", type: "level", targets: [{ type: "broadcast" }], escalate_after: "90m", raise_urgency: true }],
      else: [{ id: "n5", type: "defer", working_hours_id: "office" }],
    },
  ],
  repeat: { times: 2, after: "1h" },
};

describe("fromWire and toWire", () => {
  it("round trips a team policy, broadcast included", () => {
    const document = fromWire(POLICY, [EVERYONE, PRIYA]);
    expect(document.nodes[1]).toMatchObject({
      type: "level",
      targets: [PRIYA],
      escalateAfterSeconds: 900,
      raiseUrgency: false,
    });
    const branch = document.nodes[2];
    if (branch.type !== "if_else") throw new Error("expected a branch");
    expect(branch.thenNodes[0]).toMatchObject({
      type: "level",
      targets: [EVERYONE],
      escalateAfterSeconds: 5_400,
      raiseUrgency: true,
    });
    expect(document.repeat).toEqual({ enabled: true, times: 2, afterSeconds: 3_600 });
    expect(toWire(document)).toEqual(POLICY);
  });

  it("names a broadcast even without a directory entry", () => {
    expect(targetFromWire({ type: "broadcast" }, [])).toEqual({
      type: "broadcast",
      id: "",
      name: "Everyone on the team",
    });
    expect(targetToWire(EVERYONE)).toEqual({ type: "broadcast" });
  });

  it("drops an escalation path picked as a level target", () => {
    const document = fromWire(
      {
        working_hours: [],
        nodes: [{ id: "n1", type: "level", targets: [{ type: "team", id: TEAM_ID }], escalate_after: "10m" }],
      },
      [PAYMENTS],
    );
    const level = document.nodes[0];
    if (level.type !== "level") throw new Error("expected a level");
    level.targets.push({ type: "escalation_path", id: "oncall", name: "On-call" });
    expect(toWire(document).nodes[0].targets).toEqual([{ type: "team", id: TEAM_ID }]);
    expect(targetToWire({ type: "escalation_path", id: "oncall", name: "On-call" })).toBeNull();
  });

  it("falls back to the level default when a duration is unreadable", () => {
    const document = fromWire(
      { working_hours: [], nodes: [{ id: "n1", type: "level", targets: [], escalate_after: "soon" }] },
      [],
    );
    expect(document.nodes[0]).toMatchObject({ escalateAfterSeconds: 900 });
    expect(document.repeat).toEqual({ enabled: false, times: 2, afterSeconds: 3_600 });
  });

  it("names a departed member by id when the directory lacks them", () => {
    const document = fromWire(
      { working_hours: [], nodes: [{ id: "n1", type: "level", targets: [{ type: "user", id: MARCUS_ID }] }] },
      [],
    );
    const level = document.nodes[0];
    if (level.type !== "level") throw new Error("expected a level");
    expect(level.targets[0].name).toBe("33333333…");
  });
});

describe("countChanges", () => {
  const saved: EscalationDocument = fromWire(POLICY, [EVERYONE, PRIYA]);

  it("counts each part once", () => {
    const draft: EscalationDocument = {
      ...saved,
      repeat: { ...saved.repeat, times: 3 },
      workingHours: [{ ...saved.workingHours[0], end: "18:00" }],
    };
    expect(countChanges(saved, saved, true)).toBe(0);
    expect(countChanges(draft, saved, true)).toBe(2);
  });

  it("treats a first document with steps as worth saving", () => {
    const empty = newDraft();
    expect(countChanges(empty, empty, false)).toBe(0);
    const withSteps: EscalationDocument = { ...saved };
    expect(countChanges(withSteps, withSteps, false)).toBe(1);
    expect(countChanges(withSteps, withSteps, true)).toBe(0);
  });
});
