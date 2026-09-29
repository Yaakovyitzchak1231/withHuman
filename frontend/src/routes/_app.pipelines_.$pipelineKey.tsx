import { createFileRoute, Navigate } from "@tanstack/react-router";
import { useAccess } from "../app/authorization";
import { PipelineViewScreen, loadPipelineView } from "../app/pipeline-view";
import { pipelineSearch } from "../app/pipeline-link";

export const Route = createFileRoute("/_app/pipelines_/$pipelineKey")({
  validateSearch: pipelineSearch,
  loaderDeps: ({ search }) => ({ revision: search.revision }),
  loader: ({ params, deps }) => loadPipelineView(params.pipelineKey, deps.revision),
  component: PipelineView,
});

function PipelineView() {
  const { can } = useAccess();
  const { pipelineKey } = Route.useParams();
  const page = Route.useLoaderData();
  const { edit, block } = Route.useSearch();
  const resource = { kind: "pipeline", id: pipelineKey } as const;
  if (!can("pipeline.read", resource)) {
    return <Navigate to="/queue" replace />;
  }
  return (
    <PipelineViewScreen
      pipelineKey={pipelineKey}
      initiallyEditing={edit}
      focusedBlock={block}
      page={page}
      canWrite={can("pipeline.write", resource)}
      canActivate={can("pipeline.activate", resource)}
    />
  );
}
