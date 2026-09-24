import { createFileRoute } from "@tanstack/react-router";
import { ThemeProvider } from "@/ds/theme/theme-provider";
import { OAuthConsentScreen } from "../app/oauth-consent";
import type { OAuthAuthorizationRequest } from "../types";

/* The MCP server's OAuth consent page. Public in the routing sense: it
 * lives outside /_app so the client's query string survives the sign-in
 * it performs in place, and the screen fetches the session itself. */
export const Route = createFileRoute("/oauth/authorize")({
  validateSearch: (search: Record<string, unknown>): OAuthAuthorizationRequest => {
    const text = (key: string) => (typeof search[key] === "string" ? (search[key] as string) : "");
    return {
      client_id: text("client_id"),
      redirect_uri: text("redirect_uri"),
      state: text("state"),
      code_challenge: text("code_challenge"),
      code_challenge_method: text("code_challenge_method"),
      scope: text("scope"),
      resource: text("resource"),
      response_type: text("response_type"),
    };
  },
  component: OAuthAuthorizePage,
});

function OAuthAuthorizePage() {
  const search = Route.useSearch();
  return (
    <ThemeProvider>
      <OAuthConsentScreen search={search} />
    </ThemeProvider>
  );
}
