import { createFileRoute } from "@tanstack/react-router";
import { api } from "../api";
import { MEMBER_POLICIES, PermissionPoliciesScreen, TEAM_POLICIES } from "../app/permission-policies";

export const Route = createFileRoute("/_app/settings/permissions/$kind/$principalId")({
  loader: async ({ params }) => {
    try {
      if (params.kind !== "members" && params.kind !== "teams") throw new Error("Unknown permission recipient");
      return { page: await api.permissionPolicies(params.kind, params.principalId), error: "" };
    } catch (reason) {
      return { page: null, error: reason instanceof Error ? reason.message : "Unable to load permissions" };
    }
  },
  component: () => {
    const { kind, principalId } = Route.useParams();
    return <PermissionPoliciesScreen {...Route.useLoaderData()} subject={kind === "teams" ? TEAM_POLICIES : MEMBER_POLICIES} principalId={principalId} />;
  },
});
