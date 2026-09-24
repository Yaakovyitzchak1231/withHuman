import type { ComponentProps } from "react";
import { Tabs as BaseTabs } from "@base-ui/react/tabs";
import { cn } from "@/ds/lib/cn";
import { focusRing } from "@/ds/lib/focus";

export const Tabs = BaseTabs.Root;

/* Underline tabs: quiet labels on a hairline baseline, one sliding
 * ink indicator shared by pointer and keyboard. */
export function TabsList({
  className,
  children,
  ...props
}: ComponentProps<typeof BaseTabs.List>) {
  return (
    <BaseTabs.List
      className={cn(
        "relative flex gap-1 border-b-hairline border-edge",
        className,
      )}
      {...props}
    >
      {children}
      <BaseTabs.Indicator
        className={cn(
          "absolute bottom-[-0.5px] left-0 h-[2px] rounded-full bg-ink",
          "w-(--active-tab-width) translate-x-(--active-tab-left)",
          "transition-[translate,width] duration-160 ease-standard",
        )}
      />
    </BaseTabs.List>
  );
}

export function Tab({
  className,
  ...props
}: ComponentProps<typeof BaseTabs.Tab>) {
  return (
    <BaseTabs.Tab
      className={cn(
        "flex h-8 cursor-pointer items-center gap-1.5 rounded-sm px-2.5",
        "text-body text-ink-muted whitespace-nowrap select-none",
        "transition-colors duration-100",
        "hover:text-ink data-selected:text-ink",
        "data-disabled:pointer-events-none data-disabled:opacity-50",
        focusRing,
        className,
      )}
      {...props}
    />
  );
}

export function TabPanel({
  className,
  ...props
}: ComponentProps<typeof BaseTabs.Panel>) {
  return (
    <BaseTabs.Panel
      className={cn("pt-4 outline-none", className)}
      {...props}
    />
  );
}
