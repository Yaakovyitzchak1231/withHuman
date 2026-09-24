import { createFileRoute, Navigate } from "@tanstack/react-router";
import { api } from "../api";
import { AgentsScreen, type AgentsPage } from "../app/agents";
import { useAccess } from "../app/authorization";
import type { AgentSummary } from "../types";

export const Route = createFileRoute("/_app/agents")({
  /* A failed roster load is reported by the screen rather than thrown,
   * so an expired session reaches the sign-in gate, not the router's
   * error boundary. */
  loader: async (): Promise<AgentsPage> =>
    api.agents("all").then(
      (response) => ({ agents: response.agents, error: "" }),
      (reason: unknown) => ({
        agents: [] as AgentSummary[],
        error: reason instanceof Error ? reason.message : "Unable to load agents",
      }),
    ),
  component: Agents,
});

function Agents() {
  const { holdsAnywhere } = useAccess();
  const page = Route.useLoaderData();
  if (!holdsAnywhere("agent.read")) {
    return <Navigate to="/queue" replace />;
  }
  return <AgentsScreen page={page} canCreate={holdsAnywhere("agent.write")} />;
}
