import { createFileRoute, Navigate } from "@tanstack/react-router";
import { WebhookEndpointScreen, loadWebhookEndpointPage } from "../app/webhook-endpoint";
import { useAccess } from "../app/authorization";

export const Route = createFileRoute("/_app/webhooks_/$endpointKey")({
  /* The active revision, the history, and the uses arrive together, so
   * the editor opens on the saved destination with the rollback and
   * archive affordances already informed. */
  loader: ({ params }) => loadWebhookEndpointPage(params.endpointKey),
  component: WebhookEndpoint,
});

function WebhookEndpoint() {
  const { endpointKey } = Route.useParams();
  const { can } = useAccess();
  const page = Route.useLoaderData();
  if (!can("webhook.write")) {
    return <Navigate to="/webhooks" replace />;
  }
  return <WebhookEndpointScreen endpointKey={endpointKey} page={page} />;
}
