import { createFileRoute } from "@tanstack/react-router";
import { useAccess } from "../app/authorization";
import { ToolPresentationsScreen, loadToolPresentationsPage } from "../app/tool-presentations";

export const Route = createFileRoute("/_app/settings/tool-presentations")({
  loader: loadToolPresentationsPage,
  component: ToolPresentations,
});

/* Presentations are organization-wide settings, so both checks are the
 * coarse ones, like the organization policy page. */
function ToolPresentations() {
  const { holdsAnywhere } = useAccess();
  const data = Route.useLoaderData();
  return (
    <ToolPresentationsScreen
      data={data}
      canRead={holdsAnywhere("organization.read")}
      canEdit={holdsAnywhere("organization.settings.write")}
    />
  );
}
