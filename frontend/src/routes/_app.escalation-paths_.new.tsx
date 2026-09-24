import { createFileRoute, Navigate } from "@tanstack/react-router";
import { useAccess } from "../app/authorization";
import { EscalationPathScreen, loadNewEscalationPathPage } from "../app/escalation-path";

/* The editor opened on an empty draft: the first save creates the path.
 * "new" is therefore never a path key. */
export const Route = createFileRoute("/_app/escalation-paths_/new")({
  loader: () => loadNewEscalationPathPage(),
  component: NewEscalationPath,
});

function NewEscalationPath() {
  const { can } = useAccess();
  const page = Route.useLoaderData();
  if (!can("escalation_path.write")) {
    return <Navigate to="/escalation-paths" replace />;
  }
  return (
    <EscalationPathScreen
      pathKey=""
      page={page}
      canWrite
      canActivate={can("escalation_path.activate")}
    />
  );
}
