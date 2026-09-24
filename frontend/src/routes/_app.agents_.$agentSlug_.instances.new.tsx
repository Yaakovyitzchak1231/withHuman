import { createFileRoute } from "@tanstack/react-router";
import {
  loadNewAgentInstancePage,
  NewAgentInstanceScreen,
} from "../app/agent-instance-new";

export const Route = createFileRoute("/_app/agents_/$agentSlug_/instances/new")(
  {
    loader: ({ params }) => loadNewAgentInstancePage(params.agentSlug),
    component: NewAgentInstance,
  },
);

function NewAgentInstance() {
  const { agentSlug } = Route.useParams();
  return (
    <NewAgentInstanceScreen
      key={agentSlug}
      agentSlug={agentSlug}
      page={Route.useLoaderData()}
    />
  );
}
