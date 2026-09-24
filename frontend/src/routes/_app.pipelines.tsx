import { createFileRoute, Navigate } from "@tanstack/react-router";
import { useAccess } from "../app/authorization";
import { PipelinesScreen, loadPipelinesPage } from "../app/pipelines";

export const Route = createFileRoute("/_app/pipelines")({
  loader: () => loadPipelinesPage(),
  component: Pipelines,
});

function Pipelines() {
  const { holdsAnywhere } = useAccess();
  const page = Route.useLoaderData();
  if (!holdsAnywhere("pipeline.read")) {
    return <Navigate to="/queue" replace />;
  }
  return <PipelinesScreen page={page} />;
}
