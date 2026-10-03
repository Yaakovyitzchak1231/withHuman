/* Public documentation destinations, independent of the website's source. */
const DOCS_ORIGIN = "https://withhuman.ai/docs";

export interface DocsPage {
  route: string;
  slug: string;
  title: string;
  description: string;
  anchor?: string;
  search?: Record<string, string>;
}

const OVERVIEW = {
  slug: "getting-started/overview",
  title: "withHuman Overview",
  description: "The approval layer for AI agents.",
};

export const DOCS_PAGES: DocsPage[] = [
  {
    route: "queue",
    slug: "web-app/review-queue",
    title: "Review queue",
    description: "Review and decide approval requests that need a human.",
  },
  {
    route: "agents",
    slug: "web-app/agents",
    title: "Agents",
    description: "Connect agents and manage their instances and credentials.",
  },
  {
    route: "pipelines",
    slug: "web-app/approval-pipelines",
    title: "Approval pipelines",
    description: "Define how requests are evaluated and when they reach human review.",
  },
  {
    route: "escalation-paths",
    slug: "web-app/escalation-paths",
    title: "Escalation paths",
    description: "Choose who reviews requests and how they escalate over time.",
  },
  {
    route: "webhooks",
    slug: "web-app/webhook-endpoints",
    title: "Webhook endpoints",
    description: "Manage the endpoints that approval pipelines call for external decisions.",
  },
  {
    route: "settings",
    ...OVERVIEW,
  },
  {
    route: "connect",
    slug: "web-app/agents",
    anchor: "agent-creation",
    title: "Agents",
    description: "Connect agents and manage their instances and credentials.",
  },
  {
    route: "welcome",
    slug: "getting-started/quickstart",
    title: "Quickstart",
    description: "Connect a local agent, describe which calls need your approval, and decide your first request.",
  },
];

/* An edition adds the pages for its own screens before the router
 * renders; the docs-links check reads both editions' lists. */
export function registerDocsPages(entries: { pages?: DocsPage[]; screens?: DocsPage[] }): void {
  if (entries.pages) DOCS_PAGES.push(...entries.pages);
  if (entries.screens) DOCS_SCREEN_PAGES.push(...entries.screens);
}

/** Search-specific screens precede their general screen and section guides. */
export const DOCS_SCREEN_PAGES: DocsPage[] = [
  {
    route: "/agents/$agentSlug",
    search: { tab: "tools" },
    slug: "web-app/agents",
    anchor: "tools",
    title: "Agents",
    description: "Connect agents and manage their instances and credentials.",
  },
  {
    route: "/agents/$agentSlug/instances/new",
    slug: "web-app/agents",
    anchor: "instance-connections",
    title: "Agents",
    description: "Connect agents and manage their instances and credentials.",
  },
  {
    route: "/connect/authorize",
    slug: "web-app/agents",
    anchor: "cli-enrollment",
    title: "Agents",
    description: "Connect agents and manage their instances and credentials.",
  },
  {
    route: "/oauth/authorize",
    slug: "web-app/mcp-server",
    title: "withHuman MCP server",
    description: "Let an AI agent set up approval pipelines, escalation paths, teams, agents and webhook endpoints for you.",
  },
  {
    route: "/agents/$agentSlug",
    search: { tab: "provisioners" },
    slug: "web-app/provisioner-tokens",
    title: "Provisioner tokens",
    description: "Register agent instances automatically and manage provisioner tokens.",
  },
  {
    route: "/settings/account",
    slug: "web-app/account",
    title: "Account",
    description: "Manage your password and understand how you sign in to withHuman.",
  },
  {
    route: "/settings/tool-presentations",
    slug: "web-app/tool-presentations",
    title: "Tool presentations",
    description: "Make tool requests easier to read with clear titles and formatted arguments.",
  },
  {
    route: "/settings/tool-presentations/new",
    slug: "web-app/tool-presentations",
    title: "Tool presentations",
    description: "Make tool requests easier to read with clear titles and formatted arguments.",
  },
  {
    route: "/settings/tool-presentations/$presentationId",
    slug: "web-app/tool-presentations",
    title: "Tool presentations",
    description: "Make tool requests easier to read with clear titles and formatted arguments.",
  },
  {
    route: "/settings/organization",
    slug: "web-app/organization-policies",
    title: "Organization policies",
    description: "Manage organization-wide rules for reviews, request visibility and API keys.",
  },
  {
    route: "/settings/integrations",
    slug: "web-app/integrations",
    title: "Integrations",
    description: "Manage your organization's connections to external services.",
  },
  {
    route: "/settings/notifications",
    slug: "web-app/notifications",
    title: "Notifications",
    description: "Choose how withHuman reaches you for standard and urgent requests.",
  },
  {
    route: "/settings/notifications/add",
    search: { kind: "email" },
    slug: "web-app/email-notifications",
    title: "Email",
    description: "Receive approval notifications at your account email or another confirmed address.",
  },
  {
    route: "/settings/notifications/add",
    search: { kind: "slack" },
    slug: "web-app/slack-notifications",
    title: "Slack",
    description: "Receive personal approval notifications in your connected Slack workspace.",
  },
  {
    route: "/settings/notifications/add",
    search: { kind: "discord" },
    slug: "web-app/discord-notifications",
    title: "Discord",
    description: "Receive personal approval notifications through your connected Discord server.",
  },
  {
    route: "/settings/notifications/add",
    slug: "web-app/notifications",
    title: "Notifications",
    description: "Choose how withHuman reaches you for standard and urgent requests.",
  },
  {
    route: "/settings/personal-api-keys",
    slug: "web-app/personal-api-keys",
    title: "Personal API keys",
    description: "Manage keys that let scripts act with your current permissions.",
  },
  {
    route: "/settings/personal-api-keys/new",
    slug: "web-app/personal-api-keys",
    title: "Personal API keys",
    description: "Manage keys that let scripts act with your current permissions.",
  },
  {
    route: "/settings/members",
    slug: "web-app/membership-management",
    title: "Membership management",
    description: "Invite people, manage member access and control who belongs to your organization.",
  },
  {
    route: "/settings/teams",
    slug: "web-app/team-administration",
    title: "Team administration",
    description: "Manage teams, their members and the permissions they inherit.",
  },
  {
    route: "/settings/teams/$teamId",
    slug: "web-app/team-administration",
    title: "Team administration",
    description: "Manage teams, their members and the permissions they inherit.",
  },
  {
    route: "/settings/permissions/teams/$principalId",
    slug: "web-app/permissions",
    title: "Permissions",
    description: "Manage direct and inherited access, permission scopes and decisions outside routing.",
  },
  {
    route: "/settings/permissions/members/$principalId",
    slug: "web-app/permissions",
    title: "Permissions",
    description: "Manage direct and inherited access, permission scopes and decisions outside routing.",
  },
  {
    route: "/settings/teams/$teamId/escalation-policy",
    slug: "web-app/teams",
    title: "Teams",
    description: "Choose who on a team is notified and how requests escalate.",
  },
];

/** Detail routes share their section's article unless they have a guide.
 * Among the screens a path matches, the one naming the most search
 * keys wins, so a kind's guide beats its screen's general one wherever
 * an edition registered it in the list. */
export function docsForPath(pathname: string, search: Record<string, unknown> = {}) {
  const segments = pathname.replace(/\/$/, "").split("/");
  const specificity = (page: DocsPage) => Object.keys(page.search ?? {}).length;
  const screen = DOCS_SCREEN_PAGES.filter((page) => {
    const pattern = page.route.split("/");
    return pattern.length === segments.length && pattern.every((part, index) =>
      part.startsWith("$") ? segments[index] !== "" : part === segments[index],
    ) && Object.entries(page.search ?? {}).every(([key, value]) => search[key] === value);
  }).reduce<DocsPage | undefined>((best, page) => (best && specificity(best) >= specificity(page) ? best : page), undefined);
  const page = screen ?? DOCS_PAGES.find((page) => page.route === segments[1]);
  const { slug, title, description } = page ?? OVERVIEW;
  return {
    href: `${DOCS_ORIGIN}/${slug}${page?.anchor ? `#${page.anchor}` : ""}`,
    title,
    description,
  };
}
