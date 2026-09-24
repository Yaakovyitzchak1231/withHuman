import { createFileRoute } from "@tanstack/react-router";
import { api } from "../api";
import { APIKeysScreen, type APIKeysPage } from "../app/api-keys";
import { holdsAnywhere, useAccess } from "../app/authorization";
import { currentSession, ensureSession } from "../app/session-store";
import type { APIKey, MyAPIKeys } from "../types";

const describe = (reason: unknown, fallback: string) =>
  reason instanceof Error ? reason.message : fallback;

export const Route = createFileRoute("/_app/settings/personal-api-keys")({
  /* The person's own keys always; every member's keys too when they
   * hold api_key.read, which the session settles before this runs.
   * Either fetch failing is reported in the page rather than thrown, so
   * an expired session reaches the sign-in gate, not the router's error
   * boundary. */
  loader: async (): Promise<APIKeysPage> => {
    await ensureSession();
    const principal = currentSession()?.principal;
    const canReadAll = principal ? holdsAnywhere(principal, "api_key.read") : false;
    const [mine, organization] = await Promise.all([
      api.myAPIKeys().then(
        (result): { value: MyAPIKeys | null; error: string } => ({ value: result, error: "" }),
        (reason: unknown) => ({
          value: null,
          error: describe(reason, "Unable to load your API keys"),
        }),
      ),
      canReadAll
        ? api.organizationAPIKeys("personal").then(
            (result): { value: APIKey[] | null; error: string } => ({
              value: result.keys,
              error: "",
            }),
            (reason: unknown) => ({
              value: null,
              error: describe(reason, "Unable to load the organization's API keys"),
            }),
          )
        : Promise.resolve({ value: null, error: "" }),
    ]);
    return {
      mine: mine.value,
      error: mine.error,
      organization: organization.value,
      organizationError: organization.error,
    };
  },
  component: APIKeys,
});

function APIKeys() {
  const { holdsAnywhere } = useAccess();
  const page = Route.useLoaderData();
  return (
    <APIKeysScreen
      page={page}
      kind="personal"
      canIssue={holdsAnywhere("api_key.issue")}
      canReadAll={holdsAnywhere("api_key.read")}
      canRevokeAny={holdsAnywhere("api_key.revoke")}
      links={{ create: "/settings/personal-api-keys/new" }}
    />
  );
}
