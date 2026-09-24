import { createFileRoute } from "@tanstack/react-router";
import { ThemeProvider } from "@/ds/theme/theme-provider";
import { InviteScreen } from "../app/invite";

/* Public route outside /_app: it must render logged out, so it mounts
 * its own theme provider instead of the authenticated provider stack. */
export const Route = createFileRoute("/invite/$token")({
  component: InvitePage,
});

function InvitePage() {
  const { token } = Route.useParams();
  return (
    <ThemeProvider>
      <InviteScreen token={token} />
    </ThemeProvider>
  );
}
