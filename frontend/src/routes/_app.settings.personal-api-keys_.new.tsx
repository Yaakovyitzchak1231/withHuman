import { createFileRoute, Navigate } from "@tanstack/react-router";
import { api } from "../api";
import { NewAPIKeyScreen, type NewAPIKeyPage } from "../app/api-key-new";
import { useAccess } from "../app/authorization";

/* The trailing underscore on personal-api-keys_ keeps this page out of the list
 * route, the way an escalation path editor sits beside its list. */
export const Route = createFileRoute("/_app/settings/personal-api-keys_/new")({
  /* The form needs the same answer the list reads: the organization's
   * switch, whether scoping is available, and the bearable vocabulary. A
   * failed fetch is reported in the page rather than thrown, so an
   * expired session reaches the sign-in gate. */
  loader: (): Promise<NewAPIKeyPage> =>
    api.myAPIKeys().then(
      (mine) => ({ mine, error: "" }),
      (reason: unknown) => ({
        mine: null,
        error: reason instanceof Error ? reason.message : "Unable to load your API keys",
      }),
    ),
  component: NewAPIKey,
});

function NewAPIKey() {
  const { holdsAnywhere } = useAccess();
  const page = Route.useLoaderData();
  if (!holdsAnywhere("api_key.issue")) {
    return <Navigate to="/settings/personal-api-keys" replace />;
  }
  return <NewAPIKeyScreen page={page} />;
}
