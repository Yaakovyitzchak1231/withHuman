import { useState, type FormEvent } from "react";
import { ToggleGroup, ToggleGroupItem } from "@/ds/components/toggle-group";
import { Avatar } from "@/ds/components/avatar";
import { Referenced } from "@/ds/components/reference";
import { ReferenceChip } from "./reference-chip";
import { Badge, type Tone } from "@/ds/components/badge";
import { Button } from "@/ds/components/button";
import { Callout } from "@/ds/components/callout";
import { PermissionsSummary } from "./permission-summary";
import { CodeBlock } from "@/ds/components/code-block";
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogTitle,
} from "@/ds/components/dialog";
import { Disclosure } from "@/ds/components/disclosure";
import { EmptyState } from "@/ds/components/empty-state";
import type { MenuAction } from "@/ds/components/menu";
import { SettingRow } from "@/ds/components/setting-row";
import { SettingsPage } from "@/ds/components/settings-page";
import { SettingsSection } from "@/ds/components/settings-section";
import { Table, type TableColumn } from "@/ds/components/table";
import { TextField } from "@/ds/components/text-field";
import { useToast } from "@/ds/components/toast";
import { PeopleIcon, PersonIcon, ShieldIcon } from "@/ds/icons";
import { Link, useNavigate, useRouter } from "@tanstack/react-router";
import { api, RequestError } from "../api";
import type {
  Invitation,
  Member,
  MembershipInEscalationPathDetails,
  MembershipStatus,
  TargetedEscalationPath,
  TargetedTeamEscalationPolicy,
  Team,
} from "../types";
import {
  PermissionPolicyFields,
  emptyPolicy,
  policyValid,
  type PermissionPolicyDraft,
} from "./permission-policies";
import type { PermissionPoliciesPage } from "../types";
import { useAccess } from "./authorization";
import { extensionLink, extensions, useAuditLinks } from "./extensions";
import { capitalize, formatRelative, formatUntil } from "./format";
import { useTitle } from "./use-title";

const STATUS_TONE: Record<MembershipStatus, Tone> = {
  active: "approve",
  suspended: "warn",
  deprovisioned: "neutral",
};

/* What the members route loader delivers. */
export interface MembersPage {
  members: Member[];
  teams: Team[];
  invitations: Invitation[];
  error: string;
  permissionOptions: PermissionPoliciesPage | null;
}

export function MembersScreen({
  page,
  canRead,
  canInvite,
  canWrite,
  canAssign,
  isOwner,
  selfMembershipId,
}: {
  page: MembersPage;
  canRead: boolean;
  canInvite: boolean;
  /* Suspend, reactivate, and remove. */
  canWrite: boolean;
  /* Change a member's permissions. */
  canAssign: boolean;
  isOwner: boolean;
  selfMembershipId?: string;
}) {
  useTitle("Members");
  const toast = useToast();
  const router = useRouter();
  const auditLinkFor = useAuditLinks();
  const navigate = useNavigate();
  const { members, teams, invitations, error } = page;
  /* Deprovisioned members stay on the record but not on the roster:
   * the table shows provisioned members unless asked for everyone. */
  const [roster, setRoster] = useState<"provisioned" | "all">("provisioned");
  const [query, setQuery] = useState("");
  const deprovisionedCount = members.filter(
    (member) => member.status === "deprovisioned",
  ).length;
  const teamNames = new Map(teams.map((team) => [team.id, team.name]));
  /* The search matches the name, the email, a permission, or a team, so a
   * roster can be narrowed by any way a person is usually referred to. */
  const needle = query.trim().toLowerCase();
  const matches = (member: Member) =>
    needle === "" ||
    [
      member.display_name,
      member.email,
      ...member.assignments.flatMap((assignment) => assignment.permissions),
      ...member.team_ids.map((teamID) => teamNames.get(teamID) ?? ""),
    ].some((text) => (text ?? "").toLowerCase().includes(needle));
  const shown = members.filter(
    (member) =>
      (roster === "all" || member.status !== "deprovisioned") &&
      matches(member),
  );
  const [inviteOpen, setInviteOpen] = useState(false);

  const [removing, setRemoving] = useState<Member | null>(null);
  /* A suspension or removal the server refused because active escalation
   * paths still name the member; the dialog lists them and offers to
   * force the change. */
  const [blocked, setBlocked] = useState<BlockedStatusChange | null>(null);
  /* One-time invite URLs, kept only in memory for this visit. */
  const [inviteURLs, setInviteURLs] = useState<Record<string, string>>({});

  if (!canRead) {
    return (
      <SettingsPage title={TITLE} description={DESCRIPTION}>
        <Callout tone="neutral" title="Members are managed by administrators">
          You need the membership.read permission to view this page.
        </Callout>
      </SettingsPage>
    );
  }

  /* Every change is followed by a reload of the loader's page, so the
   * roster and the invitations always show what the server holds. */
  async function act(action: () => Promise<unknown>, failure: string) {
    try {
      await action();
      await router.invalidate();
      return true;
    } catch (reason) {
      toast.add({
        type: "deny",
        title: failure,
        description: reason instanceof Error ? reason.message : undefined,
      });
      return false;
    }
  }

  /* Suspend, reactivate, or remove. A refusal because active escalation
   * paths name the member is not a failure to toast: it opens the dialog
   * that lists the paths and lets the administrator force the change. */
  async function changeStatus(
    member: Member,
    status: "active" | "suspended" | "deprovisioned",
    force = false,
  ): Promise<"done" | "blocked" | "failed"> {
    try {
      if (status === "deprovisioned") await api.removeMember(member.id, force);
      else await api.setMemberStatus(member.id, status, force);
      await router.invalidate();
      return "done";
    } catch (reason) {
      if (
        status !== "active" &&
        reason instanceof RequestError &&
        reason.code === "membership_in_escalation_path"
      ) {
        const details =
          reason.details as unknown as MembershipInEscalationPathDetails;
        setBlocked({
          member,
          status,
          paths: details.escalation_paths ?? [],
          policies: details.team_escalation_policies ?? [],
        });
        return "blocked";
      }
      toast.add({
        type: "deny",
        title:
          status === "deprovisioned"
            ? "Removal failed"
            : "Status change failed",
        description: reason instanceof Error ? reason.message : undefined,
      });
      return "failed";
    }
  }

  const pending = invitations.filter(
    (invitation) => invitation.status === "pending",
  );
  /* Plain array, not useMemo: the cells close over act and the dialog
   * setters, and the roster is small. */
  const columns: TableColumn<Member>[] = [
    {
      id: "member",
      label: "Member",
      grow: true,
      cell: (member) => (
        <div className="flex min-w-0 items-center gap-3">
          <Avatar
            name={member.display_name || member.email}
            id={member.id}
            size="md"
          />
          <p className="truncate text-body font-medium text-ink">
            {member.display_name || member.email}
            {member.id === selfMembershipId ? (
              <span className="ml-2 text-label font-normal text-ink-faint">
                you
              </span>
            ) : null}
          </p>
        </div>
      ),
    },
    {
      id: "permissions",
      label: "Permissions",
      width: 150,
      info: "Hover or focus the count to see each permission, its scope, and where it comes from.",
      docsLinks: [{ href: "https://withhuman.ai/docs/web-app/permissions#direct-and-inherited-access", label: "Direct and inherited permissions" }],
      cell: (member) => (
        <div className="flex flex-col items-start gap-1">
          <PermissionsSummary
            assignments={member.assignments}
            definitions={page.permissionOptions?.permissions}
          />
          {member.directory_managed ? (
            <Badge tone="accent" icon={<ShieldIcon width={14} height={14} />}>
              Directory
            </Badge>
          ) : null}
        </div>
      ),
    },
    {
      id: "teams",
      label: "Teams",
      width: 130,
      cell: (member) => (
        <div className="flex min-w-0 flex-col items-start gap-1.5">
          {member.team_ids.map((teamID) => (
            <Referenced
              key={teamID}
              triggerClassName="min-w-0 max-w-full"
              reference={{
                kind: "team",
                id: teamID,
                name: teamNames.get(teamID) ?? "A team",
              }}
            >
              <Badge
                className="min-w-0 max-w-full"
                icon={
                  <PeopleIcon width={14} height={14} className="shrink-0" />
                }
              >
                <span className="truncate">
                  {teamNames.get(teamID) ?? `${teamID.slice(0, 8)}…`}
                </span>
              </Badge>
            </Referenced>
          ))}
        </div>
      ),
    },
    {
      id: "status",
      label: "Status",
      info: "Suspended members keep their permissions but cannot sign in. Deprovisioned members have been removed.",
      docsLinks: [{ href: "https://withhuman.ai/docs/web-app/membership-management#member-status", label: "Member status and access" }],
      width: 104,
      cell: (member) => (
        <Badge tone={STATUS_TONE[member.status]}>
          {capitalize(member.status)}
        </Badge>
      ),
    },
  ];

  /* The reader's own row, a directory-managed member, and a
   * deprovisioned one cannot be changed here; an owner is only touched
   * by another owner. The entries stay, faded. */
  const memberActions = (member: Member): MenuAction[] => {
    const locked =
      member.id === selfMembershipId ||
      member.directory_managed ||
      member.status === "deprovisioned" ||
      (member.assignments.some((assignment) => assignment.is_owner) &&
        !isOwner);
    const audit = auditLinkFor({ actor: member.id });
    return [
      ...(audit
        ? [
            {
              id: "audit",
              label: audit.label,
              disabled: !audit.allowed,
              action: () => void navigate(extensionLink(audit)),
            },
          ]
        : []),
      {
        id: "permissions",
        label: "Edit permissions",
        disabled: !canAssign || locked,
        action: () =>
          void navigate({
            to: "/settings/permissions/$kind/$principalId",
            params: { kind: "members", principalId: member.id },
          }),
      },
      {
        id: "suspend",
        label: member.status === "suspended" ? "Reactivate" : "Suspend",
        disabled: !canWrite || locked,
        action: () =>
          void changeStatus(
            member,
            member.status === "suspended" ? "active" : "suspended",
          ),
      },
      {
        id: "remove",
        label: "Remove",
        danger: true,
        disabled: !canWrite || locked,
        action: () => setRemoving(member),
      },
    ];
  };

  return (
    <SettingsPage
      title={TITLE}
      description={DESCRIPTION}
      action={
        canInvite ? (
          <Button variant="primary" onClick={() => setInviteOpen(true)}>
            Invite member
          </Button>
        ) : undefined
      }
    >
      {error ? (
        <Callout tone="warn" title="Unable to load members">
          {error}
        </Callout>
      ) : null}

      <SettingsSection
        title="Members"
        description="Everyone in this organization, with the teams they belong to and their permissions."
        inset="none"
        toolbar={
          <>
            <div className="min-w-0 flex-1">
              <TextField
                aria-label="Search members by name, email, permission, or team"
                placeholder="Search members by name, email, permission, or team"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
              />
            </div>
            <ToggleGroup
              aria-label="Which members to show"
              value={roster}
              onValueChange={(value) =>
                setRoster(value as "provisioned" | "all")
              }
            >
              <ToggleGroupItem value="provisioned">Provisioned</ToggleGroupItem>
              <ToggleGroupItem value="all">
                All{deprovisionedCount > 0 ? ` (${members.length})` : ""}
              </ToggleGroupItem>
            </ToggleGroup>
          </>
        }
      >
        {shown.length === 0 ? (
          <EmptyState
            title="No members match"
            description="Nobody matches that search. Clear it or try another name, permission, or team."
          />
        ) : (
          <div className="overflow-x-auto">
            <Table
              aria-label="Organization members"
              items={shown}
              getId={(member) => member.id}
              selected={new Set()}
              onSelectedChange={() => undefined}
              selectable={false}
              columns={columns}
              rowLabel={(member) => member.display_name || member.email}
              rowActions={memberActions}
              className="min-w-[600px]"
            />
          </div>
        )}
      </SettingsSection>

      {pending.length === 0 ? (
        <SettingsSection
          title="Pending invitations"
          description="Invitations that have not been accepted yet."
          inset="none"
        >
          <EmptyState
            icon={<PersonIcon width={18} height={18} />}
            title="No pending invitations"
            description={
              canInvite
                ? "Invite someone to add them to this organization."
                : undefined
            }
          />
        </SettingsSection>
      ) : (
        <SettingsSection
          title="Pending invitations"
          description="Invitations that have not been accepted yet. Getting a link again replaces the previous one."
        >
          {pending.map((invitation) => (
            <SettingRow
              key={invitation.id}
              label={invitation.email}
              description={
                <>
                  Invited {formatRelative(invitation.created_at)}, expires{" "}
                  {formatUntil(invitation.expires_at)}
                </>
              }
              panel={
                inviteURLs[invitation.id] ? (
                  <CodeBlock
                    label="invite link (getting it again replaces it)"
                    code={inviteURLs[invitation.id]}
                  />
                ) : undefined
              }
            >
              {invitation.grants.map((grant) => (
                <Badge
                  key={`${grant.permissions.join(":")}:${grant.scope_kind}:${grant.scope_id ?? ""}`}
                  icon={<PersonIcon width={14} height={14} />}
                >
                  {grant.permissions.length} permissions
                </Badge>
              ))}
              {canInvite ? (
                <>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() =>
                      void act(async () => {
                        const result = await api.resendInvitation(
                          invitation.id,
                        );
                        if (result.invite_url) {
                          setInviteURLs((urls) => ({
                            ...urls,
                            [invitation.id]: result.invite_url ?? "",
                          }));
                        } else {
                          toast.add({
                            type: "approve",
                            title: `Invitation resent to ${invitation.email}`,
                          });
                        }
                      }, "Could not resend the invitation")
                    }
                  >
                    Resend
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() =>
                      void act(
                        () => api.revokeInvitation(invitation.id),
                        "Revoke failed",
                      )
                    }
                  >
                    Revoke
                  </Button>
                </>
              ) : null}
            </SettingRow>
          ))}
        </SettingsSection>
      )}

      <InviteDialog
        open={inviteOpen}
        onOpenChange={setInviteOpen}
        options={page.permissionOptions}
        onRefresh={() => router.invalidate()}
      />
      {removing ? (
        <Dialog open onOpenChange={(open) => !open && setRemoving(null)}>
          <DialogContent>
            <DialogTitle>
              Remove {removing.display_name || removing.email}?
            </DialogTitle>
            <DialogDescription>
              Their access ends immediately and their sessions are revoked. The
              membership record is retained for the audit trail.
            </DialogDescription>
            <DialogFooter>
              <Button variant="ghost" onClick={() => setRemoving(null)}>
                Cancel
              </Button>
              <Button
                variant="danger"
                onClick={() =>
                  void changeStatus(removing, "deprovisioned").then(
                    (result) => result !== "failed" && setRemoving(null),
                  )
                }
              >
                Remove member
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      ) : null}
      {blocked ? (
        <BlockedStatusDialog
          change={blocked}
          onClose={() => setBlocked(null)}
          onForce={async () => {
            const result = await changeStatus(
              blocked.member,
              blocked.status,
              true,
            );
            if (result === "done") setBlocked(null);
          }}
        />
      ) : null}
    </SettingsPage>
  );
}

/* A suspension or removal refused because these active escalation path
 * revisions and team escalation policies name the member as a target. */
interface BlockedStatusChange {
  member: Member;
  status: "suspended" | "deprovisioned";
  paths: TargetedEscalationPath[];
  policies: TargetedTeamEscalationPolicy[];
}

function BlockedStatusDialog({
  change,
  onClose,
  onForce,
}: {
  change: BlockedStatusChange;
  onClose: () => void;
  onForce: () => Promise<void>;
}) {
  const [working, setWorking] = useState(false);
  const name = change.member.display_name || change.member.email;
  const verb = change.status === "deprovisioned" ? "Removing" : "Suspending";
  const count = change.paths.length + change.policies.length;
  const what =
    change.paths.length > 0 && change.policies.length > 0
      ? "escalation paths and team policies"
      : change.policies.length > 0
        ? "team escalation policies"
        : "escalation paths";
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogTitle>
          {name} is named in {what}
        </DialogTitle>
        <DialogDescription>
          {count === 1
            ? `An active ${change.policies.length > 0 ? "team escalation policy" : "escalation path"} names ${name} as a reviewer. ${verb} them leaves that step with nobody to notify until it is edited.`
            : `${count} active ${what} name ${name} as a reviewer. ${verb} them leaves those steps with nobody to notify until they are edited.`}
        </DialogDescription>
        <DialogBody>
          <TargetedPathList paths={change.paths} policies={change.policies} />
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" disabled={working} onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="danger"
            loading={working}
            onClick={() => {
              setWorking(true);
              void onForce().finally(() => setWorking(false));
            }}
          >
            {change.status === "deprovisioned"
              ? "Remove anyway"
              : "Suspend anyway"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/* The active escalation path revisions and team escalation policies
 * that name a member, one row each, linking to their editors. */
function TargetedPathList({
  paths,
  policies,
}: {
  paths: TargetedEscalationPath[];
  policies: TargetedTeamEscalationPolicy[];
}) {
  return (
    <ul className="flex flex-col gap-1">
      {paths.map((path) => (
        <li
          key={path.path_key}
          className="flex items-center gap-2 text-body text-ink-secondary"
        >
          <ReferenceChip
            reference={{
              kind: "escalation_path",
              key: path.path_key,
              name: path.name,
            }}
            link={{
              to: "/escalation-paths/$pathKey",
              params: { pathKey: path.path_key },
            }}
          />
          <span>revision {path.revision}</span>
        </li>
      ))}
      {policies.map((policy) => (
        <li
          key={policy.team_id}
          className="flex items-center gap-2 text-body text-ink-secondary"
        >
          <ReferenceChip
            reference={{
              kind: "team_escalation_policy",
              teamId: policy.team_id,
              teamName: policy.team_name,
            }}
            link={{
              to: "/settings/teams/$teamId/escalation-policy",
              params: { teamId: policy.team_id },
            }}
          />
          <span>revision {policy.revision}</span>
        </li>
      ))}
    </ul>
  );
}

const TITLE = "Members";
const DESCRIPTION = "Manage your organization's people.";

function InviteDialog({
  open,
  onOpenChange,
  options,
  onRefresh,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  options: PermissionPoliciesPage | null;
  onRefresh: () => Promise<void>;
}) {
  const toast = useToast();
  const [quotaBlocked, setQuotaBlocked] = useState(false);
  const [email, setEmail] = useState("");
  const [policy, setPolicy] = useState<PermissionPolicyDraft>(emptyPolicy);
  const [submitting, setSubmitting] = useState(false);
  const [inviteURL, setInviteURL] = useState("");
  const [failure, setFailure] = useState("");
  function close(next: boolean) {
    onOpenChange(next);
    if (!next) {
      setPolicy(emptyPolicy());
      setEmail("");
      setInviteURL("");
      setFailure("");
      setQuotaBlocked(false);
    }
  }
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!policyValid(policy)) return;
    setSubmitting(true);
    setFailure("");
    setQuotaBlocked(false);
    try {
      const result = await api.createInvitation(email, [policy]);
      await onRefresh();
      if (result.invite_url) {
        setInviteURL(result.invite_url);
        toast.add({ type: "approve", title: "Invitation created" });
      } else {
        toast.add({ type: "approve", title: `Invitation emailed to ${email}` });
        close(false);
      }
    } catch (reason) {
      setFailure(
        reason instanceof Error
          ? reason.message
          : "Unable to create invitation",
      );
      setQuotaBlocked(
        reason instanceof RequestError && reason.code === "quota_exceeded",
      );
    } finally {
      setSubmitting(false);
    }
  }
  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="w-[min(560px,calc(100vw-32px))]">
        {inviteURL ? (
          <>
            <DialogTitle>Invitation created</DialogTitle>
            <DialogDescription>Send this link to {email}.</DialogDescription>
            <DialogBody>
              <CodeBlock label="invite link" code={inviteURL} />
            </DialogBody>
            <DialogFooter>
              <Button variant="primary" onClick={() => close(false)}>
                Done
              </Button>
            </DialogFooter>
          </>
        ) : (
          <form onSubmit={submit} className="flex min-h-0 flex-col">
            <DialogTitle>Invite a member</DialogTitle>
            <DialogDescription>
              Choose the permissions they receive when they accept.
            </DialogDescription>
            <DialogBody className="flex flex-col gap-4">
              <TextField
                label="Email"
                type="email"
                required
                value={email}
                onChange={(e) => setEmail(e.target.value)}
              />
              {failure ? (
                <Callout tone="warn">
                  {failure}
                  {quotaBlocked && extensions.invitationQuotaHint ? (
                    <extensions.invitationQuotaHint />
                  ) : null}
                </Callout>
              ) : null}
              {options ? (
                <PermissionPolicyFields
                  value={policy}
                  onChange={setPolicy}
                  permissions={options.permissions}
                  resources={options.resources}
                  disabled={submitting}
                />
              ) : (
                <Callout tone="warn">
                  Permission options could not be loaded. Reload Members to try
                  again.
                </Callout>
              )}
            </DialogBody>
            <DialogFooter>
              <Button
                type="button"
                variant="ghost"
                onClick={() => close(false)}
              >
                Cancel
              </Button>
              <Button
                type="submit"
                variant="primary"
                loading={submitting}
                disabled={!options || !policyValid(policy)}
              >
                Create invitation
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
