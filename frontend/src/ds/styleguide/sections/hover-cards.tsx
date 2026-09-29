import type { ReactNode } from "react";
import { Avatar } from "@/ds/components/avatar";
import { Badge } from "@/ds/components/badge";
import { CodeBlock } from "@/ds/components/code-block";
import { DocsLink } from "@/ds/components/docs-link";
import { PathTree } from "@/ds/components/escalation-tree";
import {
  HoverCard,
  HoverCardBody,
  HoverCardFact,
  HoverCardFacts,
  HoverCardMeta,
  HoverCardTitle,
} from "@/ds/components/hover-card";
import { List, ListRowContent } from "@/ds/components/list";
import { ReferenceCardsProvider, Referenced, type Reference } from "@/ds/components/reference";
import { TargetBadge } from "@/ds/components/reviewer-target";
import { Timeline, TimelineItem } from "@/ds/components/timeline";
import { AlertIcon, ArrowUpRightIcon, CheckIcon, InboxIcon, ShieldIcon } from "@/ds/icons";
import { Section, Demo, DemoRow, KeyboardHint } from "../section";

const NONE: ReadonlySet<string> = new Set();

const LINEAR_TOOLS = [
  { name: "create_issue", description: "Create an issue in a team" },
  { name: "update_issue", description: "Change an issue's fields" },
  { name: "list_issues", description: "List issues matching a filter" },
  { name: "add_comment", description: "Comment on an issue" },
  { name: "list_teams", description: "List the workspace's teams" },
  { name: "list_projects", description: "List projects" },
  { name: "create_project", description: "Create a project" },
  { name: "archive_issue", description: "Archive an issue" },
  { name: "assign_issue", description: "Assign an issue to a person" },
  { name: "list_cycles", description: "List a team's cycles" },
];

/* What the app's provider does for real, done here with fixed facts:
 * one card per kind of reference, in the shared anatomy (a mark, the
 * name, an aside, a muted meta line, then facts or a note). */
function demoCard(reference: Reference): ReactNode | null {
  switch (reference.kind) {
    case "person":
      return (
        <>
          <HoverCardTitle>
            <span className="flex min-w-0 items-center gap-2.5">
              <Avatar name={reference.name} size="lg" tooltip={false} />
              <span className="min-w-0">
                <span className="flex items-center gap-1">
                  <span className="truncate underline-offset-[3px] hover:underline">{reference.name}</span>
                  <ArrowUpRightIcon width={12} height={12} className="text-ink-faint" />
                </span>
                <HoverCardMeta className="truncate">ana@acme.example</HoverCardMeta>
              </span>
            </span>
            <span className="shrink-0 self-start font-mono text-[10px] tracking-wide text-ink-faint uppercase">Member</span>
          </HoverCardTitle>
          <HoverCardBody>
            <HoverCardFacts>
              <HoverCardFact term="Teams">Payments, Platform</HoverCardFact>
              <HoverCardFact term="Permissions">Manage Payments, review requests</HoverCardFact>
              <HoverCardFact term="Joined">3mo ago</HoverCardFact>
            </HoverCardFacts>
          </HoverCardBody>
        </>
      );
    case "team":
      return (
        <>
          <HoverCardTitle>
            <span className="flex min-w-0 items-center gap-2.5">
              <span className="min-w-0">
                <span className="flex items-center gap-1">
                  <span className="truncate underline-offset-[3px] hover:underline">{reference.name}</span>
                  <ArrowUpRightIcon width={12} height={12} className="text-ink-faint" />
                </span>
                <HoverCardMeta>6 members</HoverCardMeta>
              </span>
            </span>
            <span className="shrink-0 self-start font-mono text-[10px] tracking-wide text-ink-faint uppercase">Team</span>
          </HoverCardTitle>
          <HoverCardBody>
            <HoverCardFacts>
              <HoverCardFact term="Escalation policy">Revision 3 active</HoverCardFact>
              <HoverCardFact term="Permissions">Review requests</HoverCardFact>
            </HoverCardFacts>
          </HoverCardBody>
        </>
      );
    case "escalation_path":
      return (
        <>
          <HoverCardTitle>
            <span className="flex min-w-0 items-center gap-1">
              <span className="truncate underline-offset-[3px] hover:underline">{reference.name}</span>
              <ArrowUpRightIcon width={12} height={12} className="text-ink-faint" />
            </span>
            <span className="shrink-0 self-start font-mono text-[10px] tracking-wide text-ink-faint uppercase">Escalation path</span>
          </HoverCardTitle>
          <HoverCardBody>
            <PathTree
              depth={1}
              nodes={[
                {
                  id: "l1",
                  type: "level",
                  targets: [
                    { type: "user", id: "m-ana", name: "Ana Suarez" },
                    { type: "team", id: "t-payments", name: "Payments" },
                  ],
                  escalateAfter: "15m",
                  raiseUrgency: true,
                },
                {
                  id: "l2",
                  type: "level",
                  targets: [{ type: "team", id: "t-platform", name: "Platform" }],
                  escalateAfter: "30m",
                  raiseUrgency: false,
                },
              ]}
              repeat={{ times: 2, after: "1h" }}
            />
          </HoverCardBody>
        </>
      );
    case "tool":
      return (
        <>
          <HoverCardTitle>
            <span className="flex min-w-0 items-center gap-2.5">
              <span className="min-w-0">
                <span className="block truncate font-mono text-[12px]">{reference.name}</span>
                <HoverCardMeta className="truncate">Mcp github create issue</HoverCardMeta>
              </span>
            </span>
            <span className="shrink-0 self-start font-mono text-[10px] tracking-wide text-ink-faint uppercase">Tool</span>
          </HoverCardTitle>
          <HoverCardBody>
            <HoverCardFacts>
              <HoverCardFact term="Server" mono>
                github
              </HoverCardFact>
              <HoverCardFact term="Tool" mono>
                create_issue
              </HoverCardFact>
              <HoverCardFact term="Hook pattern" mono>
                create_*
              </HoverCardFact>
            </HoverCardFacts>
          </HoverCardBody>
        </>
      );
        case "agent":
      return (
        <>
          <HoverCardTitle>
            <span className="flex min-w-0 items-center gap-2.5">
              <Avatar name={reference.name} kind="agent" tooltip={false} />
              <span className="min-w-0">
                <span className="flex min-w-0 items-center gap-1">
                  <span className="truncate underline-offset-[3px] hover:underline">{reference.name}</span>
                  <ArrowUpRightIcon width={12} height={12} className="text-ink-faint" />
                </span>
                <HoverCardMeta className="font-mono">deploy-bot</HoverCardMeta>
              </span>
            </span>
            <span className="shrink-0 self-start font-mono text-[10px] tracking-wide text-ink-faint uppercase">
              Agent
            </span>
          </HoverCardTitle>
          <HoverCardBody>
            <HoverCardFacts>
              <HoverCardFact term="Instances">4</HoverCardFact>
              <HoverCardFact term="Last request">1h ago</HoverCardFact>
              <HoverCardFact term="Weekly requests">400</HoverCardFact>
            </HoverCardFacts>
          </HoverCardBody>
        </>
      );
    case "request":
      return (
        <>
          <HoverCardTitle className="items-center">
            <span className="min-w-0">
              <span className="flex min-w-0 items-center gap-1">
                <span className="min-w-0 font-mono text-[12px] break-all underline-offset-[3px] hover:underline">
                  create_issue
                </span>
                <ArrowUpRightIcon width={12} height={12} className="shrink-0 text-ink-faint" />
              </span>
            </span>
            <span className="shrink-0 self-start font-mono text-[10px] tracking-wide text-ink-faint uppercase">
              Request
            </span>
          </HoverCardTitle>
          <HoverCardBody>
            <blockquote className="mb-2.5 flex flex-col gap-1 border-l-2 border-edge-strong pl-3">
              <p className="text-micro text-ink-faint">Agent's explanation</p>
              <p className="line-clamp-3 text-label text-ink-secondary">
                The payments suite has failed three runs in a row on main with the same timeout, so I am
                filing it for the on-call to pick up.
              </p>
            </blockquote>
            <HoverCardFacts>
              <HoverCardFact term="Status">
                <span className="-my-1 flex">
                  <Badge tone="warn">Pending</Badge>
                </span>
              </HoverCardFact>
              <HoverCardFact term="Agent">
                <span className="-my-1 -mr-2 flex">
                  <Referenced reference={{ kind: "agent", slug: "deploy-bot", name: "deploy bot" }}>
                    <Badge className="pl-0.5" icon={<Avatar name="deploy bot" kind="agent" size="sm" tooltip={false} />}>
                      deploy bot
                    </Badge>
                  </Referenced>
                </span>
                <span>on ci-runner-4</span>
              </HoverCardFact>
              <HoverCardFact term="Created">12m ago</HoverCardFact>
              <HoverCardFact term="Sent to">
                <span className="-my-1 -mr-2 flex">
                  <TargetBadge target={{ type: "escalation_path", id: "payments-oncall", name: "Payments on-call" }} />
                </span>
              </HoverCardFact>
              <HoverCardFact term="Claimed by">
                <span className="-my-1 -mr-2 flex">
                  <Referenced reference={{ kind: "person", id: "m-ana", name: "Ana Suarez" }}>
                    <Badge className="pl-0.5" icon={<Avatar name="Ana Suarez" size="sm" tooltip={false} />}>
                      Ana Suarez
                    </Badge>
                  </Referenced>
                </span>
              </HoverCardFact>
              <HoverCardFact term="Deadline">in 48m</HoverCardFact>
            </HoverCardFacts>
          </HoverCardBody>
        </>
      );
    case "pipeline_block":
      return (
        <>
          <HoverCardTitle>
            <span className="min-w-0">
              <span className="flex min-w-0 items-center gap-1">
                <span className="truncate underline-offset-[3px] hover:underline">Entry pipeline</span>
                <ArrowUpRightIcon width={12} height={12} className="text-ink-faint" />
              </span>
              <HoverCardMeta>Revision {reference.pipeline?.revision ?? 1}, as it ran</HoverCardMeta>
            </span>
            <span className="shrink-0 self-start font-mono text-[10px] tracking-wide text-ink-faint uppercase">
              Approval pipeline
            </span>
          </HoverCardTitle>
          <HoverCardBody className="pt-4">
            <Timeline>
              <TimelineItem tone="neutral" marker={<span className="font-mono text-[11px]">1</span>}>
                <div className="flex flex-col gap-1">
                  <div className="flex items-baseline gap-3">
                    <span className="min-w-0 flex-1 truncate text-body text-ink-muted">Staging never waits</span>
                    <span className="shrink-0 text-label font-normal text-ink-faint">Branch</span>
                  </div>
                  <p className="truncate font-mono text-[11px] text-ink-faint">Branch · environment equals staging</p>
                </div>
                {/* A branch whose Outcome block decides everything ends at
                  * that block: nothing reaches its end, so none is drawn. */}
                <Timeline className="mt-3">
                  <TimelineItem tone="neutral" marker={<span className="font-mono text-[11px]">1</span>}>
                    <div className="flex flex-col gap-1">
                      <div className="flex items-baseline gap-3">
                        <span className="min-w-0 flex-1 truncate text-body text-ink-muted">Approve staging</span>
                        <span className="shrink-0 text-label font-normal text-ink-faint">Outcome</span>
                      </div>
                      <p className="truncate font-mono text-[11px] text-ink-faint">Outcome · Every request that reaches it</p>
                    </div>
                  </TimelineItem>
                </Timeline>
              </TimelineItem>
              <TimelineItem tone="neutral" marker={<span className="font-mono text-[11px]">2</span>}>
                <div className="flex flex-col gap-1">
                  <div className="flex items-baseline gap-3">
                    <span className="min-w-0 flex-1 truncate text-body text-ink-muted">Refunds</span>
                    <span className="shrink-0 text-label font-normal text-ink-faint">Branch</span>
                  </div>
                  <p className="truncate font-mono text-[11px] text-ink-faint">Branch · Tool name equals stripe.refund</p>
                </div>
                {/* A branch's blocks nest as a timeline of their own, ending
                  * where the branch sends a request nothing decided. */}
                <Timeline className="mt-3">
                  <TimelineItem tone="neutral" marker={<span className="font-mono text-[11px]">1</span>}>
                    <div className="flex flex-col gap-1">
                      <div className="flex items-baseline gap-3">
                        <span className="min-w-0 flex-1 truncate text-body text-ink-muted">Fraud service review</span>
                        <span className="shrink-0 text-label font-normal text-ink-faint">Webhook</span>
                      </div>
                      <p className="truncate font-mono text-[11px] text-ink-faint">Webhook · fraud-service</p>
                    </div>
                  </TimelineItem>
                  <TimelineItem>
                    <p className="flex flex-wrap items-center gap-y-1 text-body text-ink-muted">
                      <span className="mr-1">No decision: ask a human via</span>
                      <TargetBadge className="-my-1" target={{ type: "escalation_path", id: "refund-reviewers", name: "Refund reviewers" }} />
                    </p>
                  </TimelineItem>
                </Timeline>
              </TimelineItem>
              <TimelineItem tone="neutral" marker={<span className="font-mono text-[11px]">3</span>}>
                <div className="flex flex-col gap-1">
                  <div className="flex items-baseline gap-3">
                    <span className="min-w-0 flex-1 truncate text-body text-ink-muted">Selected tools</span>
                    <span className="shrink-0 text-label font-normal text-ink-faint">Branch</span>
                  </div>
                  <p className="truncate font-mono text-[11px] text-ink-faint">Branch · Tool name is one of Bash, Write</p>
                </div>
                <Timeline className="mt-3">
                  <TimelineItem tone="accent" marker={<span className="font-mono text-[11px]">1</span>}>
                    <div className="-mx-2 -my-1.5 flex flex-col gap-1 rounded-md border-hairline border-edge-strong bg-fill-hover px-2 py-1.5">
                      <div className="flex items-baseline gap-3">
                        <span className="min-w-0 flex-1 truncate text-body text-ink">{reference.name}</span>
                        <span className="shrink-0 text-label font-normal text-ink-faint">Outcome</span>
                      </div>
                      <p className="truncate font-mono text-[11px] text-ink-muted">Outcome · Every request that reaches it</p>
                      <p className="flex flex-wrap items-center gap-y-1 text-body text-ink-secondary">
                        <Badge className="-my-1" tone="warn">
                          Ask a human
                        </Badge>
                      </p>
                      <p className="pt-1 text-body text-ink">{reference.reason}</p>
                    </div>
                  </TimelineItem>
                </Timeline>
              </TimelineItem>
              <TimelineItem>
                <p className="text-body text-ink-secondary">No match: any authorized reviewer</p>
              </TimelineItem>
            </Timeline>
          </HoverCardBody>
        </>
      );
    default:
      return null;
  }
}

export function HoverCardsSection() {
  return (
    <Section
      id="hover-cards"
      title="Hover card"
      lede="A card that opens on hover or focus for a thing with more to say than a tooltip's one line. Raised overlay surface, a card's inset, read only. A Tooltip names a control; a HoverCard explains a thing."
    >
      <Demo
        label="References"
        hint="one card per kind of thing, wherever it is mentioned"
      >
        <ReferenceCardsProvider render={demoCard}>
          <DemoRow className="gap-x-4 gap-y-3">
            <Referenced reference={{ kind: "person", id: "m-ana", name: "Ana Suarez" }}>
              <Badge className="pl-0.5" icon={<Avatar name="Ana Suarez" size="sm" tooltip={false} />}>
                Ana Suarez
              </Badge>
            </Referenced>
            <TargetBadge target={{ type: "team", id: "t-payments", name: "Payments" }} />
            <TargetBadge target={{ type: "escalation_path", id: "payments-oncall", name: "Payments on-call" }} />
            <span className="text-body text-ink-secondary">
              The agent wants to call{" "}
              <Referenced
                reference={{ kind: "tool", name: "create_issue", server: "github" }}
                inline
              >
                <span className="font-mono text-[12px] text-ink underline decoration-edge-strong underline-offset-[3px]">
                  create_issue
                </span>
              </Referenced>
            </span>
            <Avatar name="deploy bot" id="deploy-bot" kind="agent" />
            <Avatar name="Marcus Chen" id="m-marcus" />
            <Referenced reference={{ kind: "request", id: "9f3c2a1e-7b40-4d2a-9c1e-5a6b7c8d9e0f" }}>
              <Badge className="gap-1" icon={<InboxIcon width={13} height={13} />}>
                <span className="font-mono text-[12px]">9f3c2a1e</span>
              </Badge>
            </Referenced>
            <Referenced
              reference={{
                kind: "pipeline_block",
                key: "codex-ask-about-selected-tools",
                name: "Selected tools go to a human",
                type: "outcome",
                outcome: "human",
                reason: "Selected during onboarding",
                pipeline: { pipelineKey: "entry", revision: 2 },
              }}
            >
              <Badge className="gap-1" icon={<ShieldIcon width={13} height={13} />}>
                Selected tools go to a human
              </Badge>
            </Referenced>
          </DemoRow>
        </ReferenceCardsProvider>
        <p className="mt-3 max-w-[560px] text-label font-normal text-ink-muted">
          Referenced wraps a mention and asks the app's provider for its card; Avatar, TargetBadge and
          the webhook block do so on their own. With no provider, or when the reader may not see the
          thing, the mention renders exactly as given. Cards load their facts when they open, and say
          so with HoverCardNote until they arrive.
        </p>
      </Demo>

      <Demo label="Documentation" hint="hover or focus to preview, click to open in a new tab">
        <DocsLink
          href="https://withhuman.ai/docs/web-app/review-queue"
          title="Review queue"
          description="Review and decide approval requests that need a human."
        />
        <KeyboardHint>The link takes one tab stop; Esc dismisses the preview.</KeyboardHint>
      </Demo>

      <Demo label="On badges" hint="hover or focus to open">
        <DemoRow className="gap-1.5">
          <HoverCard
            content={
              <>
                <HoverCardTitle>
                  <span className="flex items-center gap-1.5">
                    <span className="font-mono text-[12px]">linear</span>
                    <Badge fill icon={false} className="font-mono">
                      http
                    </Badge>
                  </span>
                  <Badge tone="neutral">14 tools</Badge>
                </HoverCardTitle>
                <HoverCardBody>
                  <div className="-mx-2 max-h-[240px] overflow-y-auto">
                    <List
                      items={LINEAR_TOOLS}
                      getId={(tool) => tool.name}
                      selected={NONE}
                      onSelectedChange={() => undefined}
                      selectable={false}
                      openButton={false}
                      aria-label="linear tools"
                    >
                      {(tool) => (
                        <ListRowContent
                          title={<span className="font-mono text-[12px]">{tool.name}</span>}
                          description={tool.description}
                        />
                      )}
                    </List>
                  </div>
                </HoverCardBody>
              </>
            }
          >
            <Badge tone="approve" icon={<CheckIcon width={14} height={14} />} className="font-mono">
              linear
            </Badge>
          </HoverCard>
          <HoverCard
            content={
              <>
                <HoverCardTitle>
                  <span className="flex items-center gap-1.5">
                    <span className="font-mono text-[12px]">computer-use</span>
                    <Badge fill icon={false} className="font-mono">
                      stdio
                    </Badge>
                  </span>
                  <Badge tone="deny" icon={<AlertIcon width={14} height={14} />}>
                    Not listed
                  </Badge>
                </HoverCardTitle>
                <HoverCardBody>
                  <CodeBlock
                    label="error"
                    code="start ./Codex Computer Use.app/Contents/MacOS/SkyComputerUseClient: fork/exec: no such file or directory"
                    wrap
                  />
                </HoverCardBody>
              </>
            }
          >
            <Badge
              tone="deny"
              icon={<AlertIcon width={14} height={14} />}
              className="font-mono"
            >
              computer-use
            </Badge>
          </HoverCard>
        </DemoRow>
        <KeyboardHint>Focusing a trigger with Tab opens its card; Esc dismisses.</KeyboardHint>
      </Demo>
    </Section>
  );
}
