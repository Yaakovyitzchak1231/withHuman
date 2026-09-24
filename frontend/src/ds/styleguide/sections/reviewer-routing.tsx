import { useState } from "react";
import {
  PathConditionEditor,
  PathTree,
  type PathCondition,
  type TeamPath,
} from "@/ds/components/escalation-tree";
import {
  PipelineConditionExits,
  PipelineConnector,
  PipelineEntryCard,
  PipelineTerminusCard,
  PolicyBlockCard,
  PolicyBlockSummaryCard,
  type PolicyBlockDraft,
} from "@/ds/components/policy-block";
import {
  TargetBadge,
  TargetSelect,
  type ReviewerTarget,
} from "@/ds/components/reviewer-target";
import { Section, Demo, DemoRow } from "../section";

/* The organization's teams first, then its people. */
const PAYMENTS: ReviewerTarget = { type: "team", id: "payments", name: "Payments approvers" };
const FINANCE: ReviewerTarget = { type: "team", id: "finance", name: "Finance leads" };
const BILLING_ONCALL: ReviewerTarget = { type: "team", id: "billing-oncall", name: "Billing on-call" };
const PRIYA: ReviewerTarget = { type: "user", id: "priya", name: "Priya Natarajan" };
const MARCUS: ReviewerTarget = { type: "user", id: "marcus", name: "Marcus Bell" };
const ADA: ReviewerTarget = { type: "user", id: "ada", name: "Ada Okafor" };
/* Inside a team's escalation policy a level may name everyone on the
 * team; the chip carries the team's id so it sits beside the members. */
const EVERYONE_IN_FINANCE: ReviewerTarget = {
  type: "broadcast",
  id: FINANCE.id,
  name: "Everyone on Finance leads",
};
const DIRECTORY: ReviewerTarget[] = [PAYMENTS, FINANCE, BILLING_ONCALL, PRIYA, MARCUS, ADA];

const SUGGESTIONS = [
  "/request/tool",
  "/request/arguments/amount_cents",
  "/request/arguments/customer/labels",
  "/agent/slug",
];

const LARGE_REFUNDS: PolicyBlockDraft = {
  id: "large-refunds",
  name: "Large refunds need payments",
  enabled: true,
  outcome: "human",
  target: PAYMENTS,
  reason: "Refunds over 100 USD are reviewed by the payments approvers.",
  when: {
    id: "rr-g1",
    all: [
      { id: "rr-c1", field: "/request/tool", operator: "starts_with", value: ["stripe."] },
      { id: "rr-c2", field: "/request/arguments/amount_cents", operator: "greater_than_or_equal", value: 10000 },
    ],
  },
};

const UNKNOWN_TOOLS: PolicyBlockDraft = {
  id: "unknown-tools",
  name: "Unknown tools ask a human",
  enabled: true,
  outcome: "human",
  target: null,
  reason: "",
  when: {
    id: "rr-g2",
    all: [
      { id: "rr-c3", field: "/request/tool", operator: "not_in", value: ["crm.update_note", "crm.tag"] },
    ],
  },
};

/* The finance leads' own escalation policy, opened inline from a path
 * level that names the team: a member first, then everyone on it. */
const FINANCE_POLICY: TeamPath = {
  teamId: FINANCE.id,
  teamName: FINANCE.name,
  nodes: [
    { id: "fin-defer", type: "defer", set: "UK office hours", standardOnly: true },
    { id: "fin-1", type: "level", targets: [MARCUS], escalateAfter: "30m" },
    { id: "fin-2", type: "level", targets: [EVERYONE_IN_FINANCE], escalateAfter: "30m", raiseUrgency: true },
  ],
};

/* An organization escalation path. Its levels name teams and people. */
const PAYMENTS_PATH: TeamPath = {
  teamId: PAYMENTS.id,
  teamName: PAYMENTS.name,
  nodes: [
    {
      id: "pay-big",
      type: "if_else",
      conditions: [
        {
          kind: "request",
          when: {
            id: "pay-big-g",
            all: [
              { id: "pay-big-c", field: "/request/arguments/amount_cents", operator: "greater_than_or_equal", value: 100000 },
            ],
          },
        },
      ],
      then: [{ id: "pay-lead", type: "level", targets: [PRIYA], escalateAfter: "10m", raiseUrgency: true }],
      else: [],
    },
    { id: "pay-team", type: "level", targets: [PAYMENTS], escalateAfter: "15m" },
    {
      id: "pay-hours",
      type: "if_else",
      conditions: [{ kind: "working_hours", set: "UK office hours", active: false }],
      then: [
        { id: "pay-oncall", type: "level", targets: [BILLING_ONCALL], escalateAfter: "20m", raiseUrgency: true },
      ],
      else: [{ id: "pay-finance", type: "level", targets: [FINANCE, ADA], escalateAfter: "30m" }],
    },
  ],
  repeat: { times: 2, after: "1h" },
};

/* Each team's active escalation policy. Payments approvers has none, so
 * a level naming it notifies everyone on the team at once. */
const TEAM_POLICIES = new Map<string, TeamPath>([[FINANCE.id, FINANCE_POLICY]]);

export function ReviewerRoutingSection() {
  const [picked, setPicked] = useState<ReviewerTarget | null>(PAYMENTS);
  const [large, setLarge] = useState<PolicyBlockDraft>(LARGE_REFUNDS);
  const [largeExpanded, setLargeExpanded] = useState(true);
  const [terminusTarget, setTerminusTarget] = useState<ReviewerTarget | null>(BILLING_ONCALL);
  /* The overview's terminus: saved on its own, never part of a draft. */
  const [overviewTarget, setOverviewTarget] = useState<ReviewerTarget | null>(null);
  const [overviewDraft, setOverviewDraft] = useState<ReviewerTarget | null>(null);
  const [overviewEditing, setOverviewEditing] = useState(false);
  const [overviewSaving, setOverviewSaving] = useState(false);
  const [condition, setCondition] = useState<PathCondition>({
    kind: "working_hours",
    set: "UK office hours",
    active: false,
  });

  return (
    <Section
      id="reviewer-routing"
      title="Reviewer routing"
      lede="Who gets asked, decided where the pipeline decides that a human is needed. There is no separate routing rule list: an Ask a human exit names an escalation path, the pipeline's default lives on its terminus, the path's levels name people and teams, and a team's own escalation policy names its members or everyone on it. The chips and picker below serve every target kind."
    >
      <Demo label="TargetBadge" hint="a person, a team, everyone on a team, or the fallback">
        <DemoRow>
          <TargetBadge target={PRIYA} />
          <TargetBadge target={PAYMENTS} />
          <TargetBadge target={EVERYONE_IN_FINANCE} />
          <TargetBadge target={null} />
          <TargetBadge target={null} fallbackLabel="Any authorized reviewer" />
        </DemoRow>
        <p className="mt-3 text-micro text-ink-muted">
          A person shows their avatar and a team its people icon, both as the system property
          badge. A broadcast, the megaphone, is a level inside a team escalation policy that
          notifies everyone on the team; it never appears on an organization path, where the
          team chip already means that. No target is not an empty state: it names what the
          request falls back to, in muted ink so an inherited value never reads as a chosen
          one. On a block exit the fallback is the pipeline default; on the terminus it is any
          authorized reviewer, which is the queue.
        </p>
      </Demo>

      <Demo label="TargetSelect" hint="teams first, people after">
        <div className="flex flex-col gap-3">
          <div className="flex flex-wrap items-center gap-3">
            <TargetSelect
              directory={DIRECTORY}
              value={picked}
              onChange={setPicked}
              fallbackLabel="Pipeline default"
            />
            <TargetBadge target={picked} />
          </div>
          <p className="text-micro text-ink-muted">
            One filterable list, no target-type switch: teams come first because a team is the
            durable choice and a person the exception. The fallback entry is offered only where
            inheriting makes sense; the terminus requires none.
          </p>
        </div>
      </Demo>

      <Demo label="Human exits on the rail" hint="the consequence has two halves">
        <div className="flex flex-col">
          <PipelineEntryCard agentSlug="billing" />
          <PipelineConnector />
          <PolicyBlockCard
            block={large}
            position={1}
            expanded={largeExpanded}
            onExpandedChange={setLargeExpanded}
            onChange={setLarge}
            onRemove={() => undefined}
            onDuplicate={() => undefined}
            handleProps={{}}
            suggestions={SUGGESTIONS}
            directory={DIRECTORY}
          />
          <PipelineConditionExits outcome={large.outcome} target={large.target} />
          <PolicyBlockSummaryCard block={UNKNOWN_TOOLS} position={2} />
          <PipelineConditionExits outcome="human" target={UNKNOWN_TOOLS.target} matched />
          <PipelineConnector />
          <PipelineTerminusCard
            target={terminusTarget}
            directory={DIRECTORY}
            onTargetChange={setTerminusTarget}
          />
        </div>
        <p className="mt-3 text-micro text-ink-muted">
          Ask a human stays the warm outcome chip; the reviewers follow it on the same branch,
          because a human exit's consequence is both that a human decides and which humans. The
          expanded card gains one row, Escalation path, only while its outcome is human: change
          the outcome to Approve and the row leaves. In the product the row also renders the
          chosen team's path tree beneath the picker, and a team chip on an exit opens the same
          tree in viewing mode. The second block names nobody, so its exit
          shows Pipeline default and inherits whatever the terminus says. The terminus is the
          one place the default is set; in editing mode it is a picker in the sentence, and a
          fall-through request goes to the same place.
        </p>
      </Demo>

      <Demo label="Terminus on the overview" hint="the card saves itself from its menu">
        <div className="flex flex-col">
          <PipelineConnector label="If no decision is made" />
          <PipelineTerminusCard
            target={overviewEditing ? overviewDraft : overviewTarget}
            directory={overviewEditing ? DIRECTORY : undefined}
            onTargetChange={overviewEditing ? setOverviewDraft : undefined}
            onSave={
              overviewEditing
                ? () => {
                    setOverviewSaving(true);
                    window.setTimeout(() => {
                      setOverviewTarget(overviewDraft);
                      setOverviewSaving(false);
                      setOverviewEditing(false);
                    }, 600);
                  }
                : undefined
            }
            onCancel={overviewEditing ? () => setOverviewEditing(false) : undefined}
            saving={overviewSaving}
            actions={
              overviewEditing
                ? undefined
                : [
                    {
                      id: "edit",
                      label: "Change default reviewers",
                      action: () => {
                        setOverviewDraft(overviewTarget);
                        setOverviewEditing(true);
                      },
                    },
                  ]
            }
          />
        </div>
        <p className="mt-3 text-micro text-ink-muted">
          On the pipelines overview the terminus is the organization default without the
          organization pipeline around it, so it carries the block cards' menu. Change default
          reviewers swaps the chip for the picker with Cancel and Save beside it; Save creates and
          activates an organization revision with the same blocks, and the card returns to its
          chip. In the pipeline editor the same card is part of the draft and has neither the
          menu nor the buttons.
        </p>
      </Demo>

      <Demo label="PathTree" hint="click a team chip to open its escalation policy">
        <div className="flex flex-col gap-3">
          <div className="flex items-baseline gap-2">
            <span className="text-label text-ink-secondary">Large refunds: escalation path</span>
            <span className="text-micro font-normal text-ink-faint">
              an organization path; each team's policy lives under Teams
            </span>
          </div>
          <PathTree
            nodes={PAYMENTS_PATH.nodes}
            repeat={PAYMENTS_PATH.repeat}
            teamPath={(teamId) => TEAM_POLICIES.get(teamId)}
            teamAction={(path) => (
              <span className="text-label text-ink-muted">Edit {path.teamName}</span>
            )}
          />
          <p className="text-micro text-ink-muted">
            The path is a tree read top to bottom. A level notifies its targets and starts a
            timer; a branch tests the request itself, the clock, or the urgency, and the request
            condition is the same builder the pipeline blocks use. A team target opens inline
            and its escalation policy walks while the level that named it keeps its own timer
            running, so escalation only ever widens; the header carries whatever the page
            passes as teamAction, in the product a link to edit the policy. Three rules keep
            the tree honest: a team met again higher up is notified but not walked twice, a
            team without a policy notifies everyone on it at once (open Payments approvers or
            Billing on-call), and the deadline caps every timer. PathLevelTargets is the level
            row on its own, which the path editor reuses under a level's picker.
          </p>
        </div>
      </Demo>

      <Demo label="PathConditionEditor" hint="request, working hours, or urgency">
        <div className="flex flex-col gap-3">
          <PathConditionEditor
            value={condition}
            onChange={setCondition}
            workingHours={[
              { id: "UK office hours", name: "UK office hours" },
              { id: "US office hours", name: "US office hours" },
            ]}
            suggestions={SUGGESTIONS}
          />
          <p className="text-micro text-ink-muted">
            The kind comes first because it decides the rest of the row. The guideline that keeps
            the two condition homes from blurring: a pipeline block decides whether a human is
            needed, a path branch decides which human. Nothing stops a path from repeating a
            pipeline condition; it is simply never necessary.
          </p>
        </div>
      </Demo>
    </Section>
  );
}
