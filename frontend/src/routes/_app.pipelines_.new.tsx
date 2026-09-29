import { createFileRoute, Navigate } from "@tanstack/react-router";
import { useAccess } from "../app/authorization";
import { NewPipelineScreen } from "../app/pipeline-new";

/* A form for a pipeline that does not exist yet: the first save creates
 * it. "new" is therefore never a pipeline key. */
export const Route = createFileRoute("/_app/pipelines_/new")({
  component: NewPipeline,
});

function NewPipeline() {
  const { can } = useAccess();
  if (!can("pipeline.write")) {
    return <Navigate to="/pipelines" replace />;
  }
  return <NewPipelineScreen />;
}
