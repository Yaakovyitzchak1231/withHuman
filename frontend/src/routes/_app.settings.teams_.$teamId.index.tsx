import { createFileRoute, Navigate } from "@tanstack/react-router";
import { useAccess } from "../app/authorization";
import { TeamScreen, loadTeamPage } from "../app/team";

/* The trailing underscore on teams_ keeps this page out of the teams
 * list route; index keeps it beside the escalation policy editor
 * rather than above it. */
export const Route = createFileRoute("/_app/settings/teams_/$teamId/")({
  loader: ({ params }) => loadTeamPage(params.teamId),
  component: TeamRoute,
});

/* A team is a team resource: a team-scoped grant is enough for the
 * team it names. */
function TeamRoute() {
  const { teamId } = Route.useParams();
  const { can } = useAccess();
  const page = Route.useLoaderData();
  const team = { kind: "team" as const, id: teamId };
  if (!can("team.read", team)) {
    return <Navigate to="/settings/teams" replace />;
  }
  return (
    <TeamScreen
      page={page}
      canWrite={can("team.write", team)}
      canEditMembers={can("team.member.write", team)}
    />
  );
}
