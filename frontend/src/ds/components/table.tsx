import type { ReactNode, SyntheticEvent } from "react";
import { cn } from "@/ds/lib/cn";
import { focusRing } from "@/ds/lib/focus";
import {
  ChevronDownIcon,
  ChevronUpDownIcon,
  ChevronUpIcon,
  EllipsisIcon,
} from "@/ds/icons";
import { IconButton } from "./icon-button";
import { LIST_PADDING, List, type ListProps } from "./list";
import { Menu, MenuContent, MenuTrigger, type MenuAction } from "./menu";
import { Spinner } from "./spinner";
import { InfoCard, type InfoCardProps } from "./info-card";

/*
 * The columned surface for members, agents, and credentials, built on
 * the List engine: same highlight, selection, and keyboard contract,
 * with a header row and column definitions so headers and cells
 * cannot drift. Sorting is a header affordance owned by the screen
 * (sort/onSort are controlled), so it can live in the URL. A column
 * whose meaning is not obvious from its label carries an info line:
 * the header grows an info card trigger, separate from its sort button.
 *
 * Every row ends in the same dot-dot-dot menu: View first, whenever the
 * row can be opened, then the screen's own actions. An entry the reader
 * cannot take right now (a missing permission, a row in the wrong
 * state) stays in place faded rather than vanishing, so every row reads
 * the same and the reader learns what a row can do.
 */
export interface TableColumn<Item> {
  id: string;
  label?: ReactNode;
  /* Contextual help opened from the info icon beside the label. */
  info?: ReactNode;
  infoTitle?: string;
  docsLinks?: InfoCardProps["docsLinks"];
  /* Fixed pixel width; omit with grow for the elastic column. */
  width?: number;
  grow?: boolean;
  align?: "left" | "right";
  sortable?: boolean;
  /* The column that says the row's status. It keeps full contrast in a
   * muted row, so the badge still explains why the row is faded. */
  status?: boolean;
  cell: (item: Item) => ReactNode;
}

export interface TableSort {
  column: string;
  direction: "asc" | "desc";
}

export interface TableProps<Item>
  extends Omit<ListProps<Item>, "children" | "openButton"> {
  columns: TableColumn<Item>[];
  sort?: TableSort | null;
  onSort?: (next: TableSort) => void;
  /* The row menu's entries after View. Disable an entry the reader
   * cannot take rather than leaving it out. */
  rowActions?: (item: Item) => MenuAction[];
  /* Names the row in the menu trigger's accessible label. */
  rowLabel?: (item: Item) => string;
  /* What the View entry says; a table whose open expands a panel in
   * place says so. */
  openLabel?: string;
  /* A row with work in flight shows a spinner in place of the dots. */
  rowBusy?: (item: Item) => boolean;
  /* A row that is switched off (a disabled instance) fades every cell
   * except the status column and the row menu. */
  rowMuted?: (item: Item) => boolean;
}

/* One icon button; the menu itself opens in a portal. */
const ACTIONS_WIDTH = 44;
const ACTIONS_COLUMN = "row-actions";

export function Table<Item>({
  columns: ownColumns,
  sort = null,
  onSort,
  rowActions,
  rowLabel,
  openLabel = "View",
  rowBusy,
  rowMuted,
  className,
  ...listProps
}: TableProps<Item>) {
  const selectable = listProps.selectable ?? true;
  const inset = LIST_PADDING[listProps.padding ?? "compact"];
  const { onOpen } = listProps;
  const columns: TableColumn<Item>[] =
    onOpen || rowActions
      ? [
          ...ownColumns,
          {
            id: ACTIONS_COLUMN,
            width: ACTIONS_WIDTH,
            align: "right",
            cell: (item) => (
              <RowActions
                label={rowLabel?.(item)}
                busy={rowBusy?.(item) ?? false}
                actions={[
                  ...(onOpen
                    ? [{ id: "view", group: "open", label: openLabel, action: () => onOpen(item) }]
                    : []),
                  ...(rowActions?.(item) ?? []),
                ]}
              />
            ),
          },
        ]
      : ownColumns;
  return (
    <div className={cn("flex flex-col", className)}>
      <div className={cn("flex items-center gap-1 border-b-hairline border-edge", inset)}>
        {selectable ? <span className="w-[22px] shrink-0" /> : null}
        <div className="flex min-w-0 flex-1 items-center gap-3">
          {columns.map((column) => (
            <HeaderCell
              key={column.id}
              column={column}
              sort={sort}
              onSort={onSort}
            />
          ))}
        </div>
      </div>
      <List {...listProps} openButton={false}>
        {(item) => {
          const muted = rowMuted?.(item) ?? false;
          return (
            <div className="flex min-w-0 flex-1 items-center gap-3 py-1.5">
              {columns.map((column) => (
                <div
                  key={column.id}
                  style={column.width ? { width: column.width } : undefined}
                  className={cn(
                    column.grow ? "min-w-0 flex-1" : "shrink-0",
                    column.align === "right" && "text-right",
                    muted &&
                      !column.status &&
                      column.id !== ACTIONS_COLUMN &&
                      "opacity-60",
                  )}
                >
                  {column.cell(item)}
                </div>
              ))}
            </div>
          );
        }}
      </List>
    </div>
  );
}

/* The menu lives inside a row that opens on click and Enter, and the
 * popup's events bubble through the React tree even from its portal:
 * nothing from the menu may reach the row. */
function RowActions({
  label,
  busy,
  actions,
}: {
  label: string | undefined;
  busy: boolean;
  actions: MenuAction[];
}) {
  const stop = (event: SyntheticEvent) => event.stopPropagation();
  return (
    <span
      className="flex items-center justify-end"
      onClick={stop}
      onDoubleClick={stop}
      onKeyDown={stop}
    >
      <Menu actions={actions}>
        <MenuTrigger
          render={
            <IconButton aria-label={label ? `Actions for ${label}` : "Row actions"} size="sm">
              {busy ? <Spinner size={14} /> : <EllipsisIcon width={15} height={15} />}
            </IconButton>
          }
        />
        <MenuContent align="end" />
      </Menu>
    </span>
  );
}

function HeaderCell<Item>({
  column,
  sort,
  onSort,
}: {
  column: TableColumn<Item>;
  sort: TableSort | null;
  onSort?: (next: TableSort) => void;
}) {
  const active = sort?.column === column.id;
  const label = (
    <span className="text-micro tracking-[0.06em] uppercase">{column.label}</span>
  );
  const trigger =
    column.sortable && onSort ? (
      <button
        type="button"
        onClick={() =>
          onSort({
            column: column.id,
            direction: active && sort?.direction === "asc" ? "desc" : "asc",
          })
        }
        className={cn(
          "group/sort inline-flex cursor-pointer items-center gap-1 rounded-xs",
          "transition-colors duration-100",
          active ? "text-ink-secondary" : "text-ink-faint hover:text-ink-secondary",
          column.align === "right" && "flex-row-reverse",
          focusRing,
        )}
      >
        {label}
        {active ? (
          sort?.direction === "asc" ? (
            <ChevronUpIcon width={11} height={11} />
          ) : (
            <ChevronDownIcon width={11} height={11} />
          )
        ) : (
          <ChevronUpDownIcon
            width={11}
            height={11}
            className="opacity-0 transition-opacity duration-100 group-hover/sort:opacity-100"
          />
        )}
      </button>
    ) : (
      <span className="text-ink-faint">{label}</span>
    );
  return (
    <div
      style={column.width ? { width: column.width } : undefined}
      className={cn(
        "py-2",
        column.grow ? "min-w-0 flex-1" : "shrink-0",
        column.align === "right" && "text-right",
      )}
    >
      <span className="inline-flex items-center gap-1">
        {trigger}
        {column.info ? (
          <InfoCard
            title={column.infoTitle ?? (typeof column.label === "string" ? column.label : column.id)}
            docsLinks={column.docsLinks}
          >
            {column.info}
          </InfoCard>
        ) : null}
      </span>
    </div>
  );
}
