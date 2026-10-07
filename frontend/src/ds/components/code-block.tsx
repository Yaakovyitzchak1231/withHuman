import type { ComponentProps } from "react";
import { cn } from "@/ds/lib/cn";
import { CopyButton } from "./copy-button";

export interface CodeBlockProps extends ComponentProps<"div"> {
  code: string;
  /* Header line, e.g. the tool name. Mono, uppercase-free, quiet. */
  label?: string;
  copyable?: boolean;
  /* What the copy button writes, when it should carry more than the
   * displayed code (e.g. the label folded in). Defaults to code. */
  copyValue?: string;
  /* Fold long lines instead of scrolling sideways: for prose-like
   * machine output (an error message) in a narrow place. */
  wrap?: boolean;
  /* On the canvas rather than inside a card: surface, hairline and a
   * small shadow, the Card grammar. Off, the block is a recessed well
   * for a place that is already raised. */
  raised?: boolean;
}

/* The code block: machine content with a hover-revealed copy
 * affordance. The hairline is a border, not a ring, so a clipping
 * ancestor can never shave it off. */
export function CodeBlock({
  code,
  label,
  copyable = true,
  copyValue,
  wrap = false,
  raised = false,
  className,
  ...props
}: CodeBlockProps) {
  const copyBody = copyValue ?? code;
  return (
    <div
      className={cn(
        "group/code relative overflow-hidden border-hairline border-edge",
        raised ? "rounded-lg bg-surface shadow-sm" : "rounded-md bg-canvas",
        className,
      )}
      {...props}
    >
      {label ? (
        <div className="flex h-8 items-center justify-between border-b-hairline border-edge pr-1.5 pl-3">
          <span className="font-mono text-[11px] text-ink-muted">{label}</span>
          {copyable ? (
            <CopyButton value={copyBody} aria-label="Copy" />
          ) : null}
        </div>
      ) : null}
      <pre
        className={cn(
          "p-3 font-mono text-[12px] leading-[19px] text-ink-secondary",
          wrap ? "break-all whitespace-pre-wrap" : "overflow-x-auto",
        )}
      >
        {code}
      </pre>
      {!label && copyable ? (
        <span
          className={cn(
            "absolute top-1.5 right-1.5 opacity-0 transition-opacity duration-100",
            "group-hover/code:opacity-100 has-[:focus-visible]:opacity-100",
            "has-[[data-copied]]:opacity-100",
          )}
        >
          <CopyButton value={copyBody} aria-label="Copy" />
        </span>
      ) : null}
    </div>
  );
}
