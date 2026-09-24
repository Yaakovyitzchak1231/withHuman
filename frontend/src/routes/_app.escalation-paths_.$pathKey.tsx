import { createFileRoute, Navigate } from "@tanstack/react-router";
import { useAccess } from "../app/authorization";
import { EscalationPathScreen, loadEscalationPathPage } from "../app/escalation-path";

export const Route = createFileRoute("/_app/escalation-paths_/$pathKey")({
  /* The directory and the active revision arrive together, so the editor
   * opens on the saved document with every target already named. */
  loader: ({ params }) => loadEscalationPathPage(params.pathKey),
  component: EscalationPath,
});

/* Escalation paths are organization resources: the checks are flat. */
function EscalationPath() {
  const { pathKey } = Route.useParams();
  const { can, holdsAnywhere } = useAccess();
  const page = Route.useLoaderData();
  if (!holdsAnywhere("escalation_path.read")) {
    return <Navigate to="/queue" replace />;
  }
  return (
    <EscalationPathScreen
      pathKey={pathKey}
      page={page}
      canWrite={can("escalation_path.write")}
      canActivate={can("escalation_path.activate")}
    />
  );
}
