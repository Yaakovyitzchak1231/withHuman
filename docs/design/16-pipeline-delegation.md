# withHuman: keyed pipelines and delegate blocks

Status: accepted · Last updated: 2026-09-29

> **Prototype.** Same caveat as
> [00-general-design.md](00-general-design.md): everything here can change
> without migration paths or backwards-compatibility constraints. Optimize for
> the right model.

## Summary

A request used to run two pipeline scopes back to back: the organization's
revision, then the authenticated agent's. The split existed so one large
pipeline stayed readable and so a central team could own the shared part
while agent-owning teams owned their agent's part. Splitting by agent was
one arbitrary axis: a company may want to split by MCP server, by tool
family, by environment, by anything a condition can express.

This design removes the agent scope. Every pipeline is a keyed, named,
revisioned object, exactly like an escalation path. One pipeline is the
organization's entry point: key `entry`, seeded when the organization is
activated, never archived. A pipeline hands matching requests to another
through a `delegate` block (`when` + `pipeline_key`). The condition belongs
to the pipeline being branched out of, the way an ingress belongs to the
team that routes traffic; everything after the match belongs to the target
pipeline's owners, who hold `pipeline.*` at the new `pipeline:<key>`
scope. Inline `branch` blocks are unchanged.

```text
entry (platform team)
  deny refunds over $500                      // shared rule
  if request.server == "stripe" -> payments   // delegate: the routing is ours
  if agent.slug == "deploy-bot" -> deploys    // delegate
  approve the rest

payments (Payments team)
  fraud webhook
  amount < 20 -> approve
  return human via payments-review            // the target's own default
```

## Model

- **Pipeline**: `pipeline_key` (pattern `^[a-z0-9][a-z0-9._-]{0,62}$`),
  a `name` on every revision (renaming is a revision), append-only numbered
  revisions with `default_escalation_path`, `block_ids` in pre-order,
  `is_active`, `archived_at`. Every live key has exactly one active
  revision at all times (deferred constraint trigger, keyed by pipeline
  key). `entry` is seeded in the activation transaction and refuses
  creation and archiving (409 `request_approval_pipeline_entry`).
- **Delegate block** (`type: "delegate"`, synchronous, 50ms default and
  1s maximum, one attempt): `config: {when, pipeline_key}`. No reason and
  no escalation path: the target owns its end. Semantics are a branch
  whose body is another pipeline. No match: `next`. Match: `enter`, and the
  run continues in the target's active blocks. A decision inside ends the
  run. When the target runs out of blocks, the run ends `human` with a
  closing evaluated event on the delegate (`end_of_branch: true`), the
  completed event's `reason_code` is `end_of_branch`, and the terminal
  block is the delegate.
- **Routing default chain**: a human outcome without a path, and the end
  of a delegated pipeline, route to the innermost delegated pipeline's
  `default_escalation_path`, else the enclosing pipelines' defaults walking
  outward, else the default queue. The agent-owner route
  (`ask_agent_owner` beside a block's path, `default_ask_agent_owner`
  beside a revision's default, see
  [06-routing-and-escalation.md](06-routing-and-escalation.md#agent-owner-route))
  travels with the path it sits beside: a block's own route is taken
  whole, and a default is inherited whole from whichever pipeline in the
  chain supplies it. This generalises the old agent-then-
  organization order. A delegate whose condition fails routes from the
  pipeline that owns the block, never the target.
- **Effective pipeline**: resolved at run start from the `entry` active
  revision plus, transitively, the active revision of every key a
  delegate in the closure names, flattened into one pre-order list where
  a delegate is immediately followed by the target's blocks (like a branch
  and its subtree). Positions index that list, `resume_position` stays one
  integer, and metadata keys are `<pipeline_key>/<block_key>`. The run
  pins the whole map of key to revision id
  (`approval_request_pipeline_suspension_pins`); resume re-flattens from
  the pins. Diamonds (`entry → A`, `entry → B`, `A → B`) are legal: B's
  blocks appear twice in the flattened list, only one copy ever runs.
- **Graph rules**, checked at activation under one organization-wide
  advisory lock (`<org>:pipeline-graph`, taken before the key lock and the
  path and endpoint locks) over every active revision with the candidate
  standing in for its key: every delegated key exists and is live
  (`request_approval_pipeline_inactive`), no cycle
  (`request_approval_pipeline_delegation_cycle`), depth at most
  `domain.MaximumDelegationDepth` (8,
  `request_approval_pipeline_delegation_too_deep`), and the flattened size
  from `entry` at most `domain.MaximumEffectivePipelineBlocks` (10,000,
  `request_approval_pipeline_too_large`). A delegate may never name
  `entry` or its own pipeline. Revision creation and preview require every
  delegated key to exist (`request_approval_pipeline_not_found` in the
  400's details). At run time the same rules are re-checked defensively;
  a violation is `compiled_cache_unavailable`, human on the entry default.
- **Archive** (`DELETE /request-approval-pipelines/{key}`,
  `pipeline.activate` at the key): refused for `entry` and while any
  active revision delegates to the key (`request_approval_pipeline_in_use`
  with `uses`); else stamps every revision, deactivates the active one,
  removes API-key role assignments at `pipeline:<key>`, and audits
  `request_approval_pipeline.archived`. Archived keys stay readable
  (`status=archived|all`), refuse revisions and activation, and are never
  reused. There is no restore.
- **Ownership** is an ordinary role assignment or permission policy at
  the scope kind `pipeline` (id the key). `pipeline.read/write/activate`
  are resource `pipeline`. Creating a pipeline needs organization-scope
  `pipeline.write`. The delegate picker lists the pipelines the author
  may read; revision creation checks existence only, like escalation
  paths.
- **Agents** have no pipeline. Requests keep `agent_slug` and
  `agent.slug` stays a condition field, so "rules for one agent" is a
  branch or a delegate on `/agent/slug`. Archiving and restoring an agent
  touch no pipeline.

## Storage

Migration `0004_pipeline_trees.sql` rewrote the tables and, because the
scope model changed shape, deleted every pipeline revision, block and
request pin (the deployments were recreated): `request_approval_pipelines`
gained `pipeline_key` and `name` in place of `agent_slug`, the trigger's
advisory key became `hashtextextended(org || ':' || pipeline_key, 0)`, the
suspension table lost its two revision columns in favour of the pins
child table, the scope-kind checks and the built-in `admin` role's
bindable kinds gained `pipeline`, and one active empty `entry` revision
was seeded per organization. The store is keyed by `pipeline_key`
throughout (`internal/postgres/request_approval_pipelines.go`); uses of
a path, an endpoint, or a pipeline all report `pipeline_key`.

## Execution

`internal/approvalpipeline/effective.go` splices the cached per-revision
compilations into one `effectivePipeline` (sections, rebased blocks,
owner per position). The executor (`execution.go`) is one cursor loop
over the flat list: containers (branch, delegate) evaluate their
condition and enter or skip; `stepBranchEnd` on a branch ends it with the
branch's reason and path, on a delegate ends the run as described above.
Evidence: `started` carries `entry_pipeline_id` and the `pipelines`
list; each `evaluated` event carries `pipeline_key`, `pipeline_id` and
`revision` (and `target_pipeline_*` on a delegate); `suspended` carries
`suspended_block_pipeline_key`; `completed` carries
`terminal_block_pipeline_key`. Webhook deliveries are keyed
`requestID:pipelineKey:blockKey`, since `entry` and a target may both hold
`fraud-check`. Preview (`/request-approval-pipelines/{key}/preview`) runs
the same walk over the document plus the active revisions of its
delegates; every result block carries `pipeline_key`, and
`escalation_path_pipeline_key` says whose default routed a human outcome.

## API

`GET/POST /api/v1/request-approval-pipelines`, then per key: `GET`,
`DELETE` (archive), `/uses`, `/preview`, `/revisions`,
`/revisions/{n}`, `/revisions/{n}/references` (now with `pipelines`),
`/revisions/{n}/activate` with `If-Match`. Summaries and revisions carry
`pipeline_key` and `name`. The MCP tools take `pipeline_key` everywhere,
plus `create_pipeline` and `archive_pipeline`.

## Editor

The pipelines overview shows the entry pipeline and a table of the
others (`/pipelines/new` creates one). A pipeline page draws one rail,
its own. A delegate block is one card and one exit: if its conditions are
met, continue in the named pipeline; otherwise check the next block. The
target's blocks are never inlined: they belong to its owners and live on
its own page, one click away. A test replay previews the whole effective
pipeline, then folds every step past the delegate it entered (the target's
blocks, pipelines it delegates to in turn, and the delegate's own end)
onto that delegate, whose exit carries what the target concluded.
(Amended 2026-09-29: the first cut drew the target's active blocks read
only in a well under the delegate, which made a delegate read like an
inline branch and put another team's rules on this team's page.)
The guided setup writes one inline branch per runtime into `entry`
(block id the runtime key) rather than per-agent pipelines; see
[09-onboarding.md](09-onboarding.md).

## Blocks after the redesign

Amended 2026-09-29. With branches and delegates in place, the standalone
conditions block (`cel`) was only a branch holding one decision, so it is
gone. Core's block types are four:

- `outcome` (renamed from `always`): decides every request that reaches
  it. On its own rail it is the catch-all; inside a branch it is the
  "then" of "if this, then that".
- `branch`: runs its blocks for requests its condition matches. Its
  `reason` is optional, since a branch whose blocks end in an outcome
  block never runs out; the editor draws no end for a branch nothing can
  reach, so that shape carries no warning.
- `delegate`: continues matching requests in another pipeline.
- `webhook`: asks a service of the organization's.

The condition engine (cel-go) is unchanged and evaluates every branch and
delegate condition under one per-request budget
(`approvalblock.MaximumConditionsPerRequest`); its version is part of
both runtimes' fingerprints. The guided setup's gate is a branch on
`/request/tool` holding an outcome block that asks the person. Migration
`0006_outcome_blocks.sql` wipes stored pipelines and reseeds an empty
`entry`, as `0005` did.
