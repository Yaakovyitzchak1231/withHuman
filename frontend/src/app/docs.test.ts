import { describe, expect, it } from "vitest";
import { docsForPath, registerDocsPages } from "./docs";

describe("contextual documentation", () => {
  it("opens the instance connection guide for new instances", () => {
    expect(docsForPath("/agents/support/instances/new").href).toBe("https://withhuman.ai/docs/web-app/agents#instance-connections");
  });
  it("opens the MCP server guide for the OAuth consent page", () => {
    expect(docsForPath("/oauth/authorize", { client_id: "c" }).href).toBe("https://withhuman.ai/docs/web-app/mcp-server");
  });
  it("opens the agent guide at creation or enrollment for the connect screens", () => {
    for (const trailingSlash of ["", "/"]) {
      expect(docsForPath(`/connect${trailingSlash}`).href).toBe("https://withhuman.ai/docs/web-app/agents#agent-creation");
      expect(docsForPath(`/connect/authorize${trailingSlash}`, { user_code: "TEST-CODE" }).href).toBe("https://withhuman.ai/docs/web-app/agents#cli-enrollment");
    }
  });

  it("follows the selected agent tab and ignores unrelated search filters", () => {
    for (const trailingSlash of ["", "/"]) {
      const path = `/agents/support${trailingSlash}`;
      expect(docsForPath(path, { tab: "provisioners", page: 2, q: "token" }).href).toBe("https://withhuman.ai/docs/web-app/provisioner-tokens");
      for (const tab of [undefined, "instances", "requests", "unknown"]) {
        expect(docsForPath(path, { tab }).href).toBe("https://withhuman.ai/docs/web-app/agents");
      }
    }
    expect(docsForPath("/agents", { tab: "provisioners" }).title).toBe("Agents");
  });

  it("opens each notification channel guide only on its setup screen", () => {
    const channels = [
      ["email", "email-notifications"],
      ["slack", "slack-notifications"],
      ["discord", "discord-notifications"],
    ];
    for (const [kind, slug] of channels) {
      for (const trailingSlash of ["", "/"]) {
        expect(docsForPath(`/settings/notifications/add${trailingSlash}`, { kind }).href).toBe(`https://withhuman.ai/docs/web-app/${slug}`);
      }
      expect(docsForPath("/settings/notifications", { kind }).title).toBe("Notifications");
    }
    for (const kind of [undefined, "unknown", ["email"], { value: "email" }]) {
      expect(docsForPath("/settings/notifications/add", { kind }).title).toBe("Notifications");
    }
  });

  it("lets a channel guide an edition registers win over the general setup screen", () => {
    registerDocsPages({
      screens: [{ route: "/settings/notifications/add", search: { kind: "pager" }, slug: "web-app/pager-notifications", title: "Pager", description: "Get paged." }],
    });
    expect(docsForPath("/settings/notifications/add", { kind: "pager" }).href).toBe("https://withhuman.ai/docs/web-app/pager-notifications");
    expect(docsForPath("/settings/notifications/add", { kind: "email" }).title).toBe("Email");
    expect(docsForPath("/settings/notifications/add").title).toBe("Notifications");
  });

  it("opens the account guide for the account page", () => {
    for (const trailingSlash of ["", "/"]) {
      expect(docsForPath(`/settings/account${trailingSlash}`)).toEqual({
        href: "https://withhuman.ai/docs/web-app/account",
        title: "Account",
        description: "Manage your password and understand how you sign in to withHuman.",
      });
    }
    expect(docsForPath("/settings/account/extra").title).toBe("withHuman Overview");
  });

  it("leaves the sign-in and directory page to the edition that serves it", () => {
    expect(docsForPath("/settings/identity").title).toBe("withHuman Overview");
  });

  it("opens Tool presentations for the inventory and editors", () => {
    for (const path of ["/settings/tool-presentations", "/settings/tool-presentations/new", "/settings/tool-presentations/refund-policy"]) {
      for (const trailingSlash of ["", "/"]) {
        expect(docsForPath(`${path}${trailingSlash}`)).toEqual({
          href: "https://withhuman.ai/docs/web-app/tool-presentations",
          title: "Tool presentations",
          description: "Make tool requests easier to read with clear titles and formatted arguments.",
        });
      }
    }
    expect(docsForPath("/settings/tool-presentations/refund-policy/extra").title).toBe("withHuman Overview");
  });

  it("opens Organization policies for organization settings", () => {
    for (const path of ["/settings/organization", "/settings/organization/"]) {
      expect(docsForPath(path)).toEqual({
        href: "https://withhuman.ai/docs/web-app/organization-policies",
        title: "Organization policies",
        description: "Manage organization-wide rules for reviews, request visibility and API keys.",
      });
    }
    expect(docsForPath("/settings/organization/extra").title).toBe("withHuman Overview");
  });

  it("opens Integrations for organization service connections", () => {
    for (const path of ["/settings/integrations", "/settings/integrations/"]) {
      expect(docsForPath(path)).toEqual({
        href: "https://withhuman.ai/docs/web-app/integrations",
        title: "Integrations",
        description: "Manage your organization's connections to external services.",
      });
    }
    expect(docsForPath("/settings/integrations/extra").title).toBe("withHuman Overview");
  });

  it("opens Notifications for personal rules and channel setup", () => {
    for (const path of ["/settings/notifications", "/settings/notifications/add"]) {
      for (const trailingSlash of ["", "/"]) {
        expect(docsForPath(`${path}${trailingSlash}`)).toEqual({
          href: "https://withhuman.ai/docs/web-app/notifications",
          title: "Notifications",
          description: "Choose how withHuman reaches you for standard and urgent requests.",
        });
      }
    }
    expect(docsForPath("/settings/notifications/add/extra").title).toBe("withHuman Overview");
  });

  it("opens the personal API key guide for the inventory and creation screens", () => {
    for (const suffix of ["", "/new"]) {
      for (const trailingSlash of ["", "/"]) {
        expect(docsForPath(`/settings/personal-api-keys${suffix}${trailingSlash}`)).toEqual({
          href: "https://withhuman.ai/docs/web-app/personal-api-keys",
          title: "Personal API keys",
          description: "Manage keys that let scripts act with your current permissions.",
        });
      }
    }
    expect(docsForPath("/settings/personal-api-keys/new/extra").title).toBe("withHuman Overview");
  });

  it("opens membership management for the Members page", () => {
    for (const path of ["/settings/members", "/settings/members/"]) {
      expect(docsForPath(path)).toEqual({
        href: "https://withhuman.ai/docs/web-app/membership-management",
        title: "Membership management",
        description: "Invite people, manage member access and control who belongs to your organization.",
      });
    }
  });

  it("opens the Teams guide for a team's escalation policy", () => {
    for (const path of ["/settings/teams/payments/escalation-policy", "/settings/teams/payments/escalation-policy/"]) {
      expect(docsForPath(path)).toEqual({
        href: "https://withhuman.ai/docs/web-app/teams",
        title: "Teams",
        description: "Choose who on a team is notified and how requests escalate.",
      });
    }
  });

  it("opens team administration for the list and detail", () => {
    for (const path of ["/settings/teams", "/settings/teams/", "/settings/teams/payments", "/settings/teams/payments/"]) {
      expect(docsForPath(path)).toEqual({
        href: "https://withhuman.ai/docs/web-app/team-administration",
        title: "Team administration",
        description: "Manage teams, their members and the permissions they inherit.",
      });
    }
  });

  it("opens Permissions for member and team policies", () => {
    for (const path of ["/settings/permissions/members/alice", "/settings/permissions/members/alice/", "/settings/permissions/teams/payments", "/settings/permissions/teams/payments/"]) {
      expect(docsForPath(path)).toEqual({
        href: "https://withhuman.ai/docs/web-app/permissions",
        title: "Permissions",
        description: "Manage direct and inherited access, permission scopes and decisions outside routing.",
      });
    }
  });

  it("uses the overview for settings screens without a dedicated guide", () => {
    for (const path of ["/settings", "/settings/permissions/members/", "/settings/permissions/teams/payments/extra", "/settings/permissions/api_keys/key", "/settings/appearance", "/settings/teams//escalation-policy", "/settings/teams/payments/escalation-policy/extra"]) {
      expect(docsForPath(path).href).toBe("https://withhuman.ai/docs/getting-started/overview");
    }
  });

  it("keeps escalation paths and unknown routes on their existing guides", () => {
    expect(docsForPath("/escalation-paths/payments").href).toBe("https://withhuman.ai/docs/web-app/escalation-paths");
    expect(docsForPath("/unknown").href).toBe("https://withhuman.ai/docs/getting-started/overview");
  });
});
