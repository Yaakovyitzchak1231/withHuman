import { describe, expect, it } from "vitest";
import type { Installation } from "../types";
import { updateTodos } from "./installation";

const current: Installation = {
  id: "8d3f0c4e-0000-4000-8000-000000000000",
  version: "1.0.4",
  latest_version: "1.0.4",
  update_available: false,
  advisories: [],
  checked_at: "2026-10-16T09:00:00Z",
};

describe("updateTodos", () => {
  it("lists nothing before the first read or when up to date", () => {
    expect(updateTodos(null)).toEqual([]);
    expect(updateTodos(current)).toEqual([]);
  });

  it("offers a newer release", () => {
    expect(updateTodos({ ...current, latest_version: "1.0.6", update_available: true })).toEqual([
      expect.objectContaining({ key: "installation:update", description: "Version 1.0.6 is available." }),
    ]);
  });

  it("puts a security fix first, as one item", () => {
    const advisory = { id: "GHSA-1", summary: "Fix", severity: "high", url: "https://example.com", fixed_in: "1.0.5" };
    expect(
      updateTodos({ ...current, latest_version: "1.0.6", update_available: true, advisories: [advisory, { ...advisory, id: "GHSA-2" }] }),
    ).toEqual([expect.objectContaining({ key: "installation:security", to: "/settings/version" })]);
  });
});
