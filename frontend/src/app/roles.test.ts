import { describe, expect, it } from "vitest";
import { directRole } from "./roles";
import type { RoleAssignment } from "../types";

function assignment(overrides: Partial<RoleAssignment>): RoleAssignment {
  return {
    id: "a",
    principal_kind: "membership",
    principal_id: "m",
    permissions: [],
    is_owner: false,
    scope_kind: "organization",
    source: "manual",
    created_at: "2026-09-30T00:00:00Z",
    ...overrides,
  } as RoleAssignment;
}

describe("directRole", () => {
  it("names the one predefined role a member holds directly", () => {
    expect(directRole([assignment({ built_in_role: "reviewer" })])).toBe("reviewer");
  });

  it("ignores what a team or the directory grants", () => {
    expect(
      directRole([
        assignment({ built_in_role: "auditor" }),
        assignment({ id: "b", built_in_role: "admin", via_team_id: "t" }),
        assignment({ id: "c", built_in_role: "admin", source: "directory" }),
      ]),
    ).toBe("auditor");
  });

  it("has no answer for custom permissions or several grants", () => {
    expect(directRole([assignment({ permission_policy_id: "p" })])).toBeUndefined();
    expect(
      directRole([
        assignment({ built_in_role: "reviewer" }),
        assignment({ id: "b", built_in_role: "auditor" }),
      ]),
    ).toBeUndefined();
    expect(directRole([assignment({ built_in_role: "admin", scope_kind: "team", scope_id: "t" })])).toBeUndefined();
  });
});
