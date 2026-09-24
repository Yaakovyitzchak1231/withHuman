import {
  createContext,
  useContext,
  useMemo,
  useRef,
  type ComponentProps,
  type ReactNode,
  type RefObject,
} from "react";
import { Combobox } from "@base-ui/react/combobox";
import { cn } from "@/ds/lib/cn";
import {
  ClosableComboboxRoot,
  useEscapeClose,
  useRegisterTrigger,
} from "@/ds/lib/closable-combobox";
import { SearchIcon } from "@/ds/icons";
import { Kbd } from "./kbd";
import { InfoCard, type InfoCardProps } from "./info-card";

/*
 * Action menu that filters as you type, in the Linear mold: opens as a
 * plain list, and the filter row reveals itself on the first keystroke.
 * Built on Combobox rather than Menu because filtering needs an
 * items-driven list; actions are data, not JSX.
 */
export interface MenuAction {
  id: string;
  label: string;
  /* Actions in different groups are separated by a hairline. */
  group?: string;
  /* Shortcut hint, e.g. "mod+C". Display only. */
  keys?: string;
  /* One more sentence about the action, behind an info mark at the end
   * of the row so the label itself stays short. */
  info?: ReactNode;
  docsLinks?: InfoCardProps["docsLinks"];
  icon?: ReactNode;
  danger?: boolean;
  disabled?: boolean;
  action: () => void;
}

interface ActionGroup {
  value: string;
  items: MenuAction[];
}

export interface MenuProps
  extends Omit<
    ComponentProps<typeof Combobox.Root>,
    "items" | "value" | "onValueChange" | "defaultValue"
  > {
  actions: MenuAction[];
}

const MenuHelpContext = createContext<{
  hasHelp: boolean;
  popupRef: RefObject<HTMLDivElement | null>;
} | null>(null);

export function Menu({ actions, children, ...props }: MenuProps) {
  const groups = useMemo<ActionGroup[]>(() => {
    const byGroup = new Map<string, MenuAction[]>();
    for (const action of actions) {
      const key = action.group ?? "";
      const items = byGroup.get(key) ?? [];
      items.push(action);
      byGroup.set(key, items);
    }
    return [...byGroup.entries()].map(([value, items]) => ({ value, items }));
  }, [actions]);

  const popupRef = useRef<HTMLDivElement>(null);
  const hasHelp = actions.some((action) => Boolean(action.info));
  return (
    <MenuHelpContext value={{ hasHelp, popupRef }}>
      <ClosableComboboxRoot items={groups} grid={hasHelp} {...props}>
        {children}
      </ClosableComboboxRoot>
    </MenuHelpContext>
  );
}

export function MenuTrigger(props: ComponentProps<typeof Combobox.Trigger>) {
  const registerTrigger = useRegisterTrigger();
  return <Combobox.Trigger ref={registerTrigger} {...props} />;
}

export function MenuContent({
  className,
  sideOffset = 4,
  align = "start",
  filterLabel = "Filter actions",
  emptyLabel = "No matching actions.",
  ...props
}: ComponentProps<typeof Combobox.Positioner> & {
  className?: string;
  /* What the filter row and the empty state call the items, when they
   * are not actions (people, say). */
  filterLabel?: string;
  emptyLabel?: string;
}) {
  const onEscape = useEscapeClose();
  const help = useContext(MenuHelpContext);
  const hasHelp = help?.hasHelp;
  return (
    <Combobox.Portal>
      <Combobox.Positioner align={align} sideOffset={sideOffset} {...props}>
        <Combobox.Popup
          ref={help?.popupRef}
          onKeyDownCapture={onEscape}
          onFocus={(event) => {
            // The combobox normally sends list focus back to its filter input.
            // Help buttons and their links need to keep their own focus.
            if (hasHelp && event.target !== event.currentTarget) {
              event.preventBaseUIHandler();
            }
          }}
          className={cn(
            /* ds-root re-establishes the baseline inside the portal. The
             * height cap sits here, not on the list, so the filter row
             * counts against it and only the list ever scrolls. */
            "ds-root flex max-h-(--available-height) min-w-[190px] flex-col",
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
           * input keeps focus either way, so typing reveals the row. */}
          <div
            className={cn(
              "flex items-center gap-2 border-b-hairline border-edge px-2.5",
              "has-[input:placeholder-shown]:h-0 has-[input:placeholder-shown]:overflow-hidden",
              "has-[input:placeholder-shown]:border-b-0",
            )}
          >
            <SearchIcon width={14} height={14} className="shrink-0 text-ink-faint" />
            <Combobox.Input
              aria-label={filterLabel}
              placeholder=" "
              size={1}
              /* Password managers attach to bare inputs and their overlay
               * eats the first Escape; opt out. */
              data-1p-ignore=""
              data-lpignore="true"
              data-bwignore=""
              className="h-9 w-full min-w-0 bg-transparent text-body text-ink outline-none"
            />
          </div>
          <Combobox.Empty>
            <p className="px-2.5 py-3 text-body text-ink-muted">{emptyLabel}</p>
          </Combobox.Empty>
          <Combobox.List className="min-h-0 flex-1 overflow-y-auto p-1 empty:p-0">
            {(group: ActionGroup) => (
              <Combobox.Group
                key={group.value}
                items={group.items}
                role={hasHelp ? "rowgroup" : "group"}
                className="not-last:mb-1 not-last:border-b-hairline not-last:border-edge not-last:pb-1"
              >
                <Combobox.Collection>
                  {(item: MenuAction) => (
                    <MenuActionItem key={item.id} item={item} />
                  )}
                </Combobox.Collection>
              </Combobox.Group>
            )}
          </Combobox.List>
        </Combobox.Popup>
      </Combobox.Positioner>
    </Combobox.Portal>
  );
}

function MenuActionItem({ item }: { item: MenuAction }) {
  const help = useContext(MenuHelpContext);
  const hasHelp = help?.hasHelp;
  const action = (
    <Combobox.Item
      value={item}
      disabled={item.disabled}
      onClick={() => item.action()}
      className={cn(
        "flex h-7 min-w-0 flex-1 cursor-pointer items-center gap-2 rounded-sm px-2",
        "text-body outline-none select-none",
        "transition-colors duration-100",
        item.danger
          ? "text-deny data-highlighted:bg-deny-subtle"
          : "text-ink data-highlighted:bg-fill-hover",
        "data-disabled:pointer-events-none data-disabled:opacity-50",
      )}
    >
      {item.icon ? (
        <span className={item.danger ? "flex text-deny" : "flex text-ink-muted"}>
          {item.icon}
        </span>
      ) : null}
      <span className="min-w-0 flex-1 truncate">{item.label}</span>
      {item.keys ? <Kbd keys={item.keys} /> : null}
    </Combobox.Item>
  );
  if (!hasHelp) return action;

  // Keep help in its own grid cell so reading it never selects the action.
  return (
    <Combobox.Row className="flex items-center gap-1">
      {action}
      <div role="gridcell" className="flex w-6 shrink-0 items-center">
        {item.info ? (
          <InfoCard title={item.label} docsLinks={item.docsLinks} portalContainer={help?.popupRef}>
            {item.info}
          </InfoCard>
        ) : null}
      </div>
    </Combobox.Row>
  );
}
