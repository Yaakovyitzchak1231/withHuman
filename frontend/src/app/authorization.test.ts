import { describe, expect, it } from "vitest";
import type { Grant, Principal } from "../types";
import { can, holdsAnywhere, scopeOf } from "./authorization";

function principalWith(grants: Grant[]): Principal {
  return {
    actor_type: "human",
    actor_id: "user-1",
    organization_id: "org-1",
    membership_id: "member-1",
    permissions: Array.from(new Set(grants.map((grant) => grant.permission))),
    grants,
  };
}

const organizationAdmin = principalWith([
  { permission: "pipeline.write", scope_kind: "organization" },
]);
const refundBotAdmin = principalWith([
  { permission: "pipeline.write", scope_kind: "agent", scope_id: "refund-bot" },
]);
const paymentsLead = principalWith([
  { permission: "team.member.write", scope_kind: "team", scope_id: "team-payments" },
]);

describe("can", () => {
  it("lets an organization grant cover the organization, a team, and an agent", () => {
    expect(can(organizationAdmin, "pipeline.write")).toBe(true);
    expect(can(organizationAdmin, "pipeline.write", { kind: "organization" })).toBe(true);
    expect(can(organizationAdmin, "pipeline.write", { kind: "team", id: "team-payments" })).toBe(
      true,
    );
    expect(
      can(organizationAdmin, "pipeline.write", { kind: "agent", id: "refund-bot" }),
    ).toBe(true);
  });

  it("limits an agent grant to the agent with that slug", () => {
    expect(can(refundBotAdmin, "pipeline.write", { kind: "agent", id: "refund-bot" })).toBe(
      true,
    );
    expect(can(refundBotAdmin, "pipeline.write", { kind: "agent", id: "deploy-bot" })).toBe(
      false,
    );
    expect(can(refundBotAdmin, "pipeline.write", { kind: "team", id: "refund-bot" })).toBe(false);
    expect(can(refundBotAdmin, "pipeline.write", { kind: "organization" })).toBe(false);
    expect(can(refundBotAdmin, "pipeline.write")).toBe(false);
  });

  it("limits a team grant to that team", () => {
    expect(can(paymentsLead, "team.member.write", { kind: "team", id: "team-payments" })).toBe(
      true,
    );
    expect(can(paymentsLead, "team.member.write", { kind: "team", id: "team-ops" })).toBe(false);
    expect(
      can(paymentsLead, "team.member.write", { kind: "agent", id: "team-payments" }),
    ).toBe(false);
    expect(can(paymentsLead, "team.member.write")).toBe(false);
  });

  it("refuses a permission the principal does not hold at all", () => {
    expect(can(organizationAdmin, "pipeline.activate")).toBe(false);
    expect(can(organizationAdmin, "pipeline.activate", { kind: "agent", id: "x" })).toBe(
      false,
    );
  });
});

describe("holdsAnywhere", () => {
  it("is true for a grant at any scope and false otherwise", () => {
    expect(holdsAnywhere(organizationAdmin, "pipeline.write")).toBe(true);
    expect(holdsAnywhere(refundBotAdmin, "pipeline.write")).toBe(true);
    expect(holdsAnywhere(paymentsLead, "team.member.write")).toBe(true);
    expect(holdsAnywhere(paymentsLead, "pipeline.write")).toBe(false);
  });
});

describe("scopeOf", () => {
  it("is all when an organization grant exists, whatever else is held", () => {
    const mixed = principalWith([
      { permission: "pipeline.read", scope_kind: "agent", scope_id: "refund-bot" },
      { permission: "pipeline.read", scope_kind: "organization" },
    ]);
    expect(scopeOf(mixed, "pipeline.read")).toBe("all");
  });

  it("lists the scoped grants once each", () => {
    const scoped = principalWith([
      { permission: "pipeline.read", scope_kind: "agent", scope_id: "refund-bot" },
      { permission: "pipeline.read", scope_kind: "agent", scope_id: "refund-bot" },
      { permission: "pipeline.read", scope_kind: "team", scope_id: "team-payments" },
      { permission: "pipeline.write", scope_kind: "organization" },
    ]);
    expect(scopeOf(scoped, "pipeline.read")).toEqual([
      { kind: "agent", id: "refund-bot" },
      { kind: "team", id: "team-payments" },
    ]);
  });

  it("is empty when the permission is not held", () => {
    expect(scopeOf(paymentsLead, "pipeline.read")).toEqual([]);
  });
});
