import { createFileRoute } from "@tanstack/react-router";
import { api } from "../api";
import { useAccess } from "../app/authorization";
import { WebhooksScreen, type WebhooksPage } from "../app/webhooks";
import type { WebhookEndpointSummary } from "../types";

export const Route = createFileRoute("/_app/webhooks")({
  /* A failed listing is reported by the screen rather than thrown, so
   * an expired session reaches the sign-in gate, not the router's
   * error boundary. */
  loader: (): Promise<WebhooksPage> =>
    api.webhookEndpoints().then(
      (response) => ({ endpoints: response.webhook_endpoints, error: "" }),
      (reason: unknown) => ({
        endpoints: [] as WebhookEndpointSummary[],
        error: reason instanceof Error ? reason.message : "Unable to load webhook endpoints",
      }),
    ),
  component: Webhooks,
});

function Webhooks() {
  const { can, holdsAnywhere } = useAccess();
  const page = Route.useLoaderData();
  return (
    <WebhooksScreen
      page={page}
      canManage={can("webhook.write")}
      canRead={holdsAnywhere("webhook.read") || holdsAnywhere("pipeline.read")}
    />
  );
}
