import { createFileRoute } from "@tanstack/react-router";
import { ResetRequestScreen } from "../app/reset";
import { ThemeProvider } from "@/ds/theme/theme-provider";

/* Public: lives outside /_app because that layout is the session gate. */
export const Route = createFileRoute("/reset/")({ component: ResetRequestPage });

function ResetRequestPage() {
  return (
    <ThemeProvider>
      <ResetRequestScreen />
    </ThemeProvider>
  );
}
