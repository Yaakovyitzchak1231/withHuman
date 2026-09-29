# withHuman: branch blocks in approval pipelines

Status: accepted · Last updated: 2026-09-29 (a branch's end may ask the agent's owner)

> **Prototype.** Same caveat as
> [00-general-design.md](00-general-design.md): everything here can change
> without migration paths or backwards-compatibility constraints. Optimize for
> the right model.

## Summary

A pipeline used to be a flat list. Only Conditions (`cel`) blocks had a
condition, and a match could only end the pipeline in `approve`, `deny`, or
`human`. Webhook and AI Judge blocks ran on every request that reached them.
Because hook-based adapters post every tool call, a judge anywhere in a
pipeline ran, and suspended the request into Temporal, on every `Read` and
`Grep`.

`branch` is the fifth `RequestApprovalBlock` type, after `always`, `cel`,
`llm_judge`, and `webhook`. A branch holds a condition (`when`) and its own
ordered list of blocks of any type, including further branches. A request
the condition does not match skips the whole list. A request it matches runs
the list and is settled inside it: when none of the blocks decides, the
branch hands the request to a human with its own reason and escalation path,
and nothing after the branch runs.

That expresses the scoped chain this design was written for:

```text
if request.tool == "stripe.refund" {
    fraud webhook       // approve -> approve, deny -> deny, otherwise next
    AI judge            // approve -> next, deny or escalate -> human
    amount < 1000       // -> approve
    return human        // nothing decided: the Payments team reviews it
}
```

Requests for every other tool (`Read`, `Bash`, and so on) skip the branch:
no webhook delivery, no model call, no suspension.

Branches are the only structural change. They add no references between
blocks and no tool-scoped pipelines; enrichment (named facts that conditions
read) is a later, separate design. Escalation path documents have their own
`if_else` nodes, which [06-routing-and-escalation.md](06-routing-and-escalation.md)
also calls branches; they choose reviewers inside a path and are unrelated
to this block type.

## Goals

- Scope expensive or external blocks (judges, webhooks) to the requests
  they are about, so other requests cause no delivery, no model call, and no
  suspension.
- State "for these requests, run this chain" once, instead of repeating the
  tool condition on every block.
- Keep the [one function body](01-request-approval-pipeline.md#execution-model-one-function-body)
  reading: a branch is an `if` with a body.
- Keep storage, activation checks, uses and archive refusals, routing,
  review replay, and evidence working with no migration.

## Non-goals

- References from a block to an earlier block's result by path. Earlier
  metadata stays readable through `previous_metadata`, unchanged.
- Tool-scoped pipelines.
- Enrichment: facts computed once per request and read by conditions.
- A free graph, jumps, loops, or exits that lead into sub-lists.
- New outcomes on existing block types. A Conditions block still has three
  exits and `next`.
- Collapsing branches in the editor (deferred).

## Semantics

- **Condition.** `when` is the Conditions block's condition envelope: the
  same fields (tool name, MCP server, agent slug and name, arguments, and
  `previous_metadata`, so a branch can test what an earlier webhook
  returned), the same operators, `all`/`any` groups, `some`/`every`, limits,
  and value-free trace. It runs synchronously when execution reaches the
  branch.
- **No match: skip.** None of the branch's blocks run and execution
  continues with the block after the branch. A skipped branch causes no
  delivery, no judge call, and no suspension.
- **Match: enter.** The branch's blocks run in order under the normal block
  contract. `approve`, `deny`, or `human` from any of them ends the whole
  effective pipeline, both sections; `next` moves to the next block in the
  branch.
- **Nothing inside decides: end of branch.** The request goes to a human
  with the branch's `reason` and `escalation_path`, or the pipeline default
  when the branch names none, and the pipeline stops. Blocks after the
  branch never run for that request, and neither does the agent section
  when the branch sits in the organization section. Entering a branch
  commits to it. An inner branch's end also ends the pipeline: it does not
  cascade into the rest of the outer branch.
- **Blocks after a branch are its `else`.** Because entering commits, only
  requests the branch skipped ever reach the blocks after it. The editor's
  continue row says exactly that.
- **No reviewer inheritance.** A human exit inside a branch that names no
  escalation path uses the pipeline default, never the branch's path. The
  branch's path applies to its own end only.
- **Failed condition.** A condition that cannot be evaluated (a wrong value
  type, cost exhaustion, timeout, an internal panic) returns `human` with
  the safe error code, routed to the pipeline default. It never uses the
  branch's path, because a failed condition cannot say whether the request
  belongs to the branch, and it is never a silent skip, because skipping
  could bypass the checks inside, the same rule as a failed `cel` block.
- **Always inside a branch.** An `always` block decides every request that
  reaches it, so its later siblings and the branch's own end become
  unreachable. Blocks after the branch still run for requests the branch
  skipped. An Always outside every branch keeps its existing effect: the
  rest of its section is unreachable and, in the organization section, so
  is every agent section. Unreachability is an editor warning, never a save
  or activation error.
- **Identity.** Block keys are unique across the whole revision, including
  blocks inside branches. Condition ids are unique across every `cel` and
  branch `when` tree in the revision.
- **Metadata.** `previous_metadata` stays flat, keyed `scope/block_key`. A
  block inside a branch writes under its own key and reads metadata from
  every earlier block, inside the branch or before it. A branch returns no
  metadata of its own. Blocks after a branch never see metadata written
  inside it, because only requests that skipped it reach them.

In the function-body reading:

```text
func pipeline(request) outcome {
    // organization section
    if request.tool == "stripe.refund" {      // branch "refunds"
        fraud-check                            // webhook: approve, deny, or falls through
        refund-eligibility                     // judge: falls through or returns human
        if amount < 1000 { return approve }    // cel
        return human(branch reason, payments-review)
    }
    deploy-review                              // reached only by requests "refunds" skipped
    // agent section
    ...
    return human(pipeline default)
}
```

## Decisions (Chris, 2026-09-23)

- **Branch blocks only.** No references between blocks and no tool-scoped
  pipelines. Enrichment is a later, separate design.
- **Name: Branch**, `type: "branch"`, not Group. The condition builder
  already has a Group button for nested All/Any conditions, and two
  different groups in one editor would be confusing. The Group button keeps
  its name.
- **No depth cap.** Branches may contain branches to any depth. The
  existing 1,000-block limit per pipeline document counts every nested
  block, which bounds the tree, and decoding stays linear (see
  [Limits](#limits)).
- **Pipeline-default reviewers.** A human exit inside a branch without its
  own escalation path uses the pipeline default. A branch's path covers its
  own "nothing decided" end only. Inheritance would make a block's reviewers
  depend on where it sits, so moving a block into or out of a branch would
  silently change who reviews it.

## Block definition

```json
{
  "id": "refunds",
  "name": "Refunds",
  "type": "branch",
  "enabled": true,
  "timeout": "50ms",
  "max_attempts": 1,
  "config": {
    "when": {
      "id": "6c1f0e2a-4b7d-4e35-9a1c-8d2b5f3e7a90",
      "field": "/request/tool",
      "operator": "equals",
      "value": "stripe.refund"
    },
    "blocks": [
      {
        "id": "fraud-check",
        "name": "Fraud service",
        "type": "webhook",
        "enabled": true,
        "timeout": "5s",
        "max_attempts": 3,
        "config": {
          "endpoint_key": "fraud-service",
          "allowed_outcomes": ["approve", "deny", "next"],
          "include": { "agent_reasoning": false, "context": false, "previous_metadata": true },
          "reason": "The fraud service reviewed the refund."
        }
      },
      {
        "id": "refund-eligibility",
        "name": "Check refund eligibility",
        "type": "llm_judge",
        "enabled": true,
        "timeout": "30s",
        "max_attempts": 2,
        "config": {
          "effort": "medium",
          "instructions": "Approve refunds for documented duplicate charges. Deny refunds the evidence shows are ineligible. Escalate everything else.",
          "include": { "agent_reasoning": true, "context": true, "previous_metadata": true },
          "on_verdict": { "approve": "next", "deny": "human", "escalate": "human" },
          "automatic_decisions_require_confidence": "high",
          "reason": "Refund eligibility checked against the payment evidence.",
          "escalation_path": "payments-review"
        }
      },
      {
        "id": "small-refunds",
        "name": "Approve small refunds",
        "type": "cel",
        "enabled": true,
        "timeout": "50ms",
        "max_attempts": 1,
        "config": {
          "when": {
            "id": "b2d94f1e-7c3a-4a58-8e06-3f1c9d7b2e45",
            "field": "/request/arguments/amount",
            "operator": "less_than",
            "value": 1000
          },
          "on_match": "approve",
          "reason": "Small refunds that passed the fraud and eligibility checks are pre-authorized."
        }
      }
    ],
    "reason": "No refund check decided, so Payments reviews the refund.",
    "escalation_path": "payments-review"
  }
}
```

The judge names `payments-review` itself because it does not inherit the
branch's path. The webhook names none, so a failed delivery goes to the
pipeline default.

| Field | Meaning | Rules |
| --- | --- | --- |
| `timeout` | Evaluation of the branch's own `when` | 50 ms default, 1 s maximum, like `cel`; each child keeps its own envelope |
| `max_attempts` | Attempts for the condition | Exactly 1 |
| `enabled` | As for every block | Always `true`; leave disabled blocks out |
| `config.when` | The condition envelope | Required; same validation as a `cel` condition |
| `config.blocks` | The branch's blocks, in order | 1 to 999 (with the branch, the pipeline's 1,000); any type, including `branch` |
| `config.reason` | Recorded when no block in the branch decides | Required |
| `config.escalation_path` | Where a request that runs out of the branch goes | Optional; the pipeline default when absent |
| `config.ask_agent_owner` | Ask the agent's owner first, with `escalation_path` as the fallback ([06](06-routing-and-escalation.md#agent-owner-route)) | Optional; requires `escalation_path` |

`config_version` is `1`. There is no `on_match`: a match always enters the
branch. The Go DSL adds `approvalblock.DefineBranch(id, blocks, options...)`,
which takes the same `Name`, `When`, and `Reason` options as the other
types.

## Storage

No migration. Every block, nested or not, is its own immutable snapshot row
in `request_approval_blocks` (the `block_type` column only checks the key
pattern, so `branch` fits). The revision's `block_ids` lists every block in
pre-order: a branch, then its subtree, then the branch's next sibling. A
branch row's stored config is the document config with its children
replaced by their keys:

```json
{
  "when": {
    "id": "6c1f0e2a-4b7d-4e35-9a1c-8d2b5f3e7a90",
    "field": "/request/tool",
    "operator": "equals",
    "value": "stripe.refund"
  },
  "block_keys": ["fraud-check", "refund-eligibility", "small-refunds"],
  "reason": "No refund check decided, so Payments reviews the refund.",
  "escalation_path": "payments-review"
}
```

`block_keys` names the branch's direct children, in order. Documents
(create, read, preview, and the MCP tools) nest children under
`config.blocks`, and reads carry the snapshot fields (`snapshot_id`,
`config_version`, `content_hash`) at every level. Reads and compilation
rebuild the tree from the pre-order rows and `block_keys`, and refuse a
malformed stored tree, such as a `block_keys` entry that does not match the
next row in pre-order, as a load or compile error.

For an organization revision holding the `refunds` branch and one more
Conditions block, `block_ids` and the positions are:

| Position | Block | Parent |
| ---: | --- | --- |
| 0 | `refunds` (branch) | |
| 1 | `fraud-check` | `refunds` |
| 2 | `refund-eligibility` | `refunds` |
| 3 | `small-refunds` | `refunds` |
| 4 | `deploy-review` | |

The agent section's positions continue from 5.

Keeping every block a row, rather than nesting children inside the branch's
`config`, keeps these working unchanged, because each already reads every
row in `block_ids`:

- the key-uniqueness constraint trigger from migration 0003, so block keys
  are unique across the whole tree with no new constraint;
- the activation reference guard (`readPipelineReferences`), so a nested
  block's escalation path or endpoint, and a branch's own path, must be
  active before activation;
- the escalation-path and webhook-endpoint uses queries and the archive
  refusals built on them;
- the routing snapshot's `block_id` foreign key, which can point at a
  branch snapshot when its end routes a request;
- the review's replay by snapshot id, which finds a nested terminal block
  or a branch;
- content-hash reuse: a branch row names its children by key, not by
  snapshot id, so editing only a child writes a new child row and reuses the
  unchanged branch row.

Nesting children inside the branch row would instead have meant parsing
JSON trees in every one of those queries, rewriting every ancestor row when
a descendant changes, and fitting a whole subtree into one block's 64 KiB
document limit.

## Execution

- **Compilation.** Compilation is tree-wide: it counts every block against
  the 1,000-block limit, checks unique keys and condition ids across all
  `cel` and branch `when` trees, and returns the compiled blocks flat, in
  pre-order: every block knows its enclosing branch, and every branch knows
  the position where its subtree ends. A branch compiles only its own
  `when`, the same way a `cel` block compiles, and the registry registers
  `branch` as synchronous.
- **Positions.** Positions are pre-order indexes across both sections,
  organization first, so `resume_position` stays one integer.
- **Condition.** When the cursor reaches a branch, its `when` is evaluated
  synchronously in whichever segment is running (the request path, or the
  worker after a suspension), drawing on the same CEL budget and recording
  the same trace and cost as a `cel` block. It returns only `enter` or
  `next`; an error or panic becomes `human`.
- **Skip.** `next` moves the cursor past the branch's subtree. Asynchronous
  blocks inside a skipped branch are never reached, so they never suspend
  the request.
- **Enter.** `enter` moves the cursor to the branch's first block. Its
  blocks run under the normal contract; the first asynchronous block the
  cursor reaches on the request path suspends the request as before.
- **End of branch.** When the cursor moves past the end of the innermost
  entered branch's subtree (its last block returned `next`, or its last
  block is a branch that was skipped), the branch ends: the closing evidence
  event is written and the run finishes with `human`, the branch's reason,
  and its route. Outer branches never reach their own end in that run.
- **Failed condition.** The run finishes with `human` and routes to the
  pipeline default, never to the branch's path.
- **Resume.** A suspension inside a branch resumes at the suspended child's
  position. The worker re-enters the ancestor branches from the revision's
  static structure, without evaluating their `when` again, so the
  synchronous segment's `enter` event is the only one and the resume never
  disagrees with it.
- **Preview.** Preview uses the same cursor. A skipped branch in a preview
  calls no model and no endpoint.

## Evidence

`request_approval_block.evaluated`:

- A branch condition writes one event with outcome `next` (skipped) or
  `enter`, plus its trace, cost, and `attempt_count` 1. `enter` is
  evidence-only: it is never a block result of another type, a pipeline
  outcome, or an AAP status.
- A branch a request runs out of writes a second, closing event for the
  same branch: outcome `human`, the branch's `reason`, `end_of_branch: true`,
  `attempt_count: 0`, `cost: 0`, `trace: []`, and the branch's own block
  fields. It adds nothing to `attempted_block_count`, total attempts, or
  total cost, so "N rules ran" stays true.
- Events keep execution order: the branch's `enter`, its blocks' events,
  then the closing event.

`request_approval_pipeline.completed` carries `reason_code: end_of_branch`
for a run that ends at a branch's end, and its terminal block is the branch
(ID, key, name, and scope). A failed branch condition completes with the
same error reason codes as a failed `cel` block, with the branch as the
terminal block.

Routing for an end of branch uses the branch's `escalation_path` with reason
`block_escalation` and the branch snapshot as the routing `block_id`;
without a path it falls to the pipeline default (`pipeline_default`) or the
default queue (`no_escalation`), exactly as for a block exit. A failed
branch condition is terminal too, but routing never uses the branch's path
for it.

A $50 refund (`amount` 5000) through the example:

| Segment | Event | Outcome |
| --- | --- | --- |
| Request path | `refunds` evaluated | `enter` |
| Request path | suspended before `fraud-check` | `resume_position` 1 |
| Worker | `fraud-check` evaluated | `next` |
| Worker | `refund-eligibility` evaluated | `next` |
| Worker | `small-refunds` evaluated | `next` |
| Worker | `refunds` evaluated, `end_of_branch: true` | `human` |
| Worker | completed, `reason_code: end_of_branch` | `human` |
| Worker | `routing.evaluated`, `payments-review` | `block_escalation` |

A `Read` request writes `refunds` evaluated with `next`, then continues at
`deploy-review` on the request path.

The review surfaces follow: `PipelineReview.terminal_block_type` gains
`branch`, and the request detail explains an end of branch as the branch
running without a decision, so human review is needed.

Preview mirrors the audit record. Preview block entries may carry outcome
`enter`, a closing entry for the branch has `end_of_branch: true`,
`reason_code` gains `end_of_branch`, and `terminal_block_id` names the
branch. The preview's `escalation_path_reason` follows the same routing
rules.

## Limits

- **Blocks.** At most 1,000 blocks per pipeline document, counted across
  nesting: a branch counts as one block, and each block inside it counts
  too. The OpenAPI `block_count` and uniqueness wording say "including
  blocks inside branches".
- **Block document size.** The 64 KiB canonical block document limit is
  measured per block without its children, so it bounds a branch's own
  condition and reason, not its subtree.
- **Condition budget.** One run segment performs at most 1,000 condition
  evaluations, shared by `cel` blocks and branch conditions
  (`MaximumCELBlocksPerRequest`). The per-block limits of
  [02-cel-policy-engine.md](02-cel-policy-engine.md#performance-and-resource-controls)
  apply to each branch `when`.
- **Audit events per segment.** The cap on audit events one initialization
  or resume may commit rises from 1,004 to a named constant of 1,015:
  - at most 1,000 condition evaluations;
  - plus 5 judge and 5 webhook evaluations (the per-request budgets);
  - plus 1 terminal event (an Always block's evaluation or a branch's
    closing event; a run has at most one);
  - plus 4 envelope events (`approval.created`,
    `request_approval_pipeline.started`, the completed or suspended event,
    and the decision or routing event).

  Without the raise, a branch holding 999 non-matching children (1,000
  condition evaluations plus its closing event) would fail request
  creation.
- **Depth.** Unbounded. JSON decoding is single-pass: a document is decoded
  once into a generic tree and each level is converted with strict envelope
  and config decoding. Re-decoding each nested level's raw JSON would be
  quadratic in the depth. Encoding and the HTTP stored-block representation
  (a typed recursive config) are single-pass for the same reason.

## Editor

- **One recursive rail** renders view mode, edit mode, and the folded
  organization section. The add-block menu gains **Branch**. The branch
  card's subtitle is "Branch · <condition summary>", and its fields are
  Name, When, the line "If no block in this branch decides, the request goes
  to a human and the pipeline stops.", the escalation path (default
  "Pipeline default"), and Reason, with docs links to
  `web-app/branch-blocks#when-a-branch-runs` and
  `web-app/branch-blocks#when-nothing-decides`. Those anchors come from the
  docs page's headings and must not change.
- **Wells.** "If conditions are met" leads along an elbow to "Check the
  blocks in this branch" and into an indented well holding the branch's
  rail. The parent rail continues to its left with the row "Otherwise, skip
  this branch and check the next block". Wells alternate the surface and
  canvas tones by depth, indent about 48px per level, and are
  `role="group"`. Each branch ends with an end card: "No block in
  “<name>” decided." then "Ask a human" with the reviewers.
- **Positions.** Cards carry hierarchical labels such as 3.2.1, and
  validation messages use the same labels ("Block 3.2"). Validation is
  tree-wide and flags an empty branch and a missing reason.
- **Adding and moving.** Every rail has its own add slot; a nested one
  reads "Add block to this branch". Dragging reorders only the rail's
  direct rows. Cross-level moves are menu actions: "Move into the branch
  above" (on a block directly after a branch, which joins it as its last
  block), "Move out of this branch" (the block leaves and follows its
  branch), "Duplicate branch" (a deep copy with fresh block keys and
  condition ids), and "Remove branch, keep its blocks" (its blocks take
  its place). Removing a non-empty branch asks "Remove “<name>” and its N
  blocks?".
- **Reachability** is computed per rail: an Always inside a branch marks
  its later siblings and the branch's end unreachable, while an Always
  outside every branch keeps marking the rest of its section, and in the
  organization section every agent section, unreachable.
- **Field hints.** The condition builder inside a branch narrows argument
  suggestions by the branch's tool condition.
- **Test replay.** Steps are keyed by section and block, with a separate
  end step for an entry flagged `end_of_branch`. The terminal step comes
  from `terminal_block_id` and `reason_code`, not from the last step. An
  entered branch lights the line into the branch; a skipped branch lights
  the "Otherwise" row and dims its children; a request that runs out of a
  branch highlights the branch's end card.

## Rollout

Server and worker must ship in one release. A binary without the `branch`
type cannot compile a revision that contains one: a server keeps readiness
closed, because startup compiles every active revision, and a worker
abandons such a resume to a human. Nobody should author branches until
every replica runs the release. For the same reason, rolling back to an
older build after a branch revision has been activated closes readiness
everywhere; activate a revision without branches first, or roll forward.

## Alternatives considered

| Alternative | Why not |
| --- | --- |
| A condition on every block (`when` on judge, webhook, and always blocks) | Scopes one block at a time, so a chain repeats its condition on every block, and there is no shared end where "nothing decided" gets its own reason and reviewers |
| Groups plus references to earlier blocks' results by path | Too complicated: paths need block keys and section names, a referenced block may not have run (missing values), and a condition reading a judge's raw verdict would bypass its verdict mapping and confidence threshold |
| Tool-scoped pipelines | Rejected: multiplies pipelines per scope, needs precedence rules between tool, agent, and organization pipelines, and still cannot scope by arguments or earlier results |
| Exits that can hold branches on every block type | Turns every block into a switch with a sub-list per outcome; readers must trace a tree of exits, and evidence, positions, and reachability all get harder |
| A free graph with jumps between blocks | Loses the one-function-body reading, needs cycle and reachability analysis, and positions stop being one integer |
| Enrichment of the event | The later way to share facts computed once per request; a separate design, not needed to scope blocks |

## Deferred

- Enrichment: named facts that conditions read.
- Collapsing branches in the editor.
- Pre-existing, unchanged by branches: webhook idempotency keys collide when
  the organization and agent sections each hold a webhook block with the
  same block key (`webhook.go`), because the key is the request ID plus the
  block key.
- Pre-existing, unchanged by branches: the review map is keyed by snapshot
  id across both sections, so one snapshot reused by both sections is
  indistinguishable there.
