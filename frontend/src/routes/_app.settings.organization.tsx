import { createFileRoute } from "@tanstack/react-router";
import { useAccess } from "../app/authorization";
import { OrganizationScreen, loadOrganizationPage } from "../app/organization";

export const Route = createFileRoute("/_app/settings/organization")({
  /* The policy arrives with the route, so the page paints filled in. A
   * failed load is reported by the screen rather than thrown, so an
   * expired session reaches the sign-in gate, not the router's error
   * boundary. */
  loader: loadOrganizationPage,
  component: Organization,
});

/* The policy is organization-wide, so both checks are the coarse
 * "holds anywhere" ones; there is no narrower scope to hold them at. */
function Organization() {
  const { holdsAnywhere } = useAccess();
  const page = Route.useLoaderData();
  return (
    <OrganizationScreen
      key={page.policy ? JSON.stringify(page.policy) : page.error}
      page={page}
      canRead={holdsAnywhere("organization.read")}
      canEdit={holdsAnywhere("organization.settings.write")}
    />
  );
}
