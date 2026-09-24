import type { ComponentProps, ReactNode } from "react";
import { cn } from "@/ds/lib/cn";
import { Badge } from "./badge";
import { IconButton } from "./icon-button";
import { Menu, MenuContent, MenuTrigger, type MenuAction } from "./menu";
import {
  ArrowLeftIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  CopyIcon,
  CornerDownRightIcon,
  EllipsisIcon,
  GripIcon,
  TrashIcon,
} from "@/ds/icons";

/*
 * The anatomy every editable pipeline block shares, whatever its kind:
 * a header with the grip that drags it, its position on the rail, its
 * name and a one-line summary in machine mono, and the block menu; and
 * a body, the kind's own fields, shown while the card is expanded. One
 * header keeps every kind on one rail looking like one thing.
 */

/* A block's place on the rail: 3 at the top level, 3.2.1 for the first
 * block of the second block of branch 3. */
export type BlockPosition = number | string;

/* The round position pill. It grows sideways for a nested position, so
 * 3.2.1 fits without shrinking the type. */
export function BlockPositionPill({
  position,
  className,
}: {
  position: BlockPosition;
  className?: string;
}) {
  return (
    <span
      className={cn(
        "flex h-6 min-w-6 shrink-0 items-center justify-center rounded-full border-hairline border-edge px-1.5",
        "font-mono text-[11px] text-ink-muted",
        className,
      )}
    >
      {position}
    </span>
  );
}

/* What the block menu can do. Move up and Move down are always listed
 * (disabled at the ends of the rail, as the keyboard path for the grip);
 * the moves across a branch's edge appear only where they apply. */
export interface BlockMenuHandlers {
  onMoveUp?: () => void;
  onMoveDown?: () => void;
  /* The block sits right after a branch: it joins that branch as its
   * last block. */
  onMoveIntoBranch?: () => void;
  /* The block sits inside a branch: it leaves it and follows it. */
  onMoveOutOfBranch?: () => void;
  onDuplicate: () => void;
  onRemove: () => void;
}

/* The block menu as data, so a kind can extend it and tests can read it
 * without opening a popup. noun names what Duplicate and Remove act on;
 * extra actions sit between them. */
export function blockMenuActions({
  onMoveUp,
  onMoveDown,
  onMoveIntoBranch,
  onMoveOutOfBranch,
  onDuplicate,
  onRemove,
  noun = "block",
  extra = [],
}: BlockMenuHandlers & { noun?: "block" | "branch"; extra?: MenuAction[] }): MenuAction[] {
  return [
    {
      id: "move-up",
      label: "Move up",
      group: "order",
      disabled: !onMoveUp,
      icon: <ChevronUpIcon width={14} height={14} />,
      action: () => onMoveUp?.(),
    },
    {
      id: "move-down",
      label: "Move down",
      group: "order",
      disabled: !onMoveDown,
      icon: <ChevronDownIcon width={14} height={14} />,
      action: () => onMoveDown?.(),
    },
    ...(onMoveIntoBranch
      ? [{
          id: "move-into-branch",
          label: "Move into the branch above",
          group: "order",
          icon: <CornerDownRightIcon width={14} height={14} />,
          action: onMoveIntoBranch,
        }]
      : []),
    ...(onMoveOutOfBranch
      ? [{
          id: "move-out-of-branch",
          label: "Move out of this branch",
          group: "order",
          icon: <ArrowLeftIcon width={14} height={14} />,
          action: onMoveOutOfBranch,
        }]
      : []),
    {
      id: "duplicate",
      label: `Duplicate ${noun}`,
      icon: <CopyIcon width={14} height={14} />,
      action: onDuplicate,
    },
    ...extra,
    {
      id: "remove",
      label: `Remove ${noun}`,
      group: "danger",
      danger: true,
      icon: <TrashIcon width={14} height={14} />,
      action: onRemove,
    },
  ];
}

export interface BlockCardHeaderProps {
  position: BlockPosition;
  title: ReactNode;
  /* The one-line summary under the name, in mono. Present in both
   * states, so expanding never reflows the header. */
  subtitle: ReactNode;
  enabled?: boolean;
  expanded: boolean;
  onExpandedChange: (expanded: boolean) => void;
  /* Spread onto the grip handle; the parent wires its drag behavior.
   * Without it the handle is not rendered. */
  handleProps?: ComponentProps<"div">;
  actions: MenuAction[];
}

export function BlockCardHeader({
  position,
  title,
  subtitle,
  enabled = true,
  expanded,
  onExpandedChange,
  handleProps,
  actions,
}: BlockCardHeaderProps) {
  return (
    <div
      role="button"
      tabIndex={0}
      aria-expanded={expanded}
      onClick={() => onExpandedChange(!expanded)}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          onExpandedChange(!expanded);
        }
      }}
      className={cn(
        "flex w-full cursor-pointer items-center gap-3 rounded-lg px-4 py-3 text-left",
        "transition-colors duration-100 hover:bg-fill-hover/50",
        expanded && "rounded-b-none",
      )}
    >
      {handleProps ? (
        <div
          aria-hidden
          {...handleProps}
          onClick={(event) => event.stopPropagation()}
          className={cn(
            "-ml-1.5 flex h-6 w-4 shrink-0 cursor-grab touch-none items-center justify-center",
            "text-ink-faint transition-colors duration-100 hover:text-ink-muted",
            "active:cursor-grabbing",
            handleProps.className,
          )}
        >
          <GripIcon width={14} height={14} />
        </div>
      ) : null}
      <BlockPositionPill position={position} />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
          <span className="font-medium text-ink">{title}</span>
          {!enabled ? <Badge>Off</Badge> : null}
        </div>
        <p className="mt-0.5 truncate font-mono text-[11px] text-ink-muted">{subtitle}</p>
      </div>
      <div
        className="flex shrink-0 items-center gap-0.5"
        onClick={(event) => event.stopPropagation()}
        onKeyDown={(event) => event.stopPropagation()}
      >
        <Menu actions={actions}>
          <MenuTrigger
            render={
              <IconButton aria-label="Block actions" size="sm">
                <EllipsisIcon width={14} height={14} />
              </IconButton>
            }
          />
          <MenuContent />
        </Menu>
      </div>
    </div>
  );
}

export interface BlockCardProps
  extends Omit<BlockCardHeaderProps, "actions">,
    BlockMenuHandlers {
  /* True while this card is the one being dragged. */
  dragging?: boolean;
  /* Replaces the standard block menu, for a kind with actions of its own
   * (build it with blockMenuActions). */
  actions?: MenuAction[];
  /* Extra classes on the expanded body, for a kind that recesses it. */
  bodyClassName?: string;
  /* The kind's own fields, shown while expanded. */
  children?: ReactNode;
}

/* The whole editable card: the shared header over the kind's body. */
export function BlockCard({
  dragging = false,
  actions,
  bodyClassName,
  children,
  onMoveUp,
  onMoveDown,
  onMoveIntoBranch,
  onMoveOutOfBranch,
  onDuplicate,
  onRemove,
  ...header
}: BlockCardProps) {
  const enabled = header.enabled ?? true;
  return (
    <div
      className={cn(
        "rounded-lg border-hairline border-edge bg-surface shadow-sm",
        !enabled && "opacity-60",
        dragging && "opacity-40",
      )}
    >
      <BlockCardHeader
        {...header}
        actions={
          actions ??
          blockMenuActions({ onMoveUp, onMoveDown, onMoveIntoBranch, onMoveOutOfBranch, onDuplicate, onRemove })
        }
      />
      {header.expanded ? (
        <div className={cn("flex flex-col gap-4 border-t-hairline border-edge px-4 py-4", bodyClassName)}>
          {children}
        </div>
      ) : null}
    </div>
  );
}
