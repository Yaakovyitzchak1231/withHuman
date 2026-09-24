import type { ComponentProps, ReactNode } from "react";
import { Checkbox as BaseCheckbox } from "@base-ui/react/checkbox";
import { cn } from "@/ds/lib/cn";
import { focusRing } from "@/ds/lib/focus";

export interface CheckboxProps extends ComponentProps<typeof BaseCheckbox.Root> {
  /* Optional trailing label; clicking it toggles the checkbox. */
  label?: ReactNode;
}

export function Checkbox({ label, className, ...props }: CheckboxProps) {
  const control = (
    <BaseCheckbox.Root
      className={cn(
        "flex size-[15px] shrink-0 cursor-pointer items-center justify-center rounded-xs",
        "border-hairline border-edge-strong bg-surface",
        "transition-[background-color,border-color] duration-100 ease-standard",
        "hover:border-ink-muted",
        "data-checked:border-accent data-checked:bg-accent",
        "data-indeterminate:border-accent data-indeterminate:bg-accent",
        "disabled:pointer-events-none disabled:opacity-50",
        focusRing,
        className,
      )}
      {...props}
    >
      {/* keepMounted so the box's content, and therefore its baseline,
       * never changes: toggling must not move surrounding layout. The
       * checkmark draws via stroke-dashoffset; fully offset it is
       * invisible, which doubles as the unchecked state. */}
      <BaseCheckbox.Indicator keepMounted className="flex text-ink-on-accent">
        <svg
          width="11"
          height="11"
          viewBox="0 0 16 16"
          fill="none"
          stroke="currentColor"
          strokeWidth="2.5"
          strokeLinecap="round"
          strokeLinejoin="round"
          aria-hidden
        >
          <path
            d="M3.2 8.4 6.5 11.7 12.8 4.7"
            className="in-data-indeterminate:hidden [stroke-dasharray:15] [stroke-dashoffset:15] transition-[stroke-dashoffset] duration-160 ease-standard in-data-checked:[stroke-dashoffset:0]"
          />
          <path
            d="M3.5 8h9"
            className="hidden in-data-indeterminate:block"
          />
        </svg>
      </BaseCheckbox.Indicator>
    </BaseCheckbox.Root>
  );

  if (!label) return control;
  return (
    /* The box sits on the label's first line, so a label that wraps
     * still reads as one row with its box rather than a box floating
     * beside a paragraph. */
    <label className="inline-flex cursor-pointer items-start gap-2.5">
      <span className="flex h-5 shrink-0 items-center">{control}</span>
      <span className="text-body text-ink select-none">{label}</span>
    </label>
  );
}
