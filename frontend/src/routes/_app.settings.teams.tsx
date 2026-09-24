import { createFileRoute } from "@tanstack/react-router";
import { api } from "../api";
import { useAccess } from "../app/authorization";
import { TeamsScreen, type TeamsPage } from "../app/teams";
import type { Team, TeamListStatus } from "../types";

export const Route = createFileRoute("/_app/settings/teams")({
  /* Teams are a management surface, so a reviewer's request is refused:
   * the screen shows its permission notice and never reads the error. A
   * failure a manager sees is reported by the screen rather than thrown,
   * so an expired session reaches the sign-in gate, not the router's
   * error boundary. */
  validateSearch: (search: Record<string, unknown>): { status?: TeamListStatus } => ({
    status: search.status === "archived" || search.status === "all" ? search.status : "live",
  }),
  loaderDeps: ({ search }) => ({ status: search.status }),
  loader: ({ deps }): Promise<TeamsPage> =>
    api.teams(deps.status).then(
      (response) => ({ teams: response.teams, error: "" }),
      (reason: unknown) => ({
        teams: [] as Team[],
        error: reason instanceof Error ? reason.message : "Unable to load teams",
      }),
    ),
  component: Teams,
});

/* The teams page lists every team, so its checks are the organization
 * wide ones; a team-scoped grant alone does not open it. */
function Teams() {
  const { can, holdsAnywhere } = useAccess();
  const page = Route.useLoaderData();
  const { status } = Route.useSearch();
  const navigate = Route.useNavigate();
  return (
    <TeamsScreen
      page={page}
      status={status ?? "live"}
      onStatusChange={(status) => void navigate({ search: { status } })}
      canRead={holdsAnywhere("team.read")}
      canWrite={can("team.write")}
      canEditMembers={can("team.member.write")}
    />
  );
}
