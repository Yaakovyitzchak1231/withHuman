import { createFileRoute } from "@tanstack/react-router";
import { VersionScreen, loadVersionPage } from "../app/installation";

export const Route = createFileRoute("/_app/settings/version")({
  /* A failed load is reported by the screen rather than thrown, so an
   * expired session reaches the sign-in gate, not the router's error
   * boundary. */
  loader: loadVersionPage,
  component: Version,
});

function Version() {
  return <VersionScreen page={Route.useLoaderData()} />;
}
