import type { ComponentProps, ReactNode } from "react";
import { Combobox } from "@base-ui/react/combobox";
import { cn } from "@/ds/lib/cn";
import {
  ClosableComboboxRoot,
  useEscapeClose,
  useRegisterTrigger,
} from "@/ds/lib/closable-combobox";
import { focusRing } from "@/ds/lib/focus";
import { CheckIcon, ChevronUpDownIcon, SearchIcon } from "@/ds/icons";
import { Tooltip } from "./tooltip";
import { Badge } from "./badge";

/*
 * A select that filters as you type, in the Linear mold: the popup opens
 * as a plain list, and the filter row reveals itself on the first
 * keystroke. Built on Combobox (a filterable Select); pass
 * itemToStringLabel so filtering matches labels.
 */
export const Select = ClosableComboboxRoot;

const TRIGGER_CLASSES = cn(
  "flex h-[30px] min-w-40 cursor-pointer items-center justify-between gap-2",
  "rounded-sm border-hairline border-edge bg-control pr-1.5 pl-2.5",
  "text-body text-ink select-none shadow-sm",
  "transition-[border-color,box-shadow] duration-100 ease-standard",
  "hover:border-edge-strong",
  "data-disabled:pointer-events-none data-disabled:opacity-50",
  focusRing,
);

export function SelectTrigger({
  className,
  placeholder,
  icon,
  ...props
}: ComponentProps<typeof Combobox.Trigger> & {
  placeholder?: string;
  icon?: ReactNode;
}) {
  const registerTrigger = useRegisterTrigger();
  return (
    <Combobox.Trigger
      ref={registerTrigger}
      className={cn(TRIGGER_CLASSES, className)}
      {...props}
    >
      <span className="flex min-w-0 items-center gap-1.5">
        {icon ? <span className="flex shrink-0">{icon}</span> : null}
        <span className="truncate in-data-placeholder:text-ink-faint">
          <Combobox.Value placeholder={placeholder} />
        </span>
      </span>
      <Combobox.Icon className="flex text-ink-muted">
        <ChevronUpDownIcon width={14} height={14} />
      </Combobox.Icon>
    </Combobox.Trigger>
  );
}

/* Trigger for `<Select multiple>`: shows the first labels and folds the
 * rest into a count, e.g. "Slack, Email +2". */
export function MultiSelectTrigger<Item>({
  className,
  placeholder,
  emptyLabel,
  itemLabel,
  maxLabels = 2,
  display = "summary",
  ...props
}: ComponentProps<typeof Combobox.Trigger> & {
  /* Shown faint while nothing is selected, inviting a choice. */
  placeholder?: string;
  /* For a filter, where nothing selected means everything: shown in
   * ink like a chosen value, since it is one. Wins over placeholder. */
  emptyLabel?: string;
  itemLabel: (item: Item) => string;
  maxLabels?: number;
  /* Pills show every selected value and wrap inside the trigger. */
  display?: "summary" | "pills";
}) {
  const registerTrigger = useRegisterTrigger();
  return (
    <Combobox.Trigger
      ref={registerTrigger}
      className={cn(
        TRIGGER_CLASSES,
        display === "pills" && "h-auto min-h-[30px] py-1 pl-1",
        className,
      )}
      {...props}
    >
      <span
        className={cn(
          "flex min-w-0 gap-1.5",
          display === "pills"
            ? "flex-wrap items-center text-left"
            : "items-baseline truncate",
        )}
      >
        <Combobox.Value>
          {(value: Item[]) => {
            if (!value || value.length === 0) {
              return emptyLabel ? (
                <span className="truncate">{emptyLabel}</span>
              ) : (
                <span
                  className={cn(
                    "text-ink-faint",
                    display === "pills" && "pl-1.5",
                  )}
                >
                  {placeholder}
                </span>
              );
            }
            const labels = value.map(itemLabel);
            if (display === "pills") {
              return labels.map((label, index) => (
                <Badge
                  key={index}
                  icon={false}
                  fill
                  className="h-auto min-h-6 max-w-full whitespace-normal py-0.5 [overflow-wrap:anywhere]"
                >
                  {label}
                </Badge>
              ));
            }
            const shown = labels.slice(0, maxLabels).join(", ");
            const extra = labels.length - maxLabels;
            return (
              <>
                <span className="truncate">{shown}</span>
                {extra > 0 ? (
                  <Tooltip content={labels.slice(maxLabels).join(", ")}>
                    <span className="shrink-0 font-mono text-micro font-normal text-ink-muted">
                      +{extra}
                    </span>
                  </Tooltip>
                ) : null}
              </>
            );
          }}
        </Combobox.Value>
      </span>
      <Combobox.Icon className="flex shrink-0 text-ink-muted">
        <ChevronUpDownIcon width={14} height={14} />
      </Combobox.Icon>
    </Combobox.Trigger>
  );
}

export function SelectContent<Item>({
  className,
  children,
  sideOffset = 4,
  filterPlaceholder,
  ...props
}: Omit<ComponentProps<typeof Combobox.Positioner>, "children"> & {
  className?: string;
  /* Keeps the filter row visible from the moment the popup opens,
   * showing this placeholder. Without it the row stays collapsed until
   * the first keystroke. Use for long vocabularies where filtering is
   * the expected way in. */
  filterPlaceholder?: string;
  children: (item: Item) => ReactNode;
}) {
  const onEscape = useEscapeClose();
  return (
    <Combobox.Portal>
      <Combobox.Positioner align="start" sideOffset={sideOffset} {...props}>
        <Combobox.Popup
          onKeyDownCapture={onEscape}
          className={cn(
            /* ds-root re-establishes the baseline inside the portal. The
             * height cap sits here, not on the list, so the filter row
             * counts against it and only the list ever scrolls. */
            "ds-root flex max-h-(--available-height) min-w-(--anchor-width) flex-col",
            "rounded-md bg-surface-raised",
            "shadow-overlay ring-hairline",
            "[transform-origin:var(--transform-origin)]",
            "transition-[opacity,scale] duration-120 ease-out",
            "data-starting-style:scale-[0.98] data-starting-style:opacity-0",
            "data-ending-style:scale-[0.98] data-ending-style:opacity-0",
            className,
          )}
        >
          {/* Collapsed while the query is empty (placeholder shown); the
           * input keeps focus either way, so typing reveals the row. A
           * filterPlaceholder keeps the row open from the start instead. */}
          <div
            className={cn(
              "flex items-center gap-2 border-b-hairline border-edge px-2.5",
              !filterPlaceholder && [
                "has-[input:placeholder-shown]:h-0 has-[input:placeholder-shown]:overflow-hidden",
                "has-[input:placeholder-shown]:border-b-0",
              ],
            )}
          >
            <SearchIcon
              width={14}
              height={14}
              className="shrink-0 text-ink-faint"
            />
            <Combobox.Input
              aria-label="Filter options"
              placeholder={filterPlaceholder ?? " "}
              size={1}
              /* Password managers attach to bare inputs and their overlay
               * eats the first Escape; opt out. */
              data-1p-ignore=""
              data-lpignore="true"
              data-bwignore=""
              className="h-9 w-full min-w-0 bg-transparent text-body text-ink outline-none placeholder:text-ink-faint"
            />
          </div>
          <Combobox.Empty>
            <p className="px-2.5 py-3 text-body text-ink-muted">
              No matching options.
            </p>
          </Combobox.Empty>
          <Combobox.List className="min-h-0 flex-1 overflow-y-auto p-1 empty:p-0">
            {children}
          </Combobox.List>
        </Combobox.Popup>
      </Combobox.Positioner>
    </Combobox.Portal>
  );
}

/* Grouped options: pass `items` as `{ value, items }` groups to the
 * Select and render each group through these inside SelectContent. */
export function SelectGroup<Item>({
  className,
  label,
  children,
  ...props
}: Omit<ComponentProps<typeof Combobox.Group>, "children"> & {
  label?: ReactNode;
  children: (item: Item) => ReactNode;
}) {
  return (
    <Combobox.Group
      className={cn(
        "not-last:mb-1 not-last:border-b-hairline not-last:border-edge not-last:pb-1",
        className,
      )}
      {...props}
    >
      {label ? (
        <Combobox.GroupLabel className="px-2 pt-1.5 pb-1 text-micro text-ink-faint">
          {label}
        </Combobox.GroupLabel>
      ) : null}
      <Combobox.Collection>{children}</Combobox.Collection>
    </Combobox.Group>
  );
}

export function SelectItem({
  className,
  children,
  ...props
}: ComponentProps<typeof Combobox.Item> & { children: ReactNode }) {
  return (
    <Combobox.Item
      className={cn(
        "grid h-7 cursor-pointer grid-cols-[14px_1fr] items-center gap-2 rounded-sm px-2",
        "text-body text-ink outline-none select-none",
        "transition-colors duration-100",
        "data-highlighted:bg-fill-hover",
        "data-disabled:pointer-events-none data-disabled:opacity-50",
        className,
      )}
      {...props}
    >
      <Combobox.ItemIndicator className="col-start-1 flex text-ink">
        <CheckIcon width={13} height={13} />
      </Combobox.ItemIndicator>
      <span className="col-start-2 truncate">{children}</span>
    </Combobox.Item>
  );
}
