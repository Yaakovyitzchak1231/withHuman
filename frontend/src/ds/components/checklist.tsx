import type { ComponentProps } from "react";
import { cn } from "@/ds/lib/cn";
import { CheckIcon } from "@/ds/icons";

export interface ChecklistItem {
  label: string;
  done: boolean;
}

export interface ChecklistProps extends ComponentProps<"ul"> {
  items: ChecklistItem[];
}

/*
 * Things to do in any order, each done or not. A Stepper is for a flow
 * whose steps follow one another; a checklist has no current item. A done
 * item carries the Stepper's check node and recedes to muted ink; an open
 * one keeps a hollow node and ink, so what is left reads first. The state
 * word is kept for assistive technology.
 */
export function Checklist({ items, className, ...props }: ChecklistProps) {
  return (
    <ul className={cn("flex flex-col gap-1.5", className)} {...props}>
      {items.map((item) => (
        <li key={item.label} className="flex items-center gap-2">
          <span
            aria-hidden
            className={cn(
              "flex size-4 shrink-0 items-center justify-center rounded-full",
              item.done ? "bg-approve-subtle text-approve" : "border-hairline border-edge-strong bg-surface",
            )}
          >
            {item.done ? <CheckIcon width={10} height={10} strokeWidth={2.5} /> : null}
          </span>
          <span className={cn("min-w-0 truncate text-label", item.done ? "text-ink-muted" : "text-ink")}>
            {item.label}
          </span>
          <span className="sr-only">{item.done ? "Done" : "Not done"}</span>
        </li>
      ))}
    </ul>
  );
}
