# Member and team permission policies

Status: implemented. 2026-09-15.

This proposal follows the requested member/team journey. The supplied screenshots
illustrate interaction and information order, not withHuman styling or terminology.
It supersedes the role-authoring and progressive scope-disclosure UX in
`10-rbac.md` for this change. The permission evaluator remains the foundation.

## RBAC model before this change

| Relationship | Cardinality | Storage / behavior |
| --- | --- | --- |
| Permission to role | Many-to-many | Code owns the permission registry. Each role stores an array of permission keys in `roles.permissions`; there is no permission join table. |
| Role to role assignment | One-to-many | Each `role_assignments` row references exactly one `role_id`. |
| Principal to role assignment | One-to-many | Each assignment has one recipient: an organization membership, a team, or an agent. |
| Scope to role assignment | One-to-many | Each assignment applies at exactly one organization, team, or agent scope. Scope is a value on the assignment, not a separate table. |
| Member to team | Many-to-many | Live team membership contributes the team's assignments to the member. |

`RoleAssignment` is the code's name for what the request calls `RoleBinding`.
`AssignmentSpec` is its write shape: `role_id`, `scope_kind`, and `scope_id`.
Organization scope has no resource ID; team scope uses a team UUID; agent
scope uses the agent's slug.

A role says **what**. An assignment says **who receives it and where it applies**.
The recipient and the resource are independent. For example, the Support team
can receive pipeline permissions for the agent `refund-agent`. Being the
recipient does not mean the scope is the Support team.

At session resolution, `internal/postgres/roles.go:loadGrants` joins direct and
inherited assignments to role permission arrays and produces distinct grants:
`permission + scope_kind + scope_id`. `internal/authorization/check.go` checks
those grants against the actual resource. `Scope` supplies list filtering;
`HoldsAnywhere` supplies coarse route/navigation access. The flat permissions
array alone is not authorization for a particular resource.

Access is additive. There is no explicit deny and `write` does not imply `read`.
Removing one direct grant does not remove matching access from another policy
or team. An organization grant covers that permission across the organization.
Teams and agents are sibling scopes, not an automatic hierarchy.

Built-in owner, admin, reviewer, auditor, and agent roles are code-owned and
reconciled at startup. Customers cannot edit built-in roles. Custom-role
authoring is not built in either edition; when the hosted edition adds it,
editing a custom role changes the permission bundle for every assignment that
references it, which is the shared effect this new UX must avoid.

### Constraints considered in the design

- Request read/claim/decide permissions are organization-scoped. Routing further
  restricts review/decision eligibility. Agent scope currently controls
  agents and pipelines, not review of requests from those agents.
- Team scope controls team management, roster changes, and team escalation
  policies. It does not automatically control agents used by that team.
- `membership.role.assign` has a scope resource, so delegation can be scoped.
  However, the member assignment service currently requires organization scope,
  while team assignments accept a scoped delegator.
- Custom-role grants require the actor to hold each permission at the requested
  scope or wider. Built-in non-owner grants currently bypass that per-permission
  restriction, allowing an admin to invite a reviewer without being a reviewer.
- Direct self-mutation and direct edits to directory-managed memberships are
  currently rejected. Directory-sourced and team-inherited assignments have
  independent provenance. Archived teams are read-only.
- Ownership is protected using the built-in owner assignment, not by comparing
  a role's permission set. Equivalent permissions do not make someone an owner.

## Product language and entry points

Use **Permissions** for the page/tab and **permission policy** for a saved row.
A policy is an allow-only permission set for one recipient over one scope kind
and one or more selected resources. It is unrelated to approval pipelines or
team escalation policies.

- Members: row menu **Edit permissions** opens that member's Permissions page.
- Teams: row menu **Edit permissions** opens that team's Permissions page.
- Team detail: add a Permissions tab/entry beside existing member, escalation,
  and settings surfaces. Preserve existing escalation functionality.
- Member detail: direct policies first, inherited policies second, with a link
  to the source team. A member can inspect inherited access but edits it at its
  source if authorized.
- Remove the Roles navigation, role pages, role badges, custom-role upsells,
  role pickers, and role-oriented search/help copy. Invitations and directory
  mapping must use the same policy vocabulary rather than exposing role IDs.
- Ownership may be displayed separately as **Organization ownership**, a
  protected system status. It is not a selectable convenience role or a normal
  editable policy. Preserve the existing ownership lifecycle and last-owner
  protection; do not silently replace owner assignments with generated bundles.

No role name, role key, role ID, or role-selection step appears in this flow.
Predefined convenience roles are explicitly outside this change.

## Policy list

Header identifies the recipient: **Permissions for Support** or **Permissions
for Alex Morgan**. Team helper text: “Every member of this team receives these
permissions while they belong to it.”

Use a searchable table with **Scope**, **Applies to**, **Permissions**, and row
actions. Display resource names as reference chips. Show the first few permission
labels and an expandable remainder. Each editable row has **Edit** and **Remove**.
The primary action is **Add permission policy**. A count counts saved policies,
not generated bindings or inherited recipients.

An inherited row identifies its source, for example **Inherited from Support**,
and offers **View team permissions** instead of direct edit/remove. Directory
rows identify their source and remain read-only where directory ownership
currently requires it. Empty and failed loads are separate states.

## Add and edit journey

Use the same dedicated editor for members and teams. The recipient is already
known from navigation. No policy-name or description field is required.

1. Show **Scope** on the left and **Applies to** on the right.
2. Scope options are **Organization**, **Teams**, and **Agents**, always
   visible even when the organization contains only one team or agent.
3. Initially neither scope nor resource is selected. The right control is
   disabled and the body says “Select a scope to choose permissions.”
4. Organization sets the right control to **Entire organization**, disabled.
5. Teams shows a searchable resource dropdown populated with live teams.
   Agents shows the equivalent dropdown of existing agents, each option
   showing the agent name with its slug.
   Support multiple selections, displayed as pills inside the dropdown trigger.
   Toggle options in the dropdown to remove selections. All selected resources
   receive the same permission set. One resource is a valid selection.
6. After choosing a scope, show the applicable permission checklist below it.
   Search matches friendly labels, descriptions, and technical keys. Group by
   area. Each row has a checkbox, action label, and short explanation; technical
   keys are secondary. Offer a group select-all with an indeterminate state.
7. Save is enabled only with a scope, at least one permission, and, for resource
   scopes, at least one resource. A selected resource outside the actor's authority
   produces an explicit validation error. Do not partially save a selection.
8. **Save policy** returns to the recipient's list with the saved row and a
   “Permissions updated” notification. Edit opens the same form prepopulated.

Changing the scope clears resource selections and selected permissions so an
old selection cannot silently widen to the organization. Searching never clears
hidden selections. Cancelling writes nothing. Show dirty-form navigation
protection. Save errors retain the draft, and a stale edit requires a reload
rather than overwriting another administrator's work.

Organization policies cover current and future resources of the applicable
permission kind. Resource policies cover only the explicitly selected resources.
Do not include an “all teams” selection that can be mistaken for organization
scope. Empty resource inventories have a meaningful empty state and retry on a
load failure; never silently hide a scope because its inventory failed.

### Applicable permissions

| Scope | Permission resources that apply | Examples |
| --- | --- | --- |
| Organization | All human-applicable resources | Organization settings, members, teams, agents, pipelines, requests, audit, connections, billing when registered |
| Teams | `team`, `scope` | Read teams, edit the selected teams, edit their rosters, edit/activate escalation policies, delegate permissions within the selected teams |
| Agents | `agent`, `scope` | Read/manage agents, issue/revoke credentials, read/edit/activate pipelines, delegate permissions for the selected agents |

Derive scope applicability and human eligibility from the backend registry.
Remove role-management keys from the customer-facing model; expose permission
management using `permission_policy.read` and `permission_policy.write` with
scope-aware checks. Do not offer `request.create` or `gateway.call` to humans.
Do not offer billing, request decisions, or organization settings at resource
scope. Checkboxes remain independent; no silent `write` to `read` implication.

For selected resources, the server returns which permissions the actor can
delegate across **every** selection. Scope-compatible permissions outside that
authority can be shown disabled with a reason. Server validation is authoritative.

## Backend design

This cannot be implemented honestly as a relabelled role picker. The browser
must submit explicit permission keys, and the server owns the internal roles.

### Public policy shape

Example write body on the recipient's collection:

```json
{
  "scope_kind": "agent",
  "resource_ids": ["refund-agent", "support-agent"],
  "permissions": ["pipeline.read", "pipeline.write"]
}
```

Organization scope requires `resource_ids: []`. Team scope lists team IDs and
agent scope lists agent slugs. Read responses add the policy ID, revision,
resolved resource labels (the agent name for an agent slug), provenance, and
action capabilities.
They do not include internal role identities.

- Member collection: `/api/v1/members/{id}/permission_policies`.
- Team collection: `/api/v1/teams/{id}/permission_policies`.
- `GET` lists policies; `POST` creates; `GET`, `PUT`, and `DELETE` on a policy ID
  read, replace, and remove that policy. Mutations use idempotency keys and
  updates/removals require the observed revision.
- Keep `/api/v1/permissions` as the permission vocabulary endpoint, with
  scope-aware catalog access and delegation metadata. Resource options come
  from an authorized policy-editor read rather than unrelated broad inventory
  permissions or a partial page of the agents list.

### Internal representation

Add a durable `permission_policies` identity to group the resources selected in
one edit. Each policy belongs to one organization and one member or team.
Each current policy references one internally generated permission bundle
(a role). One assignment per selected resource binds that role to the recipient;
organization scope produces exactly one assignment.

Thus one current policy has **one internal role and one-to-many assignments**.
Persist the policy identity on its assignments. Do not reconstruct user policies
by guessing which bindings happen to share a permission set.

Generated roles are immutable snapshots and are not shared across unrelated
policies. A permission edit creates a replacement snapshot and switches this
policy's bindings atomically. Resource-only edits can retain the same snapshot.
This avoids changing another member's access through a shared role edit.
Obsolete unreferenced generated snapshots can be deleted after recording the
before/after permissions in the audit event. System built-ins remain code-owned.

Create/update/delete the policy, its internal bundle, its assignments, and its
audit event in one transaction. Validate resources and recheck the actor's live
authority in that transaction. Keep the existing grant loader and resource
checker; authorization still runs on flattened permission/scope grants.

Remove public custom-role mutation and role-catalog endpoints in both editions,
and replace assignment writes with policy writes. Update API DTOs and consumers,
including invitation defaults and directory mapping, in the same implementation.
The policy mechanism belongs to core; it must not require the SaaS custom-role
authoring capability merely because it stores an internal role.

### Authorization requirements

- Apply the no-escalation check to every selected permission at every selected
  resource. Generated bundles must not use the current built-in-role bypass.
  Consequently, an admin without `request.decide` cannot grant that permission
  through this editor. This intentionally removes the current built-in reviewer
  exception; invitation defaults must obey the same rule.
- Check both the old and new scope when moving or deleting a policy. The current
  replace-all team service validates submitted grants but does not authorize each
  removed grant's scope. Per-policy mutation must close that gap, including an
  empty replacement, instead of carrying it into the new API.
- Permit scoped member-policy editing only with authority over that policy's
  scope and authorized access to the recipient. Do not require organization-wide
  delegation merely to edit an agent policy on a member.
- Preserve current self-mutation restrictions, directory ownership, archived-team
  rules, dangerous-permission reauthentication/MFA, and last-owner invariants.
  Team-mediated changes to the acting member's own effective access also need
  evaluation and an explicit loss-of-access indication.
- Preserve independent grants. Removing a policy does not revoke access granted
  elsewhere. Show remaining inherited/direct sources when relevant.
- Do not claim that generated bundles with all human permissions confer ownership.
  The protected owner assignment stays separate from ordinary policy edits.

## Implementation and verification

1. Add policy domain/port types, transaction methods, registry scope metadata,
   authorization coverage, and policy API contracts in core.
2. Replace role management and assignment APIs/consumers, including hosted
   authorship wiring, invite flow, and directory mapping. Use a new schema
   migration for deployed databases; do not edit applied migration files or
   retain obsolete public API compatibility paths.
3. Replace `AssignmentDialog`/`AssignmentPicker` with shared policy list and
   editor components. Reuse `Page`/`SettingsPage`, `PageHeader`, reference chips,
   table, select/multiselect, checkbox, and toast primitives. Adapt the permission
   matrix to labels and scope filtering. Load page data through route loaders.
4. Update role references in navigation, help, docs, and the living styleguide.
   Keep porcelain/onyx surfaces, semantic tokens, hairline borders, existing
   typography, and 20px card radii. Do not copy screenshot branding.
5. Test scope filtering, cross-organization refusal, multi-resource atomicity,
   direct and inherited union, edit isolation, removal authorization, concurrent
   stale updates, directory restrictions, owner preservation, and reauthentication.
6. Run core `make verify`, applicable SaaS tests, rebuild embedded frontend with
   `make frontend`, and verify the actual member/team journey through the full
   stack. Verify keyboard use and narrow layouts. If marketing docs change, run
   the required docs-link checks and preview workflow too.

The following notes describe the completed implementation of this design.

## Implementation notes

- Core serves the member and team permission policy API. Public role catalogs,
  role mutation, and assignment replacement endpoints have been removed.
- The editor shares the scope/resource/checklist controls with invitations.
  Directory admission and JIT accept explicit permission sets too.
- Policy mutation is transactional, uses observed revisions and idempotency
  receipts, and refreshes the client's session after inherited access changes.
- Generated snapshots are retained internally; no user can edit or select them.
  Predefined convenience roles remain out of scope.
