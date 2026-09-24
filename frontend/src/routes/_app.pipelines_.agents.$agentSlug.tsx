import { createFileRoute, Navigate } from "@tanstack/react-router";
import { useAccess } from "../app/authorization";
import { PipelineViewScreen, loadPipelineView } from "../app/pipeline-view";
import { pipelineSearch } from "../app/pipeline-link";

export const Route = createFileRoute("/_app/pipelines_/agents/$agentSlug")({
  validateSearch: pipelineSearch,
  loaderDeps: ({ search }) => ({ revision: search.revision }),
  loader: ({ params, deps }) => loadPipelineView(params.agentSlug, deps.revision),
  component: PipelineView,
});

function PipelineView() {
  const { can } = useAccess();
  const { agentSlug } = Route.useParams();
  const page = Route.useLoaderData();
  const { block } = Route.useSearch();
  const resource = { kind: "agent", id: agentSlug } as const;
  if (!can("pipeline.read", resource)) {
    return <Navigate to="/queue" replace />;
  }
  return (
    <PipelineViewScreen
      agentSlug={agentSlug}
      focusedBlock={block}
      page={page}
      canWrite={can("pipeline.write", resource)}
      canActivate={can("pipeline.activate", resource)}
    />
  );
}
