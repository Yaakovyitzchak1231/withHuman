import type { ComponentProps, ReactNode } from "react";
import { Switch as BaseSwitch } from "@base-ui/react/switch";
import { cn } from "@/ds/lib/cn";
import { focusRing } from "@/ds/lib/focus";

export interface SwitchProps extends ComponentProps<typeof BaseSwitch.Root> {
  /* Optional trailing label; clicking it toggles the switch. */
  label?: ReactNode;
}

export function Switch({ label, className, ...props }: SwitchProps) {
  const control = (
    <BaseSwitch.Root
      className={cn(
        "relative inline-flex h-4 w-7 shrink-0 cursor-pointer items-center rounded-full",
        "bg-fill-active ring-hairline",
        "transition-[background-color] duration-160 ease-standard",
        "data-checked:bg-accent",
        "disabled:pointer-events-none disabled:opacity-50",
        focusRing,
        className,
      )}
      {...props}
    >
      <BaseSwitch.Thumb
        className={cn(
          "block size-3 translate-x-0.5 rounded-full bg-knob shadow-sm",
          "transition-transform duration-160 ease-standard",
          "data-checked:translate-x-[14px]",
        )}
      />
    </BaseSwitch.Root>
  );

  if (!label) return control;
  return (
    <label className="inline-flex cursor-pointer items-center gap-2.5">
      {control}
      <span className="text-body text-ink select-none">{label}</span>
    </label>
  );
}
