import { describe, expect, it } from "vitest";
import type { ApprovalRouting, RoutingEscalation } from "../types";
import { nextEscalation } from "./escalation-countdown";

function routingWith(escalation: RoutingEscalation | null): ApprovalRouting {
  return {
    outcome: "escalated",
    reason: "pipeline_default",
    urgency: "standard",
    escalation_path_key: "oncall",
    escalation_path_name: "On-call",
    created_at: "2026-09-19T10:00:00Z",
    targets: [],
    added_reviewers: [],
    team_policies: [{ team_id: "t1", team_name: "Payments", policy_revision_id: "r1", revision: 1 }],
    escalation,
  };
}

function progress(overrides: Partial<RoutingEscalation> = {}): RoutingEscalation {
  return {
    sequence: 1,
    phase: "level",
    urgency: "standard",
    level_ordinal: 1,
    level_count: 3,
    repeat: 0,
    next_escalation_at: "2026-09-19T10:15:00Z",
    updated_at: "2026-09-19T10:00:00Z",
    teams: [],
    ...overrides,
  };
}

describe("nextEscalation", () => {
  it("is nothing without progress", () => {
    expect(nextEscalation(undefined)).toBeNull();
    expect(nextEscalation(routingWith(null))).toBeNull();
  });

  it("reads the path's level timer", () => {
    expect(nextEscalation(routingWith(progress()))).toEqual({
      kind: "next_level",
      at: "2026-09-19T10:15:00Z",
    });
  });

  it("knows when the last level's timer ends the walk", () => {
    expect(nextEscalation(routingWith(progress({ level_ordinal: 3 })))).toEqual({
      kind: "last_level",
      at: "2026-09-19T10:15:00Z",
    });
  });

  it("names deferred and repeat waits", () => {
    expect(nextEscalation(routingWith(progress({ phase: "deferred" })))?.kind).toBe("deferred");
    expect(nextEscalation(routingWith(progress({ phase: "repeat_wait" })))?.kind).toBe("repeat");
  });

  it("picks the soonest walker and names its team", () => {
    const routing = routingWith(
      progress({
        teams: [
          {
            team_id: "t1",
            phase: "level",
            level_ordinal: 1,
            level_count: 2,
            repeat: 0,
            next_escalation_at: "2026-09-19T10:05:00Z",
          },
        ],
      }),
    );
    expect(nextEscalation(routing)).toEqual({
      kind: "next_level",
      at: "2026-09-19T10:05:00Z",
      team: { id: "t1", name: "Payments" },
    });
  });

  it("ignores exhausted walkers and reports a pause when nothing is armed", () => {
    expect(
      nextEscalation(routingWith(progress({ phase: "exhausted", next_escalation_at: undefined }))),
    ).toBeNull();
    expect(
      nextEscalation(
        routingWith(
          progress({
            phase: "exhausted",
            next_escalation_at: undefined,
            teams: [{ team_id: "t1", phase: "paused", level_ordinal: 1, level_count: 2, repeat: 0 }],
          }),
        ),
      ),
    ).toEqual({ kind: "paused" });
  });
});
