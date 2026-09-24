import { describe, expect, it } from "vitest";
import { validateAgentSearch } from "./agent-search";

describe("agent page URLs", () => {
  it("restores a filtered instance page from a shared URL", () => {
    expect(
      validateAgentSearch({
        tab: "instances",
        q: " production ",
        status: "disabled",
        page: "2",
      }),
    ).toEqual({
      tab: "instances",
      q: "production",
      status: "disabled",
      page: 2,
    });
  });

  it("restores a filtered approval request page", () => {
    expect(
      validateAgentSearch({
        tab: "requests",
        q: "production",
        status: "pending",
        page: 4,
      }),
    ).toEqual({ tab: "requests", q: "production", status: "pending", page: 4 });
  });

  it("restores token search, status and pagination in Provisioner tokens", () => {
    expect(
      validateAgentSearch({
        tab: "provisioners",
        q: " WHP_…ABCD ",
        status: "revoked",
        page: "2",
      }),
    ).toEqual({
      tab: "provisioners",
      q: "WHP_…ABCD",
      status: "revoked",
      page: 2,
    });
    expect(
      validateAgentSearch({ tab: "provisioners", status: "pending", page: -1 }),
    ).toEqual({ tab: "provisioners" });
  });

  it("keeps Overview free of table filters", () => {
    expect(
      validateAgentSearch({ q: "production", status: "active", page: 2 }),
    ).toEqual({});
    expect(validateAgentSearch({ tab: "unknown" })).toEqual({});
  });

  it("rejects statuses belonging to the other list and invalid page offsets", () => {
    expect(validateAgentSearch({ status: "approved", page: -2 })).toEqual({});
    expect(
      validateAgentSearch({
        tab: "requests",
        status: "active",
        page: Infinity,
      }),
    ).toEqual({ tab: "requests" });
    expect(validateAgentSearch({ page: 1.5 })).toEqual({});
  });
});
