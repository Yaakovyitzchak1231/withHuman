import { useState, type ReactNode } from "react";
import { cn } from "@/ds/lib/cn";
import { wellTone } from "@/ds/lib/well";
import { Badge } from "./badge";
import { Button } from "./button";
import { IconButton } from "./icon-button";
import { Menu, MenuContent, MenuTrigger, type MenuAction } from "./menu";
import type { BlockOutcome } from "./outcome-select";
import { TargetBadge, TargetSelect, type ReviewerTarget } from "./reviewer-target";
import {
  ArrowUpRightIcon,
  CheckIcon,
  ChevronDownIcon,
  CornerDownRightIcon,
  EllipsisIcon,
  InboxIcon,
  PersonIcon,
  PlusIcon,
  XIcon,
} from "@/ds/icons";
import { focusRing } from "@/ds/lib/focus";

/*
 * The rail an approval pipeline is drawn on, whatever its blocks: where
 * requests enter, the connectors between cards, the exits that fork off
 * a block, a branch's well and end, the add slot, and the terminus that
 * holds the pipeline default. The block cards themselves live with
 * their kinds.
 */

export const OUTCOME_ICONS: Record<BlockOutcome, typeof CheckIcon> = {
  approve: CheckIcon,
  deny: XIcon,
  human: PersonIcon,
};

export const OUTCOME_COPY: Record<BlockOutcome, string> = {
  approve: "Approve automatically",
  deny: "Deny automatically",
  human: "Ask a human",
};

/* One way out of a block, drawn as a horizontal branch off the rail. */
export interface PipelineExit {
  key: string;
  /* Label on the branch: "every request" for an outcome block, the
   * answer for a webhook. */
  label: string;
  outcome: BlockOutcome;
  /* For a human exit: the reviewers it names, or null for the pipeline
   * default. Ignored on automatic exits. */
  target?: ReviewerTarget | null;
}

/*
 * The exit fork under a block: one horizontal branch per terminal exit,
 * each ending in a toned outcome chip, while the rail continues down
 * with whatever does not exit. An outcome block has one exit and no
 * continue row; a webhook has one per answer that decides or asks a
 * human. Conditions live on the cards; consequences live on the exits,
 * mirroring how the pipeline actually runs.
 */
export function PipelineExits({
  exits,
  continueLabel,
  /* The exit a test run took, if any: that branch carries the decision
   * color so the taken path reads at a glance. */
  matchedKey,
  /* What a human exit's reviewers mean: the team's escalation path, or a
   * word on a person. With it the target chip toggles the panel open
   * beneath the branch. */
  renderRoute,
  defaultTarget,
}: {
  exits: PipelineExit[];
  continueLabel?: ReactNode;
  matchedKey?: string;
  renderRoute?: (exit: PipelineExit) => ReactNode;
  defaultTarget?: ReviewerTarget | null;
}) {
  const [openKey, setOpenKey] = useState<string | null>(null);
  return (
    <div className="flex flex-col pl-[15px]">
      {exits.map((exit, index) => {
        const Icon = OUTCOME_ICONS[exit.outcome];
        const matched = matchedKey === exit.key;
        const expandable = exit.outcome === "human" && renderRoute !== undefined && !!exit.target;
        const open = expandable && openKey === exit.key;
        const tone =
          exit.outcome === "approve"
            ? "bg-approve"
            : exit.outcome === "deny"
              ? "bg-deny"
              : "bg-warn";
        return (
          <div key={exit.key} className="flex flex-col">
            <div className="flex items-center">
              <span className="relative w-px shrink-0 self-stretch">
                <span className={cn("absolute top-0 w-px bg-edge-strong", continueLabel == null && index === exits.length - 1 ? "bottom-1/2" : "bottom-0")} />
              </span>
              <span
                className={cn(
                  "h-px w-5 shrink-0 transition-colors duration-200",
                  matched ? tone : "bg-edge-strong",
                )}
              />
              {/* Label and consequence wrap together on a narrow rail,
                * such as a branch's well on a phone. */}
              <span className="flex min-w-0 flex-wrap items-center gap-y-1 py-1">
                <PipelineFlowLabel label={exit.label} matched={matched} className="ml-2" />
                {/* The system badge: toned icon, neutral label, no fill. */}
                <Badge
                  className="ml-1"
                  tone={
                    exit.outcome === "approve" ? "approve" : exit.outcome === "deny" ? "deny" : "warn"
                  }
                  icon={<Icon width={13} height={13} />}
                >
                  {OUTCOME_COPY[exit.outcome]}
                </Badge>
                {/* The consequence of a human exit has two halves: that a
                 * human decides, and which humans. Both live on the exit. */}
                {exit.outcome === "human" ? (
                  <>
                    <span className="mx-1 h-px w-3 shrink-0 bg-edge-strong" />
                    <TargetBadge
                      target={exit.target ?? defaultTarget}
                      onClick={expandable ? () => setOpenKey(open ? null : exit.key) : undefined}
                      expanded={expandable ? open : undefined}
                    />
                  </>
                ) : null}
              </span>
            </div>
            {open ? (
              <div className="flex">
                <span className="w-px self-stretch bg-edge-strong" />
                <div className="mb-2 ml-7 min-w-0 flex-1">{renderRoute?.(exit)}</div>
              </div>
            ) : null}
          </div>
        );
      })}
      {continueLabel != null ? <PipelineFlowRow label={continueLabel} matched={matchedKey === "continue"} /> : null}
    </div>
  );
}

const DELEGATED_COPY: Record<BlockOutcome, string> = {
  approve: "Approved there",
  deny: "Denied there",
  human: "Sent to a human there",
};

/*
 * A delegate block's fork: met conditions leave this pipeline for the
 * named one, whose blocks are its owners' business and are not drawn
 * here; otherwise the rail continues. After a test run that entered the
 * target, the exit carries what the target concluded.
 */
export function PipelineDelegateExits({
  pipeline,
  taken,
  result,
  continueLabel = "Otherwise, check the next block",
}: {
  /* The target pipeline as the exit names it, usually a reference. */
  pipeline: ReactNode;
  /* The exit a test run took: into the target, or past it. */
  taken?: "enter" | "skip";
  /* What the target concluded for a test request that entered it. */
  result?: BlockOutcome;
  continueLabel?: ReactNode;
}) {
  const entered = taken === "enter";
  const ResultIcon = result ? OUTCOME_ICONS[result] : undefined;
  return (
    <div className="flex flex-col pl-[15px]">
      <div className="flex items-center">
        <span className="relative w-px shrink-0 self-stretch">
          <span className="absolute top-0 bottom-0 w-px bg-edge-strong" />
        </span>
        <span
          className={cn(
            "h-px w-5 shrink-0 transition-colors duration-200",
            entered ? "bg-accent" : "bg-edge-strong",
          )}
        />
        <span className="flex min-w-0 flex-wrap items-center gap-y-1 py-1">
          <PipelineFlowLabel label="If conditions are met" matched={entered} className="ml-2" />
          <Badge
            className="ml-1 min-w-0 max-w-full"
            tone="accent"
            icon={<ArrowUpRightIcon width={13} height={13} />}
          >
            <span className="truncate">Continue in {pipeline}</span>
          </Badge>
          {result && ResultIcon ? (
            <>
              <span className="mx-1 h-px w-3 shrink-0 bg-edge-strong" />
              <Badge
                tone={result === "approve" ? "approve" : result === "deny" ? "deny" : "warn"}
                icon={<ResultIcon width={13} height={13} />}
              >
                {DELEGATED_COPY[result]}
              </Badge>
            </>
          ) : null}
        </span>
      </div>
      <PipelineFlowRow label={continueLabel} matched={taken === "skip"} />
    </div>
  );
}

/*
 * A branch's fork on the rail. When the branch's conditions are met the
 * request drops through an elbow into the indented well that holds the
 * branch's own blocks and its end; when they are not, the parent rail
 * runs on past the well, to the left of it, to the next block. Wells
 * alternate surface levels with depth, and each level indents about
 * 48px (less on a phone).
 */
export function PipelineBranchWell({
  name,
  depth,
  taken,
  blocked = false,
  label = "If conditions are met",
  badge = "Check the blocks in this branch",
  continueLabel = "Otherwise, skip this branch and check the next block",
  children,
}: {
  /* The branch's name, for the well's accessible label. */
  name: string;
  /* What the elbow's chip says the request does: check the branch's
   * blocks, or continue in a delegated pipeline. */
  badge?: ReactNode;
  /* 1 for a branch on a pipeline's own rail, 2 for one inside it, and so
   * on; decides the well's surface level. */
  depth: number;
  /* The exit a test run took: into the branch, or past it. That row
   * carries the color so the taken path reads at a glance. */
  taken?: "enter" | "skip";
  /* The branch is unreachable: no request arrives, so none flows past it
   * either and the parent rail ends at the elbow. */
  blocked?: boolean;
  label?: ReactNode;
  /* The parent rail's label after the well: skip to the next block, or
   * the rail's own last words when the branch is the last block. */
  continueLabel?: ReactNode;
  children: ReactNode;
}) {
  const entered = taken === "enter";
  const line = cn("transition-colors duration-200", entered ? "bg-accent" : "bg-edge-strong");
  return (
    <div className="flex flex-col pl-[15px]">
      <div className="flex items-center">
        <span className="relative w-px shrink-0 self-stretch">
          <span className={cn("absolute top-0 w-px bg-edge-strong", blocked ? "bottom-1/2" : "bottom-0")} />
        </span>
        {/* The elbow: right from the rail, then down into the well's
          * square corner. Its width is the well's indent. */}
        <span aria-hidden className="relative w-3 shrink-0 self-stretch sm:w-5">
          <span className={cn("absolute top-1/2 right-0 left-0 h-px", line)} />
          <span className={cn("absolute top-1/2 -right-px bottom-0 w-px", line)} />
        </span>
        {/* On a narrow screen the badge wraps under the label rather
          * than pushing past the well. */}
        <span className="flex min-w-0 flex-wrap items-center gap-x-1 py-1 pl-2">
          <PipelineFlowLabel label={label} matched={entered} />
          <Badge
            tone="accent"
            icon={<CornerDownRightIcon width={13} height={13} />}
            className="min-w-0 max-w-full"
          >
            <span className="truncate">{badge}</span>
          </Badge>
        </span>
      </div>
      <div className="flex">
        <span className={cn("w-px shrink-0 self-stretch", !blocked && "bg-edge-strong")} />
        <div
          role="group"
          aria-label={`Blocks in “${name || "Untitled branch"}”`}
          className={cn(
            "ml-3 min-w-0 flex-1 rounded-lg rounded-tl-none border-hairline border-edge px-2 py-3 sm:ml-5 sm:px-3",
            wellTone(depth),
          )}
        >
          {children}
        </div>
      </div>
      {!blocked ? <PipelineFlowRow label={continueLabel} matched={taken === "skip"} /> : null}
    </div>
  );
}

/* ---------------------------------------------------------------- */
/* Rail framing: entry, sections, connectors, terminus              */
/* ---------------------------------------------------------------- */

/* The rail's head: where requests enter this pipeline. The entry
 * pipeline takes every request an agent submits; any other pipeline
 * takes what delegate blocks hand it, which the description says. */
export function PipelineEntryCard({
  title = "Approval requests start here",
  description = "An agent submits a request to withHuman.",
}: {
  title?: ReactNode;
  description?: ReactNode;
} = {}) {
  return (
    <div className="flex flex-col gap-2">
      <p className="text-micro text-ink-muted">Requests move down this pipeline, one block at a time.</p>
      <div className="flex items-center gap-3 rounded-lg border-hairline border-edge bg-surface px-4 py-2.5 shadow-sm">
        <InboxIcon width={14} height={14} className="shrink-0 text-accent" />
        <div className="min-w-0 flex-1">
          <p className="text-label text-ink">{title}</p>
          <p className="mt-0.5 text-micro text-ink-muted">{description}</p>
        </div>
      </div>
    </div>
  );
}

/*
 * The safety net, and the home of the pipeline default: the reviewers
 * a fall-through request goes to, and what every human exit that names
 * nobody inherits. Without a target the request waits for any
 * authorized reviewer in the queue. With a directory the card edits the
 * default in place, like a block in editing mode: as part of a pipeline
 * draft, or, with onSave, on its own with Save and Cancel beside the
 * picker, the way the overview changes the default without opening the
 * entry pipeline. Actions render a block-style menu at the edge.
 */
export function PipelineTerminusCard({
  target,
  directory,
  onTargetChange,
  onSave,
  onCancel,
  saving = false,
  actions,
  fallbackLabel = "Any authorized reviewer",
  route,
}: {
  target?: ReviewerTarget | null;
  directory?: ReviewerTarget[];
  onTargetChange?: (next: ReviewerTarget | null) => void;
  /* Present when the card saves itself rather than joining a draft. */
  onSave?: () => void;
  onCancel?: () => void;
  saving?: boolean;
  /* The card's own menu, in the block cards' mold. */
  actions?: MenuAction[];
  /* What no target means here: the queue, or the organization
   * pipeline's default when it has one. */
  fallbackLabel?: string;
  /* The chosen target's escalation path, rendered under the sentence. */
  route?: ReactNode;
} = {}) {
  const editing = Boolean(directory && onTargetChange);
  return (
    <PipelineEndFrame
      title="No pipeline conditions matched."
      aside={actions?.length ? (
        <Menu actions={actions}>
          <MenuTrigger
            render={
              <IconButton aria-label="Default reviewer actions" size="sm">
                <EllipsisIcon width={14} height={14} />
              </IconButton>
            }
          />
          <MenuContent align="end" />
        </Menu>
      ) : null}
    >
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <p className="text-body text-ink-secondary">Ask a human</p>
        {editing ? (
          <TargetSelect
            directory={directory ?? []}
            value={target ?? null}
            onChange={(next) => onTargetChange?.(next)}
            fallbackLabel={fallbackLabel}
            aria-label="Default reviewers"
            className="w-56"
          />
        ) : (
          <TargetBadge target={target} fallbackLabel={fallbackLabel} />
        )}
        {editing && onSave ? (
          <span className="flex items-center gap-1.5">
            <Button size="sm" variant="ghost" disabled={saving} onClick={onCancel}>
              Cancel
            </Button>
            <Button size="sm" variant="primary" loading={saving} onClick={onSave}>
              Save
            </Button>
          </span>
        ) : null}
      </div>
      {route ? <div className="mt-1.5">{route}</div> : null}
    </PipelineEndFrame>
  );
}

/* The frame every "a human decides" end shares, the pipeline's terminus
 * and a branch's end alike: the warm person mark, what happened, then
 * who is asked. */
function PipelineEndFrame({
  title,
  aside,
  children,
}: {
  title: ReactNode;
  aside?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="flex items-center gap-3 rounded-lg border-hairline border-edge bg-surface px-4 py-2.5 shadow-sm">
      <span className="flex text-warn">
        <PersonIcon width={14} height={14} />
      </span>
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <p className="text-body text-ink-secondary">{title}</p>
        {children}
      </div>
      {aside}
    </div>
  );
}

/*
 * Where a branch ends when no block inside it decided: the request goes
 * to a human with the branch's reason, and the pipeline stops there.
 * Its reviewers are the branch's own escalation path, else the pipeline
 * default. Only this end uses the branch's path; a human exit inside
 * the branch that names none uses the pipeline default.
 */
export function PipelineBranchEndCard({
  branchName,
  target,
  defaultTarget,
  fallbackLabel,
  reason,
}: {
  branchName: string;
  /* The branch's own escalation path; null or absent uses the default. */
  target?: ReviewerTarget | null;
  /* The pipeline default a branch without a path hands to. */
  defaultTarget?: ReviewerTarget | null;
  /* What the chip says when neither names anyone. */
  fallbackLabel?: string;
  reason?: string;
}) {
  return (
    <PipelineEndFrame title={<>No block in “{branchName || "Untitled branch"}” decided.</>}>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <p className="text-body text-ink-secondary">Ask a human</p>
        <TargetBadge target={target ?? defaultTarget} fallbackLabel={fallbackLabel} />
      </div>
      {reason ? <p className="text-micro text-ink-muted">{reason}</p> : null}
    </PipelineEndFrame>
  );
}

/* One kind of block the add slot can insert. info is one more sentence
 * about the kind, behind an info mark at the end of the row. */
export interface PipelineAddChoice {
  id: string;
  label: string;
  icon?: ReactNode;
  info?: ReactNode;
  disabled?: boolean;
}

/* The insertion point at the end of an editable section: a dashed
 * slot the width of the rail, so a new block visibly lands in the
 * execution order rather than appearing from a floating button. With
 * choices, clicking opens a menu of block kinds instead. */
export function PipelineAddSlot({
  label = "Add block",
  onClick,
  choices,
  onChoose,
}: {
  label?: string;
  onClick?: () => void;
  choices?: PipelineAddChoice[];
  onChoose?: (id: string) => void;
}) {
  const slotClassName = cn(
    "flex h-10 w-full cursor-pointer items-center justify-center gap-1.5",
    "rounded-lg border-hairline border-dashed border-edge-strong",
    "text-label text-ink-muted select-none",
    "transition-colors duration-100",
    "hover:border-edge-strong hover:bg-fill-hover hover:text-ink",
    focusRing,
  );
  if (choices?.length) {
    return (
      <Menu
        actions={choices.map((choice) => ({
          id: choice.id,
          label: choice.label,
          icon: choice.icon,
          info: choice.info,
          disabled: choice.disabled,
          action: () => onChoose?.(choice.id),
        }))}
      >
        <MenuTrigger
          render={
            /* The trigger is a combobox, whose name never comes from its
             * content, so it carries the label as its name too. */
            <button type="button" aria-label={label} className={slotClassName}>
              <PlusIcon width={13} height={13} />
              {label}
            </button>
          }
        />
        <MenuContent />
      </Menu>
    );
  }
  return (
    <button type="button" onClick={onClick} className={slotClassName}>
      <PlusIcon width={13} height={13} />
      {label}
    </button>
  );
}

/* Every rail and branch label shares sentence case and the same typography. */
function PipelineFlowLabel({
  label,
  matched = false,
  className,
}: {
  label: ReactNode;
  matched?: boolean;
  className?: string;
}) {
  return (
    <span className={cn(
      "text-micro font-normal transition-colors duration-200",
      matched ? "text-ink" : "text-ink-muted",
      className,
    )}>
      {typeof label === "string" ? label.charAt(0).toUpperCase() + label.slice(1) : label}
    </span>
  );
}

/* A small arrow on the rail makes the direction visible even without a label. */
function PipelineFlowLine() {
  return (
    <span aria-hidden="true" className="relative w-px shrink-0 self-stretch bg-edge-strong">
      <ChevronDownIcon
        width={9}
        height={9}
        className="absolute -bottom-0.5 left-1/2 -translate-x-1/2 text-ink-muted"
      />
    </span>
  );
}

/* Shared geometry keeps every downward connector flush with the cards. */
function PipelineFlowRow({
  label,
  matched = false,
  children,
}: {
  label?: ReactNode;
  matched?: boolean;
  children?: ReactNode;
}) {
  return (
    <div className="flex min-h-7 items-center gap-2.5">
      <PipelineFlowLine />
      {label ? <PipelineFlowLabel label={label} matched={matched} /> : null}
      {children}
    </div>
  );
}

/* The vertical joint between rail cards, labeled with why execution
 * moves on ("no match") or how it starts ("in order"). */
export function PipelineConnector({
  label,
  children,
}: {
  label?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="pl-[15px]">
      <PipelineFlowRow label={label}>{children}</PipelineFlowRow>
    </div>
  );
}
