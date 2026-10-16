// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { RevisionHistory, isDraftRevision, type RevisionRow } from "./revision-history";

afterEach(cleanup);

function row(revision: number, fields: Partial<RevisionRow> = {}): RevisionRow {
  return {
    id: `revision-${revision}`,
    revision,
    is_active: false,
    created_at: "2026-10-01T09:00:00Z",
    created_by_actor_id: "author",
    ...fields,
  };
}

describe("draft revisions", () => {
  it("is a draft only when it was never active and is not archived", () => {
    expect(isDraftRevision(row(3))).toBe(true);
    expect(isDraftRevision(row(3, { activated_at: "2026-10-02T09:00:00Z" }))).toBe(false);
    expect(isDraftRevision(row(3, { archived_at: "2026-10-02T09:00:00Z" }))).toBe(false);
    expect(isDraftRevision(row(3, { is_active: true }))).toBe(false);
  });

  it("labels draft rows apart from inactive ones", () => {
    render(
      <RevisionHistory
        aria-label="Revisions"
        items={[
          row(3),
          row(2, { is_active: true, activated_at: "2026-10-01T09:00:00Z" }),
          row(1, { activated_at: "2026-09-30T09:00:00Z" }),
        ]}
        totalCount={3}
        hasMore={false}
        activeRevision={2}
      />,
    );
    expect(screen.getByText("Draft")).toBeTruthy();
    expect(screen.getByText("Active")).toBeTruthy();
    expect(screen.getByText("Inactive")).toBeTruthy();
  });
});
