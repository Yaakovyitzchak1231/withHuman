import { createFileRoute } from "@tanstack/react-router";
import { useAccess } from "../app/authorization";
import { ToolPresentationEditorScreen, loadToolPresentationEditPage } from "../app/tool-presentation-editor";
import { validateNewToolPresentationSearch } from "../app/tool-presentation-search";

export const Route = createFileRoute("/_app/settings/tool-presentations_/$presentationId")({
  validateSearch: validateNewToolPresentationSearch,
  loaderDeps: ({ search }) => ({ request: search.request }),
  loader: ({ params, deps }) => loadToolPresentationEditPage(params.presentationId, deps.request),
  component: EditToolPresentation,
});

function EditToolPresentation() {
  const { holdsAnywhere } = useAccess();
  const data = Route.useLoaderData();
  return <ToolPresentationEditorScreen data={data} canEdit={holdsAnywhere("organization.settings.write")} />;
}
