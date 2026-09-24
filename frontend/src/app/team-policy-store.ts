import type { TeamPath } from "@/ds/components/escalation-tree";
import type { ReviewerTarget } from "@/ds/components/reviewer-target";
import { api } from "../api";
import type { Team } from "../types";
import { teamPolicyTreeFromWire } from "./reviewer-route";

/*
 * The active escalation policies of the organization's teams, as the
 * design system's trees, so a path level that names a team can open
 * what the team does with the request. Loaded by route loaders beside
 * the path vocabulary and kept at module scope: a policy revision is
 * immutable, so a tree fetched once for a revision is good until the
 * team's active revision moves.
 */

/* Trees by team id: a tree, or null for a team with no active policy
 * (everyone on it is notified at once). */
export type TeamPaths = Record<string, TeamPath | null>;

const cache = new Map<string, { revision: number; path: TeamPath }>();

export async function loadTeamPaths(teams: Team[], names: ReviewerTarget[]): Promise<TeamPaths> {
  const paths: TeamPaths = {};
  await Promise.all(
    teams.map(async (team) => {
      const revision = team.escalation_policy?.active_revision ?? null;
      if (revision === null) {
        paths[team.id] = null;
        return;
      }
      const cached = cache.get(team.id);
      if (cached && cached.revision === revision) {
        paths[team.id] = cached.path;
        return;
      }
      try {
        const wire = await api.teamEscalationPolicy(team.id);
        const path = teamPolicyTreeFromWire(team, wire, names);
        cache.set(team.id, { revision: wire.revision, path });
        paths[team.id] = path;
      } catch {
        /* A policy that cannot be read renders as none; the tree is
         * explanation, not authority. */
        paths[team.id] = null;
      }
    }),
  );
  return paths;
}
