import { createFileRoute } from "@tanstack/react-router";
import { ensureSession, currentSession } from "../app/session-store";
import { api } from "../api";
import { useAccess } from "../app/authorization";
import { MembersScreen, type MembersPage } from "../app/members";
import { useSession } from "../app/principal";
import type { Invitation, Member, Team } from "../types";

export const Route = createFileRoute("/_app/settings/members")({
  /* The roster, the team names, and the pending invitations arrive
   * together. Every listing is a management surface, so a reviewer's
   * request is refused: the screen shows its permission notice and
   * never reads the error. A failure a manager sees is reported by the
   * screen rather than thrown, so an expired session reaches the sign-in
   * gate, not the router's error boundary. */
  loader: async (): Promise<MembersPage> => {
    await ensureSession();
    const id = currentSession()?.principal.membership_id;
    return Promise.all([
      api.members(),
      api.teams(),
      api.invitations(),
      id
        ? api.permissionPolicies("members", id).catch(() => null)
        : Promise.resolve(null),
    ]).then(
      ([members, teams, invitations, permissionOptions]) => ({
        permissionOptions,
        members: members.members,
        teams: teams.teams,
        invitations: invitations.invitations,
        error: "",
      }),
      (reason: unknown) => ({
        permissionOptions: null,
        members: [] as Member[],
        teams: [] as Team[],
        invitations: [] as Invitation[],
        error:
          reason instanceof Error ? reason.message : "Unable to load members",
      }),
    );
  },
  component: Members,
});

function Members() {
  const session = useSession();
  const { holdsAnywhere, can, isOwner } = useAccess();
  const page = Route.useLoaderData();
  return (
    <MembersScreen
      page={page}
      canRead={can("membership.read")}
      canInvite={can("membership.invite")}
      canWrite={can("membership.write")}
      canAssign={holdsAnywhere("permission_policy.write")}
      isOwner={isOwner}
      selfMembershipId={session.principal.membership_id}
    />
  );
}
