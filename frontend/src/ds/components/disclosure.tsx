import { useState, type ReactNode } from "react";
import { Collapsible } from "@base-ui/react/collapsible";
import { cn } from "@/ds/lib/cn";
import { focusRing } from "@/ds/lib/focus";
import { ChevronRightIcon } from "@/ds/icons";

/*
 * An expandable separator: a labeled hairline rule that opens into its
 * content. Closed by default, for secondary detail that should not
 * compete with the main column.
 */
export interface DisclosureProps {
  label: string;
  defaultOpen?: boolean;
  children: ReactNode;
  /* Trailing control on the separator row (e.g. a CopyButton). Lives
   * outside the trigger; only present while the panel is open, so the
   * closed separator runs full width. */
  action?: ReactNode;
  className?: string;
}

export function Disclosure({
  label,
  defaultOpen = false,
  children,
  action,
  className,
}: DisclosureProps) {
  const [open, setOpen] = useState(defaultOpen);
  /* The panel clips only while its height animates. At rest it must not,
   * or a card inside loses its hairline and shadow at the right and
   * bottom edges. */
  const [settling, setSettling] = useState(false);
  return (
    <Collapsible.Root
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        setSettling(true);
      }}
      className={cn("flex flex-col", className)}
    >
      {/* Fixed row height: the action button (26px) must not change
       * the row's geometry when it shows. */}
      <div className="flex h-[26px] items-center">
        <Collapsible.Trigger
          className={cn(
            "group flex h-full flex-1 cursor-pointer items-center gap-2 rounded-sm",
            "text-label text-ink-muted select-none",
            "transition-colors duration-100 hover:text-ink",
            focusRing,
          )}
        >
          <ChevronRightIcon
            width={13}
            height={13}
            className={cn(
              "shrink-0 text-ink-faint",
              "transition-transform duration-160 ease-standard",
              "group-data-panel-open:rotate-90",
            )}
          />
          {label}
          <span className="flex-1 border-t-hairline border-edge" />
        </Collapsible.Trigger>
        {action ? (
          /* Stays mounted so the width can tween: the 0fr to 1fr grid
           * track animates the line handing space to the action. */
          <span
            inert={!open}
            className={cn(
              "grid shrink-0",
              "transition-[grid-template-columns,opacity,margin-right] duration-160 ease-standard",
              /* -mr-2 cancels the inner pl-2 the 0fr track cannot
               * compress, so the closed rule truly reaches the edge. */
              open
                ? "mr-0 grid-cols-[1fr] opacity-100"
                : "-mr-2 grid-cols-[0fr] opacity-0",
            )}
          >
            <span className="flex min-w-0 items-center overflow-hidden pl-2">
              {action}
            </span>
          </span>
        ) : null}
      </div>
      <Collapsible.Panel
        onTransitionEnd={(event) => {
          if (event.target === event.currentTarget && event.propertyName === "height") {
            setSettling(false);
          }
        }}
        className={cn(
          "h-[var(--collapsible-panel-height)]",
          settling ? "overflow-hidden" : "overflow-visible",
          "transition-[height] duration-160 ease-standard",
          "data-starting-style:h-0 data-ending-style:h-0",
        )}
      >
        {/* Content aligns with the label, past the chevron and its gap. */}
        <div className="pt-2 pl-[21px]">{children}</div>
      </Collapsible.Panel>
    </Collapsible.Root>
  );
}
