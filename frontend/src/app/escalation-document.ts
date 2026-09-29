import { newPathCondition, type PathCondition, type WorkingHoursOption } from "@/ds/components/escalation-tree";
import type { ReviewerTarget } from "@/ds/components/reviewer-target";
import type { Weekday } from "@/ds/components/weekday-picker";
import type {
  EscalationDocumentBodyWire,
  EscalationPathConditionWire,
  EscalationPathNodeWire,
  EscalationTargetWire,
} from "../types";
import { formatGoDuration, parseGoDuration } from "./duration";

/*
 * The escalation document as the editor works on it, shared by the
 * organization escalation path and the team escalation policy: seconds
 * instead of Go durations, single-interval working hours, named
 * targets, and the design system's branch conditions. toWire and
 * fromWire translate at the boundary; nothing here touches the network
 * or the DOM.
 */

export interface WorkingHoursSet {
  id: string;
  name: string;
  timezone: string;
  weekdays: Weekday[];
  start: string;
  end: string;
}

export type PathNode =
  | {
      id: string;
      type: "level";
      targets: ReviewerTarget[];
      escalateAfterSeconds: number;
      raiseUrgency: boolean;
    }
  | {
      id: string;
      type: "if_else";
      conditions: PathCondition[];
      thenNodes: PathNode[];
      elseNodes: PathNode[];
    }
  | {
      id: string;
      type: "defer";
      workingHoursId: string;
      standardOnly: boolean;
    };

export interface EscalationDocument {
  workingHours: WorkingHoursSet[];
  nodes: PathNode[];
  repeat: { enabled: boolean; times: number; afterSeconds: number };
}

export const NODE_META: Record<PathNode["type"], { label: string; tone: "neutral" | "accent" | "warn" }> = {
  defer: { label: "Defer", tone: "neutral" },
  level: { label: "Notify", tone: "accent" },
  if_else: { label: "If / else", tone: "neutral" },
};

const DEFAULT_ESCALATE_AFTER_SECONDS = 900;
const DEFAULT_REPEAT = { enabled: false, times: 2, afterSeconds: 3600 } as const;
const DEFAULT_WEEKDAYS: Weekday[] = ["mon", "tue", "wed", "thu", "fri"];

const uid = () => crypto.randomUUID();

/* A duration the wire sent, or the level default when it is missing or
 * unreadable: the editor always has a number to show. */
function seconds(value: string | undefined, fallback: number): number {
  const parsed = value === undefined ? null : parseGoDuration(value);
  return parsed === null ? fallback : Math.round(parsed);
}

function targetName(type: ReviewerTarget["type"], id: string, directory: ReviewerTarget[]): string {
  return (
    directory.find((entry) => entry.type === type && entry.id === id)?.name ?? `${id.slice(0, 8)}…`
  );
}

/* A level target on the wire. A broadcast carries no id; an escalation
 * path or an agent-owner route is never a level target and is dropped. */
export function targetToWire(target: ReviewerTarget): EscalationTargetWire | null {
  switch (target.type) {
    case "broadcast":
      return { type: "broadcast" };
    case "user":
    case "team":
      return { type: target.type, id: target.id };
    case "escalation_path":
    case "agent_owner":
      return null;
  }
}

/* A wire target named from the directory. The one broadcast entry of a
 * team vocabulary names the whole team; without it the chip still says
 * what it means. */
export function targetFromWire(target: EscalationTargetWire, directory: ReviewerTarget[]): ReviewerTarget {
  if (target.type === "broadcast") {
    return (
      directory.find((entry) => entry.type === "broadcast") ?? {
        type: "broadcast",
        id: "",
        name: "Everyone on the team",
      }
    );
  }
  return { type: target.type, id: target.id, name: targetName(target.type, target.id, directory) };
}

function conditionToWire(condition: PathCondition): EscalationPathConditionWire {
  switch (condition.kind) {
    case "working_hours":
      return { type: "working_hours", working_hours_id: condition.set, active: condition.active };
    case "urgency":
      return { type: "urgency", urgency: condition.urgency };
    case "request":
      return {
        type: "request",
        when: condition.when as unknown as EscalationPathConditionWire["when"],
      };
  }
}

function conditionFromWire(condition: EscalationPathConditionWire): PathCondition {
  switch (condition.type) {
    case "working_hours":
      return { kind: "working_hours", set: condition.working_hours_id ?? "", active: condition.active === true };
    case "urgency":
      return { kind: "urgency", urgency: condition.urgency ?? "interrupt" };
    case "request": {
      const when = (condition.when ?? { id: uid(), all: [] }) as unknown as Extract<
        PathCondition,
        { kind: "request" }
      >["when"];
      /* The builder edits groups; wrap a bare leaf in an implicit all so
       * it renders as one row, as the pipeline editor does. */
      return {
        kind: "request",
        when: when.all || when.any ? when : { id: uid(), all: [when] },
      };
    }
  }
}

function nodeToWire(node: PathNode): EscalationPathNodeWire {
  switch (node.type) {
    case "level":
      return {
        id: node.id,
        type: "level",
        targets: node.targets.flatMap((target) => {
          const wire = targetToWire(target);
          return wire ? [wire] : [];
        }),
        escalate_after: formatGoDuration(node.escalateAfterSeconds),
        ...(node.raiseUrgency ? { raise_urgency: true } : {}),
      };
    case "if_else":
      return {
        id: node.id,
        type: "if_else",
        conditions: node.conditions.map(conditionToWire),
        then: node.thenNodes.map(nodeToWire),
        else: node.elseNodes.map(nodeToWire),
      };
    case "defer":
      return {
        id: node.id,
        type: "defer",
        working_hours_id: node.workingHoursId,
        ...(node.standardOnly ? { standard_only: true } : {}),
      };
  }
}

function nodeFromWire(node: EscalationPathNodeWire, directory: ReviewerTarget[]): PathNode {
  switch (node.type) {
    case "level":
      return {
        id: node.id,
        type: "level",
        targets: (node.targets ?? []).map((target) => targetFromWire(target, directory)),
        escalateAfterSeconds: seconds(node.escalate_after, DEFAULT_ESCALATE_AFTER_SECONDS),
        raiseUrgency: node.raise_urgency === true,
      };
    case "if_else":
      return {
        id: node.id,
        type: "if_else",
        conditions: (node.conditions ?? []).map(conditionFromWire),
        thenNodes: (node.then ?? []).map((child) => nodeFromWire(child, directory)),
        elseNodes: (node.else ?? []).map((child) => nodeFromWire(child, directory)),
      };
    case "defer":
      return {
        id: node.id,
        type: "defer",
        workingHoursId: node.working_hours_id ?? "",
        standardOnly: node.standard_only === true,
      };
  }
}

export function toWire(document: EscalationDocument): EscalationDocumentBodyWire {
  return {
    working_hours: document.workingHours.map((set) => ({
      id: set.id,
      name: set.name,
      timezone: set.timezone,
      intervals: [{ weekdays: set.weekdays, start: set.start, end: set.end }],
    })),
    nodes: document.nodes.map(nodeToWire),
    ...(document.repeat.enabled
      ? { repeat: { times: document.repeat.times, after: formatGoDuration(document.repeat.afterSeconds) } }
      : {}),
  };
}

/* The wire body as the editor holds it, targets named from the
 * directory (teams and people for a path, the broadcast entry and the
 * team's members for a policy). */
export function fromWire(body: EscalationDocumentBodyWire, directory: ReviewerTarget[]): EscalationDocument {
  return {
    workingHours: body.working_hours.map((set) => {
      const interval = set.intervals[0];
      return {
        id: set.id,
        name: set.name,
        timezone: set.timezone,
        weekdays: (interval?.weekdays ?? DEFAULT_WEEKDAYS) as Weekday[],
        start: interval?.start ?? "09:00",
        end: interval?.end ?? "17:00",
      };
    }),
    nodes: body.nodes.map((node) => nodeFromWire(node, directory)),
    repeat: body.repeat
      ? {
          enabled: true,
          times: body.repeat.times,
          afterSeconds: seconds(body.repeat.after, DEFAULT_REPEAT.afterSeconds),
        }
      : { ...DEFAULT_REPEAT },
  };
}

/* A new document starts with one working-hours set and no steps. */
export function newDraft(): EscalationDocument {
  return {
    workingHours: [newWorkingHoursSet("Business hours")],
    nodes: [],
    repeat: { ...DEFAULT_REPEAT },
  };
}

export function newWorkingHoursSet(name: string): WorkingHoursSet {
  return {
    id: uid(),
    name,
    timezone: "UTC",
    weekdays: [...DEFAULT_WEEKDAYS],
    start: "09:00",
    end: "17:00",
  };
}

export function newNode(type: PathNode["type"], sets: WorkingHoursOption[]): PathNode {
  const firstSet = sets[0]?.id ?? "";
  switch (type) {
    case "level":
      return {
        id: uid(),
        type,
        targets: [],
        escalateAfterSeconds: DEFAULT_ESCALATE_AFTER_SECONDS,
        raiseUrgency: false,
      };
    case "if_else":
      return {
        id: uid(),
        type,
        conditions: [newPathCondition("request", sets)],
        thenNodes: [],
        elseNodes: [],
      };
    case "defer":
      return { id: uid(), type, workingHoursId: firstSet, standardOnly: true };
  }
}

/* How many of the document's three parts differ from the saved one, for
 * the save bar. A document with no active revision yet has nothing to
 * keep, so steps alone make it worth saving. */
export function countChanges(
  draft: EscalationDocument,
  saved: EscalationDocument,
  hasActiveRevision: boolean,
): number {
  let count = 0;
  if (JSON.stringify(draft.workingHours) !== JSON.stringify(saved.workingHours)) count += 1;
  if (JSON.stringify(draft.nodes) !== JSON.stringify(saved.nodes)) count += 1;
  if (JSON.stringify(draft.repeat) !== JSON.stringify(saved.repeat)) count += 1;
  if (count === 0 && !hasActiveRevision && draft.nodes.length > 0) count = 1;
  return count;
}
