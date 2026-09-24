import { describe, expect, it } from "vitest";
import { diffLines, parsePatch, rowsToText } from "./line-diff";

describe("diffLines", () => {
  it("marks the changed lines and keeps the rest as context", () => {
    expect(diffLines("a\nb\nc\n", "a\nB\nc\nd")).toEqual([
      { kind: "context", text: "a" },
      { kind: "remove", text: "b" },
      { kind: "add", text: "B" },
      { kind: "context", text: "c" },
      { kind: "add", text: "d" },
      { kind: "hunk", text: "Removes the final newline" },
    ]);
  });
  it("handles empty sides", () => {
    expect(diffLines("", "x")).toEqual([{ kind: "add", text: "x" }]);
    expect(diffLines("x", "")).toEqual([{ kind: "remove", text: "x" }]);
    expect(diffLines("same", "same")).toEqual([{ kind: "context", text: "same" }]);
  });
  it("shows edits that only change the final newline", () => {
    expect(diffLines("same", "same\n")?.at(-1)).toEqual({ kind: "hunk", text: "Adds a final newline" });
    expect(diffLines("same\n", "same")?.at(-1)).toEqual({ kind: "hunk", text: "Removes the final newline" });
  });
  it("gives up above the cap", () => {
    expect(diffLines("x\n".repeat(2000), "y")).toBeNull();
  });
});

describe("parsePatch", () => {
  it("colors unified and apply_patch lines without dropping any", () => {
    const rows = parsePatch("*** Begin Patch\n*** Update File: a.go\n@@\n context\n-old\n+new\n*** End Patch");
    expect(rows.map((row) => row.kind)).toEqual(["hunk", "hunk", "hunk", "context", "remove", "add", "hunk"]);
    expect(rows[3].text).toBe("context");
    expect(rowsToText(rows)).toContain("-old\n+new");
  });
  it("treats file headers as hunks, not removals", () => {
    const rows = parsePatch("--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b");
    expect(rows.map((row) => row.kind)).toEqual(["hunk", "hunk", "hunk", "remove", "add"]);
  });
});
