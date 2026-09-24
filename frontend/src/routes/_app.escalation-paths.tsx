import { createFileRoute, Navigate } from "@tanstack/react-router";
import { api } from "../api";
import { useAccess } from "../app/authorization";
import { EscalationPathsScreen, type EscalationPathsPage } from "../app/escalation-paths";
import type { EscalationPathSummary } from "../types";

export const Route = createFileRoute("/_app/escalation-paths")({
  /* A failed load is reported by the screen rather than thrown, so an
   * expired session reaches the sign-in gate, not the router's error
   * boundary. */
  loader: (): Promise<EscalationPathsPage> =>
    api.escalationPaths("all").then(
      (response) => ({ paths: response.escalation_paths, error: "" }),
      (reason: unknown) => ({
        paths: [] as EscalationPathSummary[],
        error: reason instanceof Error ? reason.message : "Unable to load escalation paths",
      }),
    ),
  component: EscalationPaths,
});

function EscalationPaths() {
  const { holdsAnywhere } = useAccess();
  const page = Route.useLoaderData();
  if (!holdsAnywhere("escalation_path.read")) {
    return <Navigate to="/queue" replace />;
  }
  return <EscalationPathsScreen page={page} canWrite={holdsAnywhere("escalation_path.write")} />;
}
