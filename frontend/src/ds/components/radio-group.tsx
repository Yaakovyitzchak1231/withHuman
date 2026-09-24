import type { ComponentProps, ReactNode } from "react";
import { Radio } from "@base-ui/react/radio";
import { RadioGroup as BaseRadioGroup } from "@base-ui/react/radio-group";
import { cn } from "@/ds/lib/cn";
import { focusRing } from "@/ds/lib/focus";

/* Vertical labeled choices for consequential, mutually exclusive
 * options (policy modes). ToggleGroup stays the compact control for
 * cosmetic switches; RadioGroup is for choices that need explaining. */
export function RadioGroup({
  className,
  ...props
}: ComponentProps<typeof BaseRadioGroup>) {
  return (
    <BaseRadioGroup className={cn("flex flex-col gap-1", className)} {...props} />
  );
}

export interface RadioOptionProps {
  value: string;
  label: ReactNode;
  description?: ReactNode;
  disabled?: boolean;
  className?: string;
}

export function RadioOption({
  value,
  label,
  description,
  disabled,
  className,
}: RadioOptionProps) {
  return (
    <label
      className={cn(
        "flex cursor-pointer items-start gap-3 rounded-md px-2.5 py-2",
        "transition-colors duration-100",
        disabled
          ? "cursor-not-allowed opacity-50"
          : "hover:bg-fill-hover has-data-checked:bg-fill-selected",
        className,
      )}
    >
      <Radio.Root
        value={value}
        disabled={disabled}
        className={cn(
          /* The dot stays mounted and scales via color only; geometry
           * never shifts. */
          "mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full",
          "border border-edge-strong bg-surface",
          "transition-colors duration-100",
          "data-checked:border-accent data-checked:bg-accent",
          focusRing,
        )}
      >
        <Radio.Indicator
          keepMounted
          className={cn(
            "size-1.5 rounded-full bg-ink-on-accent",
            "opacity-0 transition-opacity duration-100 data-checked:opacity-100",
          )}
        />
      </Radio.Root>
      <span className="flex min-w-0 flex-col gap-0.5">
        <span className="text-body text-ink">{label}</span>
        {description ? (
          <span className="text-label font-normal text-ink-muted">
            {description}
          </span>
        ) : null}
      </span>
    </label>
  );
}

export interface RadioCardProps {
  value: string;
  label: ReactNode;
  description?: ReactNode;
  /* A quiet glyph above the label, naming the way at a glance. */
  icon?: ReactNode;
  disabled?: boolean;
  className?: string;
}

/* A choice as a tile, for a handful of ways to do one thing that each
 * open their own panel below (lay them out in a grid on the group).
 * The whole tile is the target; the radio sits in its corner. */
export function RadioCard({
  value,
  label,
  description,
  icon,
  disabled,
  className,
}: RadioCardProps) {
  return (
    <label
      className={cn(
        "group/radio-card relative flex cursor-pointer flex-col gap-3 rounded-xl p-4",
        "border-hairline border-edge bg-surface shadow-sm",
        "transition-[border-color,background-color,box-shadow] duration-100",
        disabled
          ? "cursor-not-allowed opacity-50"
          : "hover:border-edge-strong has-data-checked:border-accent has-data-checked:shadow-[0_0_0_0.5px_var(--color-accent)]",
        "has-focus-visible:outline-2 has-focus-visible:outline-offset-2 has-focus-visible:outline-accent",
        className,
      )}
    >
      <span className="flex items-start justify-between gap-3">
        {icon ? (
          <span className="flex size-8 items-center justify-center rounded-lg bg-fill-hover text-ink-secondary transition-colors duration-100 group-has-data-checked/radio-card:bg-accent group-has-data-checked/radio-card:text-ink-on-accent">
            {icon}
          </span>
        ) : (
          <span />
        )}
        <Radio.Root
          value={value}
          disabled={disabled}
          className={cn(
            "flex size-4 shrink-0 items-center justify-center rounded-full",
            "border border-edge-strong bg-surface",
            "transition-colors duration-100",
            "data-checked:border-accent data-checked:bg-accent",
            "outline-none",
          )}
        >
          <Radio.Indicator
            keepMounted
            className={cn(
              "size-1.5 rounded-full bg-ink-on-accent",
              "opacity-0 transition-opacity duration-100 data-checked:opacity-100",
            )}
          />
        </Radio.Root>
      </span>
      <span className="flex min-w-0 flex-col gap-1">
        <span className="text-body font-medium text-ink">{label}</span>
        {description ? (
          <span className="text-label font-normal text-ink-muted">
            {description}
          </span>
        ) : null}
      </span>
    </label>
  );
}
