import { extensions } from "./extensions";

/** Readable names shared by policy and API key permission lists. */
export function areaLabel(area: string): string {
  const labels: Record<string, string> = {
    api_key: "API keys",
    permission_policy: "Permission policies",
    membership: "Members",
    team: "Teams",
    agent: "Agents",
  };
  return (
    labels[area] ??
    area.replaceAll("_", " ").replace(/^./, (c) => c.toUpperCase())
  );
}

/** Compact display names; edition-owned permissions keep their registry description. */
export function permissionLabel(key: string, description?: string): string {
  const labels: Record<string, string> = {
    "organization.read": "View organization settings",
    "organization.settings.write": "Edit organization settings",
    "organization.delete": "Delete organization",
    "membership.read": "View members",
    "membership.invite": "Invite members",
    "membership.write": "Manage member status",
    "permission_policy.read": "View permission policies",
    "permission_policy.write": "Manage permission policies",
    "team.read": "View teams",
    "team.write": "Manage teams",
    "team.member.write": "Manage team members",
    "team.escalation.write": "Edit team escalation policies",
    "team.escalation.activate": "Activate team escalation policies",
    "agent.read": "View agents",
    "agent.write": "Manage agents",
    "agent.credential.issue": "Issue agent credentials",
    "agent.credential.revoke": "Revoke agent credentials",
    "pipeline.read": "View approval pipelines",
    "pipeline.write": "Edit approval pipelines",
    "pipeline.activate": "Activate approval pipelines",
    "escalation_path.read": "View escalation paths",
    "escalation_path.write": "Edit escalation paths",
    "escalation_path.activate": "Activate escalation paths",
    "request.create": "Create requests",
    "request.read": "View requests",
    "request.claim": "Claim requests",
    "request.decide": "Approve or deny assigned requests",
    "request.decide.unrouted": "Decide others’ requests (break glass)",
    "request.read.unrouted": "View others’ requests",
    "webhook.read": "View webhooks",
    "webhook.write": "Manage webhooks",
    "connection.read": "View connections",
    "connection.write": "Manage connections",
    "api_key.issue": "Create personal API keys",
    "api_key.read": "View all API keys",
    "api_key.revoke": "Revoke any API key",
  };
  return labels[key] ?? extensions.permissionLabels[key] ?? description ?? key;
}
