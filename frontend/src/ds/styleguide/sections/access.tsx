import { PermissionsSummary } from "../../../app/permission-summary";
import { useState } from "react";
import {
  PermissionPolicyFields,
  type PermissionPolicyDraft,
} from "../../../app/permission-policies";
import { SessionContext } from "../../../app/principal";
import type { PermissionDescriptor, Session } from "../../../types";
import { Section, Demo } from "../section";

const permissions: PermissionDescriptor[] = [
  {
    key: "agent.credential.issue",
    area: "agent",
    description: "Issue instance credentials and provisioner tokens",
    holders: ["human"],
    resource: "agent",
    dangerous: true,
  },
  {
    key: "team.read",
    area: "team",
    description: "View teams and their rosters",
    holders: ["human"],
    resource: "team",
    dangerous: false,
  },
  {
    key: "team.member.write",
    area: "team",
    description: "Change who belongs to a team",
    holders: ["human"],
    resource: "team",
    dangerous: false,
  },
  {
    key: "pipeline.read",
    area: "pipeline",
    description: "Read approval pipelines",
    holders: ["human"],
    resource: "agent",
    dangerous: false,
  },
  {
    key: "pipeline.write",
    area: "pipeline",
    description: "Edit approval pipelines",
    holders: ["human"],
    resource: "agent",
    dangerous: false,
  },
  {
    key: "organization.read",
    area: "organization",
    description: "Read organization settings",
    holders: ["human"],
    resource: "organization",
    dangerous: false,
  },
];
const session: Session = {
  principal: {
    actor_type: "human",
    actor_id: "person",
    organization_id: "organization",
    membership_id: "membership",
    permissions: permissions.map((p) => p.key),
    grants: [
      ...permissions.map((p) => ({
        permission: p.key,
        scope_kind: "organization" as const,
      })),
      { permission: "permission_policy.write", scope_kind: "organization" },
    ],
  },
  assignments: [],
};
export function AccessSection() {
  const [value, setValue] = useState<PermissionPolicyDraft>({
    scope_kind: "agent",
    resource_ids: ["refund-agent"],
    permissions: ["pipeline.read"],
  });
  return (
    <Section
      id="access"
      title="Permission policies"
      lede="Choose the scope first, then resources and individual permissions. Roles stay internal."
    >
      <Demo
        label="Compact permission summary"
        hint="Hover or focus the count for a compact table of permissions, scopes, and access sources."
      >
        <div className="w-[150px]">
          <PermissionsSummary
            definitions={permissions}
            assignments={[
              {
                id: "direct",
                principal_kind: "membership",
                principal_id: "example-member",
                permissions: ["pipeline.read"],
                is_owner: false,
                scope_kind: "organization",
                source: "manual",
                created_at: "2026-09-15T00:00:00Z",
              },
              {
                id: "inherited",
                principal_kind: "team",
                principal_id: "payments",
                permissions: ["pipeline.read", "pipeline.write"],
                is_owner: false,
                scope_kind: "agent",
                scope_id: "refund-agent",
                scope_name: "Refund agent",
                source: "manual",
                via_team_name: "Payments and customer operations",
                via_team_id: "payments",
                created_at: "2026-09-15T00:00:00Z",
              },
            ]}
          />
        </div>
      </Demo>
      <Demo
        label="Permission policy editor"
        hint="Changing scope clears the previous selection. Organization scope fixes Applies to at Entire organization."
      >
        <SessionContext.Provider
          value={{ ...session, signOut: () => undefined }}
        >
          <PermissionPolicyFields
            value={value}
            onChange={setValue}
            permissions={permissions}
            resources={[
              { id: "support", name: "Support", kind: "team" },
              { id: "refund-agent", name: "Refund agent", kind: "agent" },
            ]}
          />
        </SessionContext.Provider>
      </Demo>
    </Section>
  );
}
