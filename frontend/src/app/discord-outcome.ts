import { useEffect } from "react";
import { useToast } from "@/ds/components/toast";

const ERRORS: Record<string, string> = {
  access_denied: "The request was declined in Discord.",
  install_expired: "The install took too long or was already used. Start it again.",
  guild_taken: "That server is already connected to another organization.",
  link_expired: "The link took too long or was already used. Start it again.",
  not_a_member:
    "That Discord account is not a member of your organization's server. Join the server, then link again.",
  account_taken: "That Discord account is already linked to another member.",
  no_connection: "Your organization's Discord server was disconnected before the link finished.",
  discord_not_configured: "This deployment has no Discord application configured.",
  discord_rejected: "Discord rejected the request. Try again.",
};

// OAuth always reloads the app. Handle its outcome once in the shared shell,
// so the toast survives the redirect back to an unfinished onboarding session.
export function useDiscordOutcome() {
  const toast = useToast();
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const outcome = params.get("discord");
    const error = params.get("discord_error");
    if (!outcome && !error) return;
    if (outcome === "linked" || outcome === "connected") {
      toast.add({
        type: "approve",
        title: outcome === "linked" ? "Discord linked" : "Discord connected",
      });
    } else if (error) {
      toast.add({
        type: "deny",
        title: window.location.pathname === "/settings/integrations"
          ? "Discord was not connected"
          : "Discord was not linked",
        description: ERRORS[error] ?? "Something went wrong during Discord setup. Try again.",
      });
    }
    params.delete("discord");
    params.delete("discord_error");
    const query = params.toString();
    window.history.replaceState(null, "", window.location.pathname + (query ? `?${query}` : "") + window.location.hash);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
}
