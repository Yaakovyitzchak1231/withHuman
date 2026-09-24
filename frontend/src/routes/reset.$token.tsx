import { createFileRoute } from "@tanstack/react-router";
import { ResetCompleteScreen } from "../app/reset";
import { ThemeProvider } from "@/ds/theme/theme-provider";

/* Public: lives outside /_app because that layout is the session gate. */
export const Route = createFileRoute("/reset/$token")({ component: ResetCompletePage });

function ResetCompletePage() {
  const { token } = Route.useParams();
  return (
    <ThemeProvider>
      <ResetCompleteScreen token={token} />
    </ThemeProvider>
  );
}
