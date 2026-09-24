import type { ReactNode } from "react";
import { cn } from "@/ds/lib/cn";
import { XIcon } from "@/ds/icons";
import { IconButton } from "./icon-button";

export interface SelectionBarProps {
  count: number;
  onClear: () => void;
  /* Bulk action buttons. */
  children?: ReactNode;
  className?: string;
}

/* Floating bulk-actions bar; renders only while something is selected. */
export function SelectionBar({
  count,
  onClear,
  children,
  className,
}: SelectionBarProps) {
  if (count === 0) return null;
  return (
    <div
      className={cn(
        "ds-root fixed bottom-6 left-1/2 z-30 -translate-x-1/2",
        "flex items-center gap-1 rounded-lg bg-surface-overlay py-1.5 pr-1.5 pl-3",
        "shadow-overlay ring-hairline",
        "animate-[ds-rise_160ms_var(--ds-ease-out)]",
        className,
      )}
    >
      <span className="text-body font-medium whitespace-nowrap text-ink">
        {count} selected
      </span>
      <IconButton aria-label="Clear selection" size="sm" onClick={onClear}>
        <XIcon width={14} height={14} />
      </IconButton>
      {children ? (
        <>
          <span className="mx-1 h-4 w-px shrink-0 bg-edge" />
          <div className="flex items-center gap-1.5">{children}</div>
        </>
      ) : null}
    </div>
  );
}
