import { createFileRoute } from "@tanstack/react-router";
import { loadChannelsPage } from "../app/contact-channels";
import { NotificationsScreen } from "../app/notifications";

export const Route = createFileRoute("/_app/settings/notifications")({
  loader: loadChannelsPage,
  component: Notifications,
});

function Notifications() {
  const page = Route.useLoaderData();
  return <NotificationsScreen page={page} />;
}
