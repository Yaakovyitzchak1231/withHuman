/*
 * A line diff for the review page: the rows a reader sees when a tool
 * call carries a change as a before and an after (Claude Code's Edit,
 * the filesystem server's edit_file) rather than as a patch. Plain LCS
 * over lines: the texts are edit snippets, not whole repositories, and
 * a cap keeps the table small enough to build synchronously.
 */
export type DiffRowKind = "context" | "add" | "remove" | "hunk";

export interface DiffRow {
  kind: DiffRowKind;
  text: string;
}

/* Above this many lines on either side the pair is shown as two blocks
 * instead of a diff. */
export const LINE_DIFF_LIMIT = 1500;

function splitLines(text: string): string[] {
  if (text === "") return [];
  const lines = text.split("\n");
  if (lines[lines.length - 1] === "") lines.pop();
  return lines;
}

/* Rows for a before and after, or null when either side is too long. */
export function diffLines(before: string, after: string): DiffRow[] | null {
  const a = splitLines(before);
  const b = splitLines(after);
  if (a.length > LINE_DIFF_LIMIT || b.length > LINE_DIFF_LIMIT) return null;
  /* Trim the common prefix and suffix first: most edits touch a few
   * lines in the middle, and the table only has to cover the rest. */
  let start = 0;
  while (start < a.length && start < b.length && a[start] === b[start]) start++;
  let endA = a.length;
  let endB = b.length;
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) {
    endA--;
    endB--;
  }
  const rows: DiffRow[] = a.slice(0, start).map((text) => ({ kind: "context", text }));
  const midA = a.slice(start, endA);
  const midB = b.slice(start, endB);
  const width = midB.length + 1;
  const table = new Uint32Array((midA.length + 1) * width);
  for (let i = midA.length - 1; i >= 0; i--) {
    for (let j = midB.length - 1; j >= 0; j--) {
      table[i * width + j] =
        midA[i] === midB[j]
          ? table[(i + 1) * width + j + 1] + 1
          : Math.max(table[(i + 1) * width + j], table[i * width + j + 1]);
    }
  }
  let i = 0;
  let j = 0;
  while (i < midA.length && j < midB.length) {
    if (midA[i] === midB[j]) {
      rows.push({ kind: "context", text: midA[i] });
      i++;
      j++;
    } else if (table[(i + 1) * width + j] >= table[i * width + j + 1]) {
      rows.push({ kind: "remove", text: midA[i] });
      i++;
    } else {
      rows.push({ kind: "add", text: midB[j] });
      j++;
    }
  }
  for (; i < midA.length; i++) rows.push({ kind: "remove", text: midA[i] });
  for (; j < midB.length; j++) rows.push({ kind: "add", text: midB[j] });
  for (const text of a.slice(endA)) rows.push({ kind: "context", text });
  if (before.endsWith("\n") !== after.endsWith("\n")) {
    rows.push({ kind: "hunk", text: after.endsWith("\n") ? "Adds a final newline" : "Removes the final newline" });
  }
  return rows;
}

const HUNK = /^(?:@@|diff --git|index |\*\*\* (?:Begin|End) Patch|\*\*\* (?:Update|Add|Delete|Move) File:|\*\*\* Move to:|Index: |={3,}$)/;

/* Rows for a patch the tool call already carries as text: unified diff
 * or Codex's apply_patch envelope. Every line keeps its text; the kind
 * only colors it. */
export function parsePatch(patch: string): DiffRow[] {
  return splitLines(patch).map((line) => {
    if (HUNK.test(line)) return { kind: "hunk", text: line };
    if (line.startsWith("+++ ") || line.startsWith("--- ")) return { kind: "hunk", text: line };
    if (line.startsWith("+")) return { kind: "add", text: line.slice(1) };
    if (line.startsWith("-")) return { kind: "remove", text: line.slice(1) };
    if (line.startsWith(" ")) return { kind: "context", text: line.slice(1) };
    return { kind: "context", text: line };
  });
}

/* The rows back as a unified-style text, for the copy button. */
export function rowsToText(rows: DiffRow[]): string {
  return rows
    .map((row) => {
      switch (row.kind) {
        case "add":
          return `+${row.text}`;
        case "remove":
          return `-${row.text}`;
        case "hunk":
          return row.text;
        default:
          return ` ${row.text}`;
      }
    })
    .join("\n");
}
