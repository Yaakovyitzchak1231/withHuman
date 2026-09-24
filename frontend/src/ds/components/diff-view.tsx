import { useMemo } from "react";
import { cn } from "@/ds/lib/cn";
import { diffLines, parsePatch, rowsToText, type DiffRow } from "@/ds/lib/line-diff";
import { CodeBlock } from "./code-block";
import { CopyButton } from "./copy-button";

/*
 * A change, read as a diff. Either a patch the tool call carries as
 * text (unified diff, Codex's apply_patch envelope) or a before and an
 * after that the view lines up itself. Rows are colored with the
 * approve and deny fills the rest of the product uses for added and
 * removed, and nothing is elided: an edit under review is read whole.
 */
export interface DiffViewProps {
  /* A patch as text. Takes precedence over before and after. */
  patch?: string;
  before?: string;
  after?: string;
  /* Header line, like CodeBlock's. */
  label?: string;
  raised?: boolean;
  className?: string;
}

const ROW_CLASSES: Record<DiffRow["kind"], string> = {
  context: "text-ink-secondary",
  add: "bg-approve-subtle text-ink",
  remove: "bg-deny-subtle text-ink",
  hunk: "text-ink-faint",
};

const SIGNS: Record<DiffRow["kind"], string> = { context: " ", add: "+", remove: "-", hunk: " " };

export function DiffView({ patch, before, after, label, raised = false, className }: DiffViewProps) {
  const rows = useMemo(
    () => (patch !== undefined ? parsePatch(patch) : diffLines(before ?? "", after ?? "")),
    [patch, before, after],
  );
  /* Too long to line up: the two sides stand as blocks, still whole. */
  if (rows === null) {
    return (
      <div className={cn("flex flex-col gap-2", className)}>
        <CodeBlock code={before ?? ""} label={label ? `${label}: before` : "Before"} raised={raised} wrap />
        <CodeBlock code={after ?? ""} label={label ? `${label}: after` : "After"} raised={raised} wrap />
      </div>
    );
  }
  const text = rowsToText(rows);
  return (
    <div
      className={cn(
        "group/diff relative overflow-hidden border-hairline border-edge",
        raised ? "rounded-lg bg-surface shadow-sm" : "rounded-md bg-canvas",
        className,
      )}
    >
      {label ? (
        <div className="flex h-8 items-center justify-between border-b-hairline border-edge pr-1.5 pl-3">
          <span className="font-mono text-[11px] text-ink-muted">{label}</span>
          <CopyButton value={text} aria-label="Copy diff" />
        </div>
      ) : (
        <span className="absolute top-1.5 right-1.5 opacity-0 transition-opacity duration-100 group-hover/diff:opacity-100 has-[:focus-visible]:opacity-100 has-[[data-copied]]:opacity-100">
          <CopyButton value={text} aria-label="Copy diff" />
        </span>
      )}
      <div className="overflow-x-auto py-2 font-mono text-[12px] leading-[19px]">
        {rows.length === 0 ? (
          <p className="px-3 text-ink-faint">No change</p>
        ) : (
          rows.map((row, index) => (
            <div key={index} className={cn("flex min-w-max", ROW_CLASSES[row.kind])}>
              <span aria-hidden className="w-6 shrink-0 select-none pl-2 text-ink-faint">
                {SIGNS[row.kind]}
              </span>
              <span className="pr-3 whitespace-pre">{row.text || " "}</span>
            </div>
          ))
        )}
      </div>
    </div>
  );
}
