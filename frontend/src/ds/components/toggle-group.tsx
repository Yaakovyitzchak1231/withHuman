import type { ComponentProps } from "react";
import { Toggle } from "@base-ui/react/toggle";
import { ToggleGroup as BaseToggleGroup } from "@base-ui/react/toggle-group";
import { cn } from "@/ds/lib/cn";
import { focusRing } from "@/ds/lib/focus";

/*
 * Single-select segmented control (theme picker, view switcher).
 * Exactly one segment is always pressed: clicking the active segment
 * again does not empty the group.
 */
export interface ToggleGroupProps
  extends Omit<
    ComponentProps<typeof BaseToggleGroup>,
    "value" | "defaultValue" | "onValueChange"
  > {
  value: string;
  onValueChange: (value: string) => void;
}

export function ToggleGroup({
  value,
  onValueChange,
  className,
  children,
  ...props
}: ToggleGroupProps) {
  return (
    <BaseToggleGroup
      value={[value]}
      onValueChange={(groupValue: string[]) => {
        const next = groupValue[groupValue.length - 1];
        if (next !== undefined) onValueChange(next);
      }}
      className={cn(
        "inline-flex items-center gap-0.5 rounded-md border-hairline border-edge bg-canvas p-0.5",
        className,
      )}
      {...props}
    >
      {children}
    </BaseToggleGroup>
  );
}

export function ToggleGroupItem({
  className,
  ...props
}: ComponentProps<typeof Toggle>) {
  return (
    <Toggle
      className={cn(
        "flex h-6 min-w-7 cursor-pointer items-center justify-center gap-1.5",
        "rounded-[5px] px-1.5 text-label whitespace-nowrap text-ink-muted select-none",
        "transition-colors duration-100",
        "hover:bg-fill-hover hover:text-ink-secondary",
        "data-pressed:bg-control data-pressed:text-ink data-pressed:shadow-sm data-pressed:ring-hairline",
        "data-disabled:pointer-events-none data-disabled:opacity-50",
        focusRing,
        className,
      )}
      {...props}
    />
  );
}
