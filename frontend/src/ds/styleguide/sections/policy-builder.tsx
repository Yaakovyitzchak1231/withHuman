import type { ToolSchema } from "@/ds/lib/tool-schema";
import { useState } from "react";
import {
  ConditionBuilder,
  type ConditionNode,
} from "@/ds/components/condition-builder";
import { OutcomeSelect, type BlockOutcome } from "@/ds/components/outcome-select";
import { Badge } from "@/ds/components/badge";
import { Button } from "@/ds/components/button";
import {
  AlwaysBlockCard,
  AlwaysBlockSummaryCard,
  type AlwaysBlockDraft,
  PipelineAddSlot,
  PipelineBranchEndCard,
  PipelineBranchWell,
  PipelineConditionExits,
  PipelineConnector,
  PipelineEntryCard,
  PipelineExits,
  PipelineFoldedSection,
  PipelineTerminusCard,
  PolicyBlockCard,
  PolicyBlockSummaryCard,
  type PolicyBlockDraft,
} from "@/ds/components/policy-block";
import {
  BranchBlockCard,
  BranchBlockSummaryCard,
  type BranchBlockDraft,
} from "@/ds/components/branch-block";
import type { ReviewerTarget } from "@/ds/components/reviewer-target";
import { ToggleGroup, ToggleGroupItem } from "@/ds/components/toggle-group";
import { cn } from "@/ds/lib/cn";
import { UnknownBlockCard, UnknownBlockSummaryCard, type UnknownBlockDraftFields } from "@/ds/components/unknown-block";
import {
  WebhookBlockCard,
  WebhookBlockSummaryCard,
  webhookExits,
  type WebhookBlockDraft,
  type WebhookEndpointOption,
} from "@/ds/components/webhook-block";
import { BranchIcon, ChevronDownIcon, CornerDownRightIcon, LockIcon, SettingsIcon } from "@/ds/icons";
import { Section, Demo, KeyboardHint } from "../section";

/* Field paths a real editor learns from a sample request. */
const SUGGESTIONS = [
  "/request/tool",
  "/agent/slug",
  "/agent/name",
  "/request/arguments/amount_cents",
  "/request/arguments/customer",
  "/request/arguments/customer/email",
  "/request/arguments/customer/labels",
];

const VALUE_SUGGESTIONS = {
  "/request/tool": ["stripe.refund", "stripe.partial_refund", "stripe.charge", "github.merge_pull_request"],
  "/request/arguments/amount_cents": [900, 1000, 5000],
  "/request/arguments/customer/labels": ["verified", "priority", "new"],
  "/request/arguments/customer/email": ["buyer@example.com"],
  "/agent/slug": ["support", "release"],
  "/agent/name": ["Support assistant", "Release assistant"],
};

const DEMO_TOOLS: ToolSchema[] = ["stripe.refund", "stripe.partial_refund"].map((name) => ({
  name, source: "Payments", description: "Refund a payment",
  inputSchema: { type: "object", required: ["amount_cents"], properties: {
    amount_cents: { type: "integer", description: "Refund amount in minor units" },
    currency: { type: "string", enum: ["GBP", "USD", "EUR"] },
    notify: { type: "boolean", description: "Notify the customer" },
    customer: { type: "object", properties: { labels: { type: "array", items: { type: "string" } }, email: { type: "string" } } },
  } },
}));

const NESTED_CONDITION: ConditionNode = {
  id: "sg-g1",
  all: [
    {
      id: "sg-c1",
      field: "/request/tool",
      operator: "in",
      value: ["stripe.refund", "stripe.partial_refund"],
    },
    {
      id: "sg-c2",
      field: "/request/arguments/amount_cents",
      operator: "less_than",
      value: 1000,
    },
    {
      id: "sg-g2",
      any: [
        {
          id: "sg-c3",
          field: "/request/arguments/customer/labels",
          operator: "contains",
          value: "verified",
        },
        {
          id: "sg-c4",
          field: "/request/arguments/customer/email",
          operator: "ends_with",
          value: "@example.com",
        },
      ],
    },
  ],
};

const DEMO_BLOCK: PolicyBlockDraft = {
  id: "small-refunds",
  name: "Approve small refunds",
  enabled: true,
  outcome: "approve",
  reason: "Small refunds for verified customers are pre-authorized.",
  when: NESTED_CONDITION,
};

/* A block of a kind another edition adds, as the open edition meets it
 * in a stored revision. */
const DEMO_UNKNOWN: UnknownBlockDraftFields = {
  id: "risk-score",
  name: "Score refund risk",
  enabled: true,
  type: "risk_score",
  reason: "Refund scored against the billing risk model.",
  target: null,
};

const DEMO_ENDPOINTS: WebhookEndpointOption[] = [
  { key: "fraud-service", name: "Fraud service", url: "https://fraud.example.com/hooks/approval", active: true },
  { key: "budget-check", name: "Budget check", url: "https://finance.example.com/withhuman", active: true },
];

const DEMO_WEBHOOK: WebhookBlockDraft = {
  id: "fraud-check",
  name: "Fraud service review",
  enabled: true,
  endpointKey: DEMO_ENDPOINTS[0].key,
  allowedOutcomes: ["next", "human"],
  timeout: "5s",
  maxAttempts: 3,
  include: { agentReasoning: false, context: false, previousMetadata: true },
  reason: "Fraud service reviewed the request.",
};

const REFUND_REVIEWERS: ReviewerTarget = {
  type: "escalation_path",
  id: "refund-reviewers",
  name: "Refund reviewers",
};

const DEMO_DIRECTORY: ReviewerTarget[] = [
  REFUND_REVIEWERS,
  { type: "escalation_path", id: "payments-oncall", name: "Payments on-call" },
];

/* A leaf condition as a one-row group, the way the editor holds it. */
function toolIs(id: string, tool: string): ConditionNode {
  return { id, all: [{ id: `${id}-1`, field: "/request/tool", operator: "equals", value: tool }] };
}

function amountAtLeast(id: string, cents: number): ConditionNode {
  return { id, all: [{ id: `${id}-1`, field: "/request/arguments/amount_cents", operator: "greater_than_or_equal", value: cents }] };
}

const SMALL_REFUNDS: PolicyBlockDraft = {
  id: "small-refunds-inside",
  name: "Approve small refunds",
  enabled: true,
  outcome: "approve",
  reason: "Small refunds are pre-authorized.",
  when: { id: "sg-small", all: [{ id: "sg-small-1", field: "/request/arguments/amount_cents", operator: "less_than", value: 1000 }] },
};

const DEMO_BRANCH: BranchBlockDraft<PolicyBlockDraft> = {
  id: "refunds",
  name: "Refunds",
  enabled: true,
  when: toolIs("sg-refunds", "stripe.refund"),
  blocks: [SMALL_REFUNDS],
  reason: "Refunds need a person when no check decides.",
  target: REFUND_REVIEWERS,
};

type WellDemoState = "idle" | "enter" | "skip";

export function PolicyBuilderSection() {
  const [webhook, setWebhook] = useState<WebhookBlockDraft>(DEMO_WEBHOOK);
  const [webhookExpanded, setWebhookExpanded] = useState(false);
  const [unknownExpanded, setUnknownExpanded] = useState(true);
  const [unknownSummaryOpen, setUnknownSummaryOpen] = useState(false);
  const [outcome, setOutcome] = useState<BlockOutcome>("approve");
  const [condition, setCondition] = useState<ConditionNode>(NESTED_CONDITION);
  const [block, setBlock] = useState<PolicyBlockDraft>(DEMO_BLOCK);
  const [always, setAlways] = useState<AlwaysBlockDraft>({ id: "remaining", name: "Review remaining requests", enabled: true, outcome: "human", reason: "All remaining requests need human review." });
  const [alwaysExpanded, setAlwaysExpanded] = useState(true);
  const [expanded, setExpanded] = useState(false);
  const [foldOpen, setFoldOpen] = useState(false);
  const [summaryOpen, setSummaryOpen] = useState(true);
  const [webhookSummaryOpen, setWebhookSummaryOpen] = useState(true);
  const [foldLocked, setFoldLocked] = useState(false);
  const [railFoldOpen, setRailFoldOpen] = useState(false);
  const [branch, setBranch] = useState<BranchBlockDraft<PolicyBlockDraft>>(DEMO_BRANCH);
  const [branchExpanded, setBranchExpanded] = useState(true);
  const [branchSummaryOpen, setBranchSummaryOpen] = useState(false);
  const [wellState, setWellState] = useState<WellDemoState>("enter");

  return (
    <Section
      id="policy-builder"
      title="Policy builder"
      lede="Primitives for the approval pipeline editor: ordered policy blocks whose conditions read as sentences. The JSON document view stays available as a peer mode; both edit the same pipeline document. Full composition lives at /pipelines."
    >
      <Demo
        label="OutcomeSelect"
        hint="what a matching block does"
      >
        <div className="flex flex-col gap-3">
          <OutcomeSelect value={outcome} onValueChange={setOutcome} />
          <p className="text-micro text-ink-muted">
            The pressed segment carries the decision color: approve, deny, or the warm human tone.
            Exactly one outcome is always selected; a block cannot match into nothing.
          </p>
        </div>
        <KeyboardHint>
          Arrows move between outcomes; Enter or Space presses the focused one.
        </KeyboardHint>
      </Demo>

      <Demo
        label="ConditionBuilder"
        hint="field, operator, value; groups nest"
      >
        <div className="flex flex-col gap-3">
          <ConditionBuilder tools={DEMO_TOOLS}
            value={condition}
            onChange={setCondition}
            suggestions={SUGGESTIONS}
            valueSuggestions={VALUE_SUGGESTIONS}
          />
          <p className="text-micro text-ink-muted">
            The field input names fields the way a reviewer says them: Tool name, Agent slug,
            Agent name, and the dotted key of any argument the agents have sent (learned from their recent requests, saved conditions, and the test sample). Each
            suggestion shows the JSON Pointer it stores, the pointer appears while the field has
            focus, and typing any pointer still works. Search matches names and pointers alike.
            Values suggest known options for the selected field and filter as you type.
            Use the arrow keys and Enter to select, or type a custom value. Lists hide options already selected.
            The operator decides the value editor: lists become chips, numbers get a
            numeric input, presence checks need no value at all. Values compare without coercion,
            so scalar comparisons keep an explicit text or number type. some and every array
            predicates stay JSON-only for now and render as locked rows.
          </p>
        </div>
      </Demo>

      <Demo
        label="PolicyBlockCard"
        hint="click the header to expand"
      >
        <div className="flex flex-col gap-3">
          <PolicyBlockCard
            block={block}
            position={2}
            expanded={expanded}
            onExpandedChange={setExpanded}
            onChange={setBlock}
            onRemove={() => undefined}
            onDuplicate={() => undefined}
            onMoveUp={() => undefined}
            onMoveDown={() => undefined}
            handleProps={{}}
            suggestions={SUGGESTIONS}
            valueSuggestions={VALUE_SUGGESTIONS}
          />
          <p className="text-micro text-ink-muted">
            Collapsed, a block shows its name and a subtitle with the Condition type and condition
            summary in machine mono. Exact tool, agent slug, and agent name values open
            reference cards on hover. Expanded, it edits everything in place, with field guidance
            in info icons beside labels. Named reviewers and escalation paths open the same cards
            in the picker and on pipeline branches; default reviewer chips explain the fallback.
            The grip drags a block to a
            new position in the execution order; Move up and Move down stay in the overflow menu
            as the keyboard path. Every kind of block shares this header (BlockCard): the grip,
            the position pill, which widens for a nested position such as 3.2.1, the name and
            summary, and the block menu, whose moves into and out of a branch appear only where
            they apply.
          </p>
        </div>
      </Demo>

      <Demo label="AlwaysBlockCard" hint="every request that reaches this block gets the selected outcome">
        <AlwaysBlockCard block={always} position={3} expanded={alwaysExpanded} onExpandedChange={setAlwaysExpanded}
          onChange={setAlways} onRemove={() => undefined} onDuplicate={() => undefined} />
        <PipelineExits exits={[{ key: "match", label: "Always", outcome: always.outcome, target: always.target }]} />
        <p className="mt-3 text-micro text-ink-muted">The rail ends here. Later blocks remain editable and carry an Unreachable warning.</p>
      </Demo>
      <Demo label="AlwaysBlockSummaryCard" hint="the saved block shows its outcome, reviewers, and reason">
        <AlwaysBlockSummaryCard block={always} position={3} expanded onExpandedChange={() => undefined} />
      </Demo>

      <Demo
        label="UnknownBlockCard"
        hint="a block of a kind this edition does not carry"
      >
        <div className="flex flex-col gap-3">
          <div className="flex flex-col">
            <UnknownBlockCard
              block={DEMO_UNKNOWN}
              position={3}
              expanded={unknownExpanded}
              onExpandedChange={setUnknownExpanded}
              onRemove={() => undefined}
              onDuplicate={() => undefined}
              onMoveUp={() => undefined}
              onMoveDown={() => undefined}
              handleProps={{}}
              describeRoute={() => "Uses pipeline default: Any authorized reviewer."}
            />
            <PipelineExits exits={[]} continueLabel="Continue" />
          </div>
          <p className="text-micro text-ink-muted">
            An edition registers the block kinds its pipelines can run through the block-kind
            registry; each brings its own editing card and read-only face, both on this shared
            anatomy. A stored revision can still name a kind this bundle does not know, such as a
            kind another edition adds. The rail draws it with this card: the subtitle names the
            stored type, the body says the block cannot be edited here and shows what every block
            has, the reason and the escalation path, and its settings go back exactly as stored.
            The block menu stays, so the block can be moved or removed from a draft, but a revision
            that keeps it is refused until it is removed. Its exits are its own business: the rail
            runs on past it.
          </p>
        </div>
      </Demo>

      <Demo
        label="UnknownBlockSummaryCard"
        hint="the read-only face of a kind this edition does not carry"
      >
        <UnknownBlockSummaryCard
          block={DEMO_UNKNOWN}
          position={3}
          expanded={unknownSummaryOpen}
          onExpandedChange={setUnknownSummaryOpen}
          describeRoute={() => "Uses pipeline default: Any authorized reviewer."}
        />
      </Demo>

      <Demo
        label="WebhookBlockCard"
        hint="click the header to expand"
      >
        <div className="flex flex-col gap-3">
          <div className="flex flex-col">
            <WebhookBlockCard
              block={webhook}
              endpoints={DEMO_ENDPOINTS}
              position={4}
              expanded={webhookExpanded}
              onExpandedChange={setWebhookExpanded}
              onChange={setWebhook}
              onRemove={() => undefined}
              onDuplicate={() => undefined}
              onMoveUp={() => undefined}
              onMoveDown={() => undefined}
              onManageEndpoints={() => undefined}
              handleProps={{}}
            />
            <PipelineExits {...webhookExits(webhook)} />
          </div>
          <p className="text-micro text-ink-muted">
            The block names an endpoint the organization manages elsewhere (URL and signing
            secret) and decides which answers the endpoint may give. The collapsed summary shows
            the Webhook type and endpoint name; delivery settings stay in the expanded editor.
            Info icons beside labels explain each field, while endpoint problems remain visible.
            Allowed answers use checkboxes. Hover or focus one to see what authority it gives the service.
            A new block allows only
            Continue and Ask a human, so it enriches or escalates until an administrator grants
            Approve or Deny. The exits are one branch per allowed decision; Continue runs down
            the rail with the endpoint's metadata available to later blocks.
          </p>
        </div>
      </Demo>

      <Demo
        label="WebhookBlockSummaryCard"
        hint="read-only face"
      >
        <div className="flex flex-col">
          <WebhookBlockSummaryCard
            block={webhook}
            endpoints={DEMO_ENDPOINTS}
            position={4}
            onNavigate={() => undefined}
            expanded={webhookSummaryOpen}
            onExpandedChange={setWebhookSummaryOpen}
            describeRoute={() => "Asks the pipeline default reviewers."}
          />
          <PipelineExits {...webhookExits(webhook)} matchedKey="human" />
        </div>
        <p className="text-micro text-ink-muted">
          The summary line names the Webhook type and endpoint. Only the endpoint name opens
          the reference card on hover. With onExpandedChange the header opens to the endpoint,
          allowed answers, inputs, timing, path, and reason as text. In a test replay
          a delivery line appears under the card: host, status, latency, and the endpoint's own
          reason.
        </p>
      </Demo>

      <Demo
        label="PipelineAddSlot with choices"
        hint="the four kinds of block core carries"
      >
        <div className="flex flex-col gap-3">
          <PipelineAddSlot
            choices={[
              { id: "always", label: "Always block", icon: <ChevronDownIcon width={14} height={14} /> },
              { id: "cel", label: "Conditions block", icon: <BranchIcon width={14} height={14} /> },
              { id: "webhook", label: "Webhook block", icon: <SettingsIcon width={14} height={14} /> },
              {
                id: "branch",
                label: "Branch",
                icon: <CornerDownRightIcon width={14} height={14} />,
                info: "Runs its own blocks only for requests that match its conditions. If none of them decides, the request goes to a human and the pipeline stops.",
              },
            ]}
            onChoose={() => undefined}
          />
          <p className="text-micro text-ink-muted">
            The slot keeps its place at the end of every rail, a branch's included; clicking opens
            the kind menu. A choice can carry one more sentence behind an info mark, as Branch
            does. The kinds an edition registers join the menu after Webhook, before Branch, each
            with its own sentence. A deployment without a secrets key lists the webhook choice
            disabled, and a registered kind the deployment cannot run is listed disabled with the
            reason its registry entry gives.
          </p>
        </div>
      </Demo>

      <Demo
        label="BranchBlockCard"
        hint="a branch's own fields; the rail draws its blocks"
      >
        <div className="flex flex-col gap-3">
          <BranchBlockCard
            block={branch}
            position={3}
            expanded={branchExpanded}
            onExpandedChange={setBranchExpanded}
            onChange={setBranch}
            onRemove={() => undefined}
            onDuplicate={() => undefined}
            onMoveUp={() => undefined}
            onMoveDown={() => undefined}
            onMoveOutOfBranch={() => undefined}
            onUnwrap={() => undefined}
            handleProps={{}}
            tools={DEMO_TOOLS}
            suggestions={SUGGESTIONS}
            valueSuggestions={VALUE_SUGGESTIONS}
            directory={DEMO_DIRECTORY}
          />
          <p className="text-micro text-ink-muted">
            A branch runs its own blocks only for requests its conditions match; the rest skip
            them all. The card edits the branch itself: its name, When (the same condition
            builder), and what happens when no block inside decides, which is always a human,
            with the branch's Escalation path (else the pipeline default) and Reason, after which
            the pipeline stops. Blocks inside that ask a human without a path of their own use the
            pipeline default, never the branch's. The subtitle reads Branch and the condition. The
            menu says Duplicate branch, which copies every block inside with new keys, and, while
            the branch holds blocks, Remove branch, keep its blocks. Inside a branch the menu also
            offers Move out of this branch; right after a branch, Move into the branch above.
          </p>
        </div>
      </Demo>

      <Demo
        label="BranchBlockSummaryCard"
        hint="the read-only face of a branch"
      >
        <BranchBlockSummaryCard
          block={branch}
          position="3"
          expanded={branchSummaryOpen}
          onExpandedChange={setBranchSummaryOpen}
          describeRoute={() => ""}
        />
        <p className="mt-3 text-micro text-ink-muted">
          Expanded, it lays out the condition as a read-only tree, then who is asked when no block
          inside decides, and the reason. The blocks inside are drawn by the rail, in the well
          beneath the card.
        </p>
      </Demo>

      <Demo
        label="PipelineBranchWell"
        hint="the elbow into a branch; the rail runs on past it"
      >
        <div className="flex flex-col gap-3">
          <ToggleGroup
            aria-label="Test run"
            className="self-start"
            value={wellState}
            onValueChange={(next) => setWellState(next as WellDemoState)}
          >
            <ToggleGroupItem value="idle">No test</ToggleGroupItem>
            <ToggleGroupItem value="enter">Entered</ToggleGroupItem>
            <ToggleGroupItem value="skip">Skipped</ToggleGroupItem>
          </ToggleGroup>
          <div className="flex flex-col">
            <BranchBlockSummaryCard block={branch} position="3" />
            <PipelineBranchWell
              name={branch.name}
              depth={1}
              taken={wellState === "idle" ? undefined : wellState}
            >
              <div className={cn("flex flex-col transition-opacity duration-200", wellState === "skip" && "opacity-40")}>
                <PolicyBlockSummaryCard block={SMALL_REFUNDS} position="3.1" />
                <PipelineConditionExits outcome="approve" continueLabel="Otherwise" />
                <PipelineBranchEndCard branchName={branch.name} target={branch.target} reason={branch.reason} />
              </div>
            </PipelineBranchWell>
            <PolicyBlockSummaryCard
              block={{ ...SMALL_REFUNDS, id: "after", name: "Deny deletes", outcome: "deny", when: toolIs("sg-after", "crm.delete_customer") }}
              position="4"
            />
          </div>
          <p className="text-micro text-ink-muted">
            If conditions are met, the rail turns right and drops through an elbow into an
            indented well that holds the branch's blocks; the accent badge says so. The parent
            rail keeps running down to the left of the well and continues under it: Otherwise,
            skip this branch and check the next block (or the rail's own last words when the
            branch comes last). A test run lights the elbow when the request entered and the skip
            row when it did not, and a skipped branch's blocks and end fade as unreached. Wells
            are groups labeled with the branch's name.
          </p>
        </div>
      </Demo>

      <Demo
        label="PipelineBranchEndCard"
        hint="where a branch sends a request nothing inside decided"
      >
        <div className="flex flex-col gap-3">
          <PipelineBranchEndCard branchName="Refunds" target={REFUND_REVIEWERS} reason={DEMO_BRANCH.reason} />
          <PipelineBranchEndCard branchName="Deploys" defaultTarget={null} />
          <p className="text-micro text-ink-muted">
            The last card in every well, framed like the pipeline's terminus: no block in the
            branch decided, so a human is asked, through the branch's escalation path or else the
            pipeline default, with the branch's reason. The pipeline stops there; nothing after
            the branch runs. A test run that ran out of the branch rings this card.
          </p>
        </div>
      </Demo>

      <Demo
        label="Branch nesting"
        hint="wells alternate surfaces and indent about 48px a level"
      >
        <div className="flex flex-col">
          <BranchBlockSummaryCard
            block={{ ...DEMO_BRANCH, id: "payments", name: "Payments", when: toolIs("sg-n1", "stripe.refund"), target: null }}
            position="3"
          />
          <PipelineBranchWell name="Payments" depth={1}>
            <BranchBlockSummaryCard
              block={{ ...DEMO_BRANCH, id: "refunds-n", name: "Refunds", when: amountAtLeast("sg-n2", 100) }}
              position="3.1"
            />
            <PipelineBranchWell name="Refunds" depth={2} continueLabel="Otherwise">
              <PolicyBlockSummaryCard
                block={{ ...SMALL_REFUNDS, id: "fraud", name: "Deny flagged customers", outcome: "deny", when: toolIs("sg-n3", "stripe.refund") }}
                position="3.1.1"
              />
              <PipelineConditionExits outcome="deny" />
              <BranchBlockSummaryCard
                block={{ ...DEMO_BRANCH, id: "large", name: "Large refunds", when: amountAtLeast("sg-n4", 10000), target: null }}
                position="3.1.2"
              />
              <PipelineBranchWell name="Large refunds" depth={3} continueLabel="Otherwise">
                <PolicyBlockSummaryCard
                  block={{ ...SMALL_REFUNDS, id: "finance", name: "Ask finance", outcome: "human", when: amountAtLeast("sg-n5", 50000) }}
                  position="3.1.2.1"
                />
                <PipelineConditionExits outcome="human" continueLabel="Otherwise" />
                <PipelineBranchEndCard branchName="Large refunds" reason="Large refunds need a person." />
              </PipelineBranchWell>
              <PipelineBranchEndCard branchName="Refunds" target={REFUND_REVIEWERS} reason={DEMO_BRANCH.reason} />
            </PipelineBranchWell>
            <PipelineBranchEndCard branchName="Payments" reason="Payments need a person when no check decides." />
          </PipelineBranchWell>
          <PolicyBlockSummaryCard
            block={{ ...SMALL_REFUNDS, id: "after-nesting", name: "Deny deletes", outcome: "deny", when: toolIs("sg-n6", "crm.delete_customer") }}
            position="4"
          />
        </div>
        <p className="mt-3 text-micro text-ink-muted">
          Branches nest without limit. Positions follow the tree, so 3.1.2.1 is the first block of
          the second block of the first block of branch 3, and the position pill widens to fit.
          Wells alternate the surface and the canvas by depth, the same rule the escalation
          tree's wells follow, and each level indents about 48px (less on a phone).
        </p>
      </Demo>

      <Demo
        label="PolicyBlockSummaryCard"
        hint="the view-mode face of a conditions block; with onExpandedChange it opens to the same details the editing card shows, as text"
      >
        <PolicyBlockSummaryCard
          block={block}
          position={1}
          expanded={summaryOpen}
          onExpandedChange={setSummaryOpen}
          describeRoute={() => "Asks the pipeline default reviewers."}
        />
        <p className="mt-3 text-micro text-ink-muted">
          Viewing a pipeline uses this card for every conditions block. The header stays a
          one-line summary; expanding it lays out the condition as a read-only tree, then
          the outcome, the escalation path, and the reason. Without onExpandedChange the
          card is a plain row, as in a scope that is not being edited.
        </p>
      </Demo>

      <Demo
        label="Pipeline rail"
        hint="one scaffolding for viewing and editing; exact tool names open reference cards when the app provides them"
      >
        <div className="flex flex-col">
          <PipelineEntryCard agentSlug="billing" />
          <PipelineConnector label="First, check organization rules" />
          <PipelineFoldedSection
            title="Organization pipeline"
            positionLabel="1"
            summary="1 block applies to every agent. Expand to see the checks."
            expanded={railFoldOpen}
            onExpandedChange={setRailFoldOpen}
          >
            <PolicyBlockSummaryCard
              block={{
                id: "never-delete",
                name: "Never delete customers",
                enabled: true,
                outcome: "deny",
                reason: "",
                when: {
                  id: "sg-ro",
                  all: [
                    { id: "sg-ro-1", field: "/request/tool", operator: "equals", value: "crm.delete_customer" },
                  ],
                },
              }}
              position={1}
            />
          </PipelineFoldedSection>
          <PipelineConnector label="If no decision is made" />
          <div className="rounded-lg border-hairline border-edge bg-surface px-4 py-3 text-body text-ink-muted shadow-sm">
            The pipeline's blocks render here in execution order: summary rows when viewing,
            editable cards when editing.
          </div>
          <PipelineConditionExits outcome="approve" />
          <BranchBlockSummaryCard block={DEMO_BRANCH} position="3" />
          <PipelineBranchWell name={DEMO_BRANCH.name} depth={1} continueLabel="Otherwise">
            <PolicyBlockSummaryCard block={SMALL_REFUNDS} position="3.1" />
            <PipelineConditionExits outcome="approve" continueLabel="Otherwise" />
            <PipelineAddSlot label="Add block to this branch" onClick={() => undefined} />
            <PipelineConnector />
            <PipelineBranchEndCard branchName={DEMO_BRANCH.name} target={DEMO_BRANCH.target} reason={DEMO_BRANCH.reason} />
          </PipelineBranchWell>
          <PipelineAddSlot onClick={() => undefined} />
          <PipelineConnector />
          <PipelineTerminusCard />
        </div>
        <p className="mt-3 text-micro text-ink-muted">
          The effective pipeline concatenates the organization section and one agent's section;
          positions run across both, and blocks inside a branch number from the branch (3.1).
          Every block exits through PipelineExits (PipelineConditionExits for a conditions block):
          the horizontal match branch shows the block's terminal outcome in its decision color. A
          branch exits through a PipelineBranchWell instead: its blocks, their own add slot while
          editing, and its end card sit in the well, and the rail skips past it. All connector and
          branch labels use the same sentence-case UI text. Downward connectors share one height
          and meet the card borders without gaps, while the rail continues down with no match.
          Conditions live on the cards; consequences live on the exits. Viewing and editing share
          this exact scaffolding: Edit swaps the read-only rows for editable cards in place, the
          add slots appear, and a SaveBar rises once there are changes, with Create revision as
          its commit. Dragging reorders blocks within one rail; the block menu moves a block into
          or out of a branch. The organization pipeline's own rail shows no agent section and
          ends in a continue card instead of the human terminus.
        </p>
      </Demo>

      <Demo
        label="PipelineFoldedSection"
        hint="the organization section inside an agent's pipeline editor"
      >
        <div className="flex flex-col gap-3">
          <PipelineFoldedSection
            title="Organization pipeline"
            positionLabel="1"
            summary="1 block applies to every agent. Expand to see the checks."
            badge={foldLocked ? <Badge icon={<LockIcon width={13} height={13} />}>View only</Badge> : null}
            action={
              foldLocked ? undefined : (
                <Button size="sm" variant="ghost" onClick={() => undefined}>
                  Edit
                </Button>
              )
            }
            expanded={foldOpen}
            onExpandedChange={setFoldOpen}
          >
            <PolicyBlockSummaryCard
              block={{
                id: "never-delete",
                name: "Never delete customers",
                enabled: true,
                outcome: "deny",
                reason: "",
                when: {
                  id: "sg-fold",
                  all: [
                    { id: "sg-fold-1", field: "/request/tool", operator: "equals", value: "crm.delete_customer" },
                  ],
                },
              }}
              position={1}
            />
          </PipelineFoldedSection>
          <label className="flex items-center gap-2 text-micro text-ink-muted">
            <input
              type="checkbox"
              checked={foldLocked}
              onChange={(event) => setFoldLocked(event.target.checked)}
            />
            Show the locked variant
          </label>
          <p className="text-micro text-ink-muted">
            An agent's pipeline editor folds the organization section to this single row by default:
            the blocks running first stay one click away without competing with the blocks being
            edited. The organization editor shows no agent section at all; its rail ends
            with a continue card instead of the human terminus.
          </p>
        </div>
      </Demo>

    </Section>
  );
}
