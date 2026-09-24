import { loadAgentCatalog } from "../app/tool-catalog";
import { createFileRoute, Navigate } from "@tanstack/react-router";
import { api, RequestError } from "../api";
import { AgentScreen, type AgentPage } from "../app/agent";
import {
  AGENT_PAGE_SIZE,
  validateAgentSearch,
  type AgentSearch,
} from "../app/agent-search";
import { useAccess } from "../app/authorization";
import { agentTab } from "../app/extensions";
import type { AgentDetail, ApprovalRequest } from "../types";

export const Route = createFileRoute("/_app/agents_/$agentSlug")({
  validateSearch: validateAgentSearch,
  /* An edition tab filters client side, so its search is not a loader
   * dependency: only the tab itself is. */
  loaderDeps: ({ search }): AgentSearch =>
    agentTab(search.tab) ? { tab: search.tab } : search,
  loader: async ({ params, deps }): Promise<AgentPage> => {
    const instancesTab = deps.tab === "instances";
    const requestTab = deps.tab === "requests";
    const editionTab = agentTab(deps.tab);
    const [tabData, detail, history, catalogue] = await Promise.all([
      editionTab ? editionTab.load(params.agentSlug) : Promise.resolve(undefined),
      api
        .agent(
          params.agentSlug,
          instancesTab
            ? {
                limit: AGENT_PAGE_SIZE,
                offset: (deps.page ?? 0) * AGENT_PAGE_SIZE,
                q: deps.q,
                status: deps.status,
              }
            : deps.tab === "provisioners"
              ? {
                  provisionerLimit: AGENT_PAGE_SIZE,
                  provisionerOffset: (deps.page ?? 0) * AGENT_PAGE_SIZE,
                  provisionerQuery: deps.q,
                  provisionerStatus: deps.status,
                }
              : {},
        )
        .then(
          (agent) => ({ agent, error: "" }),
          (reason: unknown) => ({
            agent: null as AgentDetail | null,
            error:
              reason instanceof RequestError && reason.status === 404
                ? ""
                : reason instanceof Error
                  ? reason.message
                  : "Unable to load agent",
          }),
        ),
      requestTab
        ? api
            .agentApprovals(params.agentSlug, {
              limit: AGENT_PAGE_SIZE + 1,
              offset: (deps.page ?? 0) * AGENT_PAGE_SIZE,
              q: deps.q,
              status: deps.status,
            })
            .then(
              (response) => ({
                requests: response.requests.slice(0, AGENT_PAGE_SIZE),
                hasMoreRequests: response.requests.length > AGENT_PAGE_SIZE,
                requestsError: "",
              }),
              (reason: unknown) => ({
                requests: [] as ApprovalRequest[],
                hasMoreRequests: false,
                requestsError:
                  reason instanceof Error
                    ? reason.message
                    : "Unable to load requests",
              }),
            )
        : Promise.resolve({
            requests: [] as ApprovalRequest[],
            hasMoreRequests: false,
            requestsError: "",
          }),
      deps.tab === "tools" ? loadAgentCatalog(params.agentSlug, deps) : Promise.resolve(undefined),
    ]);
    return { ...detail, ...history, tabData, catalogue, search: deps };
  },
  component: Agent,
});

/* The write, issue, and revoke checks are scoped to the agent itself,
 * so the screen evaluates them once the agent is loaded. */
function Agent() {
  const { holdsAnywhere } = useAccess();
  const page = Route.useLoaderData();
  const search = Route.useSearch();
  if (!holdsAnywhere("agent.read")) {
    return <Navigate to="/queue" replace />;
  }
  return (
    <AgentScreen
      key={page.agent?.slug ?? "missing"}
      page={agentTab(search.tab) ? { ...page, search } : page}
    />
  );
}
