import type { ReactNode } from "react";
import { cn } from "@/ds/lib/cn";
import { Badge } from "./badge";
import { BlockPositionLabel, type BlockPosition } from "./block-card";
import { IconButton } from "./icon-button";
import { ArrowUpRightIcon, ChevronDownIcon } from "@/ds/icons";

/*
 * The read-only face shared by every block kind: same anatomy as a
 * collapsed editing card, none of the controls. Serves the scope not
 * being edited and the whole pipeline while viewing. With
 * onExpandedChange the header toggles a body that lays out the same
 * details the editing card does, as plain text, so a reader never has
 * to enter editing to see them.
 */
export function BlockSummaryCard({
  position,
  title,
  subtitle,
  enabled,
  onNavigate,
  expanded = false,
  onExpandedChange,
  children,
}: {
  position: BlockPosition;
  title: ReactNode;
  /* The one-line summary under the name, in mono. */
  subtitle: ReactNode;
  enabled: boolean;
  /* Takes the user to the scope's own editor. */
  onNavigate?: () => void;
  expanded?: boolean;
  /* Present when the card opens to its details. */
  onExpandedChange?: (expanded: boolean) => void;
  /* The details, shown when expanded. */
  children?: ReactNode;
}) {
  const expandable = onExpandedChange !== undefined;
  const open = expandable && expanded;
  return (
    <div
      className={cn(
        "rounded-lg border-hairline border-edge bg-surface shadow-sm",
        !enabled && "opacity-60",
      )}
    >
      <div
        role={expandable ? "button" : undefined}
        tabIndex={expandable ? 0 : undefined}
        aria-expanded={expandable ? open : undefined}
        onClick={expandable ? () => onExpandedChange(!open) : undefined}
        onKeyDown={
          expandable
            ? (event) => {
                if (event.key === "Enter" || event.key === " ") {
                  event.preventDefault();
                  onExpandedChange(!open);
                }
              }
            : undefined
        }
        className={cn(
          "flex w-full items-center gap-3 rounded-lg px-4 py-2.5 text-left",
          expandable && "cursor-pointer transition-colors duration-100 hover:bg-fill-hover/50",
          open && "rounded-b-none",
        )}
      >
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
            <BlockPositionLabel position={position} />
            <span className="font-medium text-ink">{title}</span>
            {!enabled ? <Badge>Off</Badge> : null}
          </div>
          <p className="mt-0.5 truncate font-mono text-[11px] text-ink-muted">{subtitle}</p>
        </div>
        {onNavigate ? (
          <span onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
            <IconButton aria-label="Edit in its own pipeline" size="sm" onClick={onNavigate}>
              <ArrowUpRightIcon width={13} height={13} />
            </IconButton>
          </span>
        ) : null}
        {expandable ? (
          <ChevronDownIcon
            width={14}
            height={14}
            aria-hidden
            className={cn(
              "shrink-0 text-ink-faint transition-transform duration-150",
              open && "rotate-180",
            )}
          />
        ) : null}
      </div>

      {open ? (
        <div className="flex flex-col gap-4 border-t-hairline border-edge px-4 py-4">{children}</div>
      ) : null}
    </div>
  );
}

/* One labelled value in a summary body, in the editing card's order. */
export function SummaryField({
  label,
  children,
  className,
}: {
  label: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex flex-col gap-1.5", className)}>
      <span className="text-label text-ink-secondary">{label}</span>
      {children}
    </div>
  );
}

/* A value that is text, or the muted note that stands in for none. */
export function SummaryText({ children, empty }: { children?: string; empty: string }) {
  return children ? (
    <p className="whitespace-pre-wrap text-body text-ink">{children}</p>
  ) : (
    <p className="text-body text-ink-muted">{empty}</p>
  );
}

/* The chosen entries of a set of switches or checkboxes, as a
 * sentence: "Agent reasoning, Request context", or the note for none. */
export function SummaryChoices({ chosen, empty }: { chosen: string[]; empty: string }) {
  return chosen.length ? (
    <p className="text-body text-ink">{chosen.join(", ")}</p>
  ) : (
    <p className="text-body text-ink-muted">{empty}</p>
  );
}
