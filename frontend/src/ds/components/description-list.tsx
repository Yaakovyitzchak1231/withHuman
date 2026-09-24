import type { ComponentProps, ReactNode } from "react";
import { cn } from "@/ds/lib/cn";
import { CopyButton } from "./copy-button";

export function DescriptionList({
  className,
  ...props
}: ComponentProps<"dl">) {
  return <dl className={cn("flex flex-col", className)} {...props} />;
}

export function DescriptionItem({
  term,
  mono = false,
  copy,
  className,
  children,
}: {
  term: string;
  /* Machine values (ids, tools, hashes) render in Roboto Mono. */
  mono?: boolean;
  /* Hovering the row reveals a copy button, left of the value so right
   * edges stay aligned. String values are copyable by default; pass a
   * string to override what is copied, or false to opt out. */
  copy?: string | false;
  className?: string;
  children: ReactNode;
}) {
  const copyValue =
    copy === false
      ? null
      : (copy ?? (typeof children === "string" ? children : null));
  return (
    <div
      className={cn(
        "group/item flex items-center justify-between gap-6 py-1.5",
        className,
      )}
    >
      <dt className="shrink-0 text-label leading-5 font-normal text-ink-muted">
        {term}
      </dt>
      {/* row-reverse keeps the value first in the DOM (it owns the
       * baseline) while the copy button renders to its left. */}
      <dd className="flex min-w-0 flex-row-reverse items-center justify-start gap-1">
        <span
          className={cn(
            "min-w-0 truncate text-right",
            mono
              ? "font-mono text-[12px] leading-5 text-ink-secondary"
              : "text-body text-ink",
          )}
        >
          {children}
        </span>
        {copyValue !== null ? (
          <span
            className={cn(
              /* Fixed layout box the 26px button visually overflows, so
               * copyable rows keep the same height as plain ones. */
              "flex h-5 w-[26px] items-center justify-center",
              /* Reveal on hover, keyboard focus, or while the copied tick
               * shows; a plain mouse-click focus must not pin it. */
              "opacity-0 transition-opacity duration-100",
              "group-hover/item:opacity-100 has-[:focus-visible]:opacity-100",
              "has-[[data-copied]]:opacity-100",
            )}
          >
            <CopyButton
              value={copyValue}
              aria-label={`Copy ${term.toLowerCase()}`}
            />
          </span>
        ) : null}
      </dd>
    </div>
  );
}
