import { createFileRoute, Navigate } from "@tanstack/react-router";
import { useAccess } from "../app/authorization";
import {
  TeamEscalationPolicyScreen,
  loadTeamEscalationPolicyPage,
} from "../app/team-escalation-policy";

/* The trailing underscore on teams_ keeps this page out of the teams
 * list route, the way an escalation path editor sits beside its list. */
export const Route = createFileRoute("/_app/settings/teams_/$teamId/escalation-policy")({
  /* The team, its roster, and the active policy arrive together, so the
   * editor opens on the saved document with every member already named. */
  loader: ({ params }) => loadTeamEscalationPolicyPage(params.teamId),
  component: TeamEscalationPolicy,
});

/* Escalation policies are team resources: a team-scoped grant is enough
 * for the team it names. */
function TeamEscalationPolicy() {
  const { teamId } = Route.useParams();
  const { can } = useAccess();
  const page = Route.useLoaderData();
  const team = { kind: "team" as const, id: teamId };
  if (!can("team.read", team)) {
    return <Navigate to="/settings/teams" replace />;
  }
  return (
    <TeamEscalationPolicyScreen
      teamId={teamId}
      page={page}
      canWrite={!page.team?.archived_at && can("team.escalation.write", team)}
      canActivate={!page.team?.archived_at && can("team.escalation.activate", team)}
    />
  );
}
