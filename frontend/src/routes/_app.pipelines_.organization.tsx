import { createFileRoute, Navigate } from "@tanstack/react-router";
import { useAccess } from "../app/authorization";
import { PipelineViewScreen, loadPipelineView } from "../app/pipeline-view";
import { pipelineSearch } from "../app/pipeline-link";

export const Route = createFileRoute("/_app/pipelines_/organization")({
  validateSearch: pipelineSearch,
  loaderDeps: ({ search }) => ({ revision: search.revision }),
  loader: ({ deps }) => loadPipelineView("", deps.revision),
  component: OrganizationPipelineView,
});

function OrganizationPipelineView() {
  const { can } = useAccess();
  const page = Route.useLoaderData();
  const { edit, block } = Route.useSearch();
  const resource = { kind: "organization" } as const;
  if (!can("pipeline.read", resource)) {
    return <Navigate to="/queue" replace />;
  }
  return (
    <PipelineViewScreen
      agentSlug=""
      initiallyEditing={edit}
      focusedBlock={block}
      page={page}
      canWrite={can("pipeline.write", resource)}
      canActivate={can("pipeline.activate", resource)}
    />
  );
}
