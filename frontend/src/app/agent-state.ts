import type { Tone } from "@/ds/components/badge";
import type {
  AgentCredentialState,
  AgentDetail,
  AgentInstanceDetail,
  AgentSummary,
} from "../types";

/* The connection state of an agent, derived from credentials rather
 * than a heartbeat: agents call in only when they have work, so there
 * is no honest online/offline. Active means at least one active
 * instance holds an unexpired, unrevoked credential. Disabled is the
 * reversible switch; archived is for good. */
export type AgentConnection = "active" | "no_live_credential" | "disabled" | "archived";

export function agentConnection(agent: AgentSummary): AgentConnection {
  if (agent.status === "archived") return "archived";
  if (agent.status === "disabled") return "disabled";
  return agent.live_instance_count > 0 ? "active" : "no_live_credential";
}

export const CONNECTION_LABEL: Record<AgentConnection, string> = {
  active: "Active",
  no_live_credential: "No live credential",
  disabled: "Disabled",
  archived: "Archived",
};

export const CONNECTION_TONE: Record<AgentConnection, Tone> = {
  active: "approve",
  no_live_credential: "warn",
  disabled: "neutral",
  archived: "neutral",
};

/* Liveness covers all instances, independent of the current page or filter. */
export function detailConnection(agent: AgentDetail): AgentConnection {
  if (agent.status === "archived") return "archived";
  if (agent.status === "disabled") return "disabled";
  return agent.live_instance_count > 0 ? "active" : "no_live_credential";
}

export function credentialLive(credential: AgentCredentialState): boolean {
  return !credential.revoked_at && new Date(credential.expires_at).getTime() > Date.now();
}

/* The newest credential tells the instance's story: what it holds now
 * and, if that is nothing usable, why. The label is a word; the dates
 * behind it belong in the cell's hover card. */
export function credentialSummary(instance: AgentInstanceDetail): {
  label: string;
  tone: Tone;
} {
  const credential = instance.credentials[0];
  if (!credential) return { label: "No credential", tone: "neutral" };
  if (credential.revoked_at) return { label: "Revoked", tone: "neutral" };
  if (!credentialLive(credential)) return { label: "Expired", tone: "warn" };
  return { label: "Active", tone: "approve" };
}
