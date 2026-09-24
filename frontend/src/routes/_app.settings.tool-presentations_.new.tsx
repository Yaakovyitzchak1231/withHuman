import { createFileRoute } from "@tanstack/react-router";
import { useAccess } from "../app/authorization";
import { ToolPresentationEditorScreen, loadNewToolPresentationPage } from "../app/tool-presentation-editor";
import { validateNewToolPresentationSearch } from "../app/tool-presentation-search";

/* The trailing underscore on tool-presentations_ keeps the editor out
 * of the list route, the way the API key form sits beside its list. */
export const Route = createFileRoute("/_app/settings/tool-presentations_/new")({
  validateSearch: validateNewToolPresentationSearch,
  loaderDeps: ({ search }) => search,
  loader: ({ deps }) => loadNewToolPresentationPage(deps),
  component: NewToolPresentation,
});

function NewToolPresentation() {
  const { holdsAnywhere } = useAccess();
  const data = Route.useLoaderData();
  return <ToolPresentationEditorScreen data={data} canEdit={holdsAnywhere("organization.settings.write")} />;
}
