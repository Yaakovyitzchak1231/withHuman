import { createFileRoute } from "@tanstack/react-router";
import { useAccess } from "../app/authorization";
import { IntegrationsScreen, loadIntegrationsPage } from "../app/integrations";

export const Route = createFileRoute("/_app/settings/integrations")({
  loader: loadIntegrationsPage,
  component: Integrations,
});

function Integrations() {
  const { can } = useAccess();
  const page = Route.useLoaderData();
  return (
    <IntegrationsScreen
      page={page}
      canRead={can("connection.read")}
      canWrite={can("connection.write")}
    />
  );
}
