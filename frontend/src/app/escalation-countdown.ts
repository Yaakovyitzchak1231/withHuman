import type { ApprovalRouting, RoutingWalkerState } from "../types";

/* What the escalation executor does next for a pending request, read off
 * the routing's progress snapshot: the soonest armed timer across the
 * path walker and every team policy walker running inline, or the fact
 * that a claim holds them all. Null when nothing is armed or the request
 * never entered an escalation path. */
export type EscalationCountdown =
  | {
      /* A level timer with more levels behind it: the next one is paged
       * when it fires. */
      kind: "next_level";
      at: string;
      team?: EscalationCountdownTeam;
    }
  | {
      /* The last level's timer: firing it ends the walk, or starts a
       * repeat when the document allows one. */
      kind: "last_level";
      at: string;
      team?: EscalationCountdownTeam;
    }
  | { kind: "repeat"; at: string; team?: EscalationCountdownTeam }
  | { kind: "deferred"; at: string; team?: EscalationCountdownTeam }
  | { kind: "paused" };

/* The team whose policy walker owns the timer; absent for the path's. */
export interface EscalationCountdownTeam {
  id: string;
  name: string;
}

export function nextEscalation(routing: ApprovalRouting | undefined): EscalationCountdown | null {
  const progress = routing?.escalation;
  if (!progress) return null;
  const walkers: RoutingWalkerState[] = [
    { ...progress, team_id: "" },
    ...progress.teams,
  ];
  let soonest: RoutingWalkerState | null = null;
  for (const walker of walkers) {
    if (!walker.next_escalation_at) continue;
    if (walker.phase !== "level" && walker.phase !== "deferred" && walker.phase !== "repeat_wait") continue;
    if (!soonest || walker.next_escalation_at < soonest.next_escalation_at!) {
      soonest = walker;
    }
  }
  if (!soonest) {
    return walkers.some((walker) => walker.phase === "paused") ? { kind: "paused" } : null;
  }
  const team = soonest.team_id
    ? {
        id: soonest.team_id,
        name:
          routing?.team_policies.find((policy) => policy.team_id === soonest!.team_id)?.team_name ??
          "A team",
      }
    : undefined;
  const at = soonest.next_escalation_at!;
  switch (soonest.phase) {
    case "deferred":
      return { kind: "deferred", at, team };
    case "repeat_wait":
      return { kind: "repeat", at, team };
    default:
      return soonest.level_ordinal < soonest.level_count
        ? { kind: "next_level", at, team }
        : { kind: "last_level", at, team };
  }
}
