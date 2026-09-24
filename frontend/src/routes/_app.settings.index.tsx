import { createFileRoute, redirect } from "@tanstack/react-router";

/* /settings has no page of its own: the first entry every member can
 * see is their notifications. */
export const Route = createFileRoute("/_app/settings/")({
  beforeLoad: () => {
    throw redirect({ to: "/settings/notifications", replace: true });
  },
});
