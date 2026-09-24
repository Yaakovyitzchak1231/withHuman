import type { ComponentProps, ReactElement, ReactNode } from "react";
import { Tooltip as BaseTooltip } from "@base-ui/react/tooltip";
import { cn } from "@/ds/lib/cn";
import { Kbd } from "./kbd";

/* Wrap a view once; tooltips inside share the 500ms warm-up, so moving
 * between neighboring triggers opens instantly. */
export function TooltipProvider({ children }: { children: ReactNode }) {
  return (
    <BaseTooltip.Provider delay={500} closeDelay={0}>
      {children}
    </BaseTooltip.Provider>
  );
}

export interface TooltipProps
  extends Omit<ComponentProps<typeof BaseTooltip.Root>, "children"> {
  content: ReactNode;
  /* Optional trailing shortcut, e.g. "mod+K". */
  keys?: string;
  side?: "top" | "bottom" | "left" | "right";
  children: ReactElement;
}

export function Tooltip({
  content,
  keys,
  side = "top",
  children,
  ...rootProps
}: TooltipProps) {
  return (
    <BaseTooltip.Root {...rootProps}>
      <BaseTooltip.Trigger render={children} />
      <BaseTooltip.Portal>
        <BaseTooltip.Positioner side={side} sideOffset={6}>
          <BaseTooltip.Popup
            className={cn(
              /* ds-root re-establishes the baseline inside the portal. */
              "ds-root flex max-w-[280px] items-center gap-2 rounded-sm bg-ink px-2 py-1",
              "text-micro text-canvas shadow-md",
              "transition-[opacity,translate] duration-100 ease-standard",
              "data-starting-style:opacity-0 data-starting-style:translate-y-[2px]",
              "data-ending-style:opacity-0",
            )}
          >
            {content}
            {keys ? <Kbd keys={keys} tone="inverse" /> : null}
          </BaseTooltip.Popup>
        </BaseTooltip.Positioner>
      </BaseTooltip.Portal>
    </BaseTooltip.Root>
  );
}
