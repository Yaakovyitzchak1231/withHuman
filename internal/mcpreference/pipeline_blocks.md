# Pipeline blocks

A pipeline revision is `name`, `blocks` (an ordered list) plus an
optional `default_escalation_path`. Blocks run in order. The first block
whose outcome is `approve`, `deny`, or `human` ends the pipeline; a block
whose outcome is `next` passes the request on. A `branch` block holds
blocks of its own that run only for the requests its condition matches
(see `branch`), so "if this, then that" is a branch whose blocks end in
an `outcome` block; a `delegate` block hands the requests its condition
matches to another pipeline, named by key (see `delegate`). A request no
block decides falls through to human review: the revision's
`default_escalation_path` names who, else the delegating pipeline's
default, outward to the entry pipeline, else any authorized reviewer.

Every organization has one entry pipeline, key `entry`, which every
request runs. Other pipelines are created with `create_pipeline` (a key
matching `^[a-z0-9][a-z0-9._-]{0,62}$`, never reused, never `entry`) and
reached only through delegate blocks; each is owned through grants at
`pipeline:<key>`, so one team can route (the delegate's condition in the
entry pipeline) while another owns what happens after the match.

Unknown fields are refused everywhere. Every block has this envelope:

```json
{
  "id": "everything-else",
  "name": "Approve everything else",
  "type": "outcome",
  "enabled": true,
  "timeout": "50ms",
  "max_attempts": 1,
  "config": { ... }
}
```

- `id`: key-shaped (`^[a-z0-9][a-z0-9._-]{0,62}$`), unique within the
  revision, blocks inside branches included. Metadata a block returns is
  readable by later blocks at
  `/previous_metadata/<pipeline key>/<id>/...`, the pipeline key being
  the one that holds the block (`entry`, or a delegated pipeline's).
- `name`: shown to reviewers and in the audit log.
- `type`: `outcome`, `branch`, `delegate`, or `webhook`, plus any
  kind this deployment adds (listed after the core grammar below).
- `enabled`: must be `true` (omit or set true).
- `timeout` and `max_attempts`: per type. `outcome`, `branch`, and
  `delegate`: timeout up to `1s` (use `50ms`), exactly 1 attempt.
  `webhook`: default `5s`, up to `30s`, 1 to 3 attempts. An added kind
  states its own.
- `outcome` and `webhook` configs carry a `reason`: the sentence
  reviewers and the audit log see for the block's decision. A branch's
  `reason` is optional (see `branch`); a delegate has none.
- A config may carry `escalation_path` (a path key) only where the block
  can hand the request to a person; it overrides the revision's
  `default_escalation_path` for that block.
- `ask_agent_owner: true` sends the request to the person who owns the
  agent instance that made it. `escalation_path` beside it, if set, is
  the fallback, used only when the request has no owner able to decide
  (a service agent, or an owner who was deactivated); without one the
  fallback is the pipeline default, else any authorized reviewer. The
  owner is the only reviewer: the request never moves to the fallback
  because the owner is slow. The revision's default takes the same pair
  as `default_ask_agent_owner` with an optional `default_escalation_path`.
  A block that names its own path without the flag does not ask the
  owner, even when the default does.

## outcome

Decides every request that reaches it. On its own rail it is the
catch-all at the end; inside a branch it is the "then" of "if this, then
that".

```json
{ "outcome": "approve", "reason": "Everything else is routine." }
```

`outcome` is `approve`, `deny`, or `human`. `escalation_path` is allowed
only with `human`.

## webhook

Posts a signed request to one of the organization's webhook endpoints
(create one with `save_webhook_endpoint`) and maps its answer to an
outcome. Any delivery failure or answer outside `allowed_outcomes` is
`human`. Available only when the deployment holds a secrets key
(`webhook_unconfigured` otherwise).

```json
{
  "endpoint_key": "fraud-service",
  "allowed_outcomes": ["next", "human", "approve", "deny"],
  "include": { "agent_reasoning": false, "context": false, "previous_metadata": true },
  "reason": "The fraud service scored this refund.",
  "escalation_path": "payments-review"
}
```

`allowed_outcomes` is a non-empty set from `next`, `human`, `approve`,
`deny`; `approve` and `deny` must be listed explicitly for the endpoint to
decide automatically.

## branch

Runs its own ordered `blocks` for the requests its condition matches (see
the `conditions` topic for the tree; every condition `id` is a UUID,
unique across the whole revision); every other
request skips the branch, so a `webhook` (or any asynchronous kind) inside is never
called for it, and passes to the next block.

A request that enters a branch is decided inside it. Its blocks follow the
usual rules: `approve`, `deny`, or `human` ends the pipeline, `next` moves
to the branch's next block. If none of them decides, the request goes to a
person with the branch's `reason` and `escalation_path`, and nothing after
the branch runs for it. A condition that cannot be evaluated sends the
request to a person on the pipeline default.

```json
{
  "when": { "id": "<uuid>", "field": "/request/tool", "operator": "equals", "value": "stripe__refund" },
  "blocks": [ { ...any block, including another branch... } ],
  "reason": "Refunds the checks did not settle go to Payments.",
  "escalation_path": "payments-review"
}
```

- `blocks`: at least one. Branches nest without a depth limit, and every
  block inside counts toward the revision's 1,000 blocks.
- `escalation_path`: where a request that runs out of the branch goes. A
  block inside the branch that names no path of its own uses the
  revision's `default_escalation_path`, never the branch's.
- `reason`: optional, recorded only when a request runs out of the
  branch. A branch whose blocks end in an `outcome` block never runs out,
  so leave its `reason` and `escalation_path` out.
- An `outcome` block inside a branch ends every request that reaches it,
  so the branch's later blocks and its own end are never reached.

"If this, then that" is a branch holding one `outcome` block:

```json
{ "id": "big-refunds", "name": "Deny refunds over $500", "type": "branch", "enabled": true, "timeout": "50ms", "max_attempts": 1,
  "config": {
    "when": { "id": "<uuid>", "all": [
      { "id": "<uuid>", "field": "/request/tool", "operator": "equals", "value": "stripe__refund" },
      { "id": "<uuid>", "field": "/request/arguments/amount", "operator": "greater_than", "value": 500 } ] },
    "blocks": [
      { "id": "deny-big-refunds", "name": "Deny", "type": "outcome", "enabled": true, "timeout": "50ms", "max_attempts": 1,
        "config": { "outcome": "deny", "reason": "Refunds over $500 are never automatic." } } ] } }
```

## delegate

Hands the requests its condition matches (the same condition tree as a
branch) to another pipeline: matching requests continue in that
pipeline's active revision, and every other request skips the block and
passes to the next one. It is a branch whose body is another pipeline,
owned by whoever holds `pipeline.write` at `pipeline:<key>`.

A request that enters the target is decided inside it: `approve`,
`deny`, or `human` ends the pipeline, `next` moves to the target's next
block. If the target runs out of blocks, the request goes to a person on
the target's `default_escalation_path`, else the delegating pipeline's,
and nothing after the delegate runs for it. A condition that cannot be
evaluated sends the request to a person on the delegating pipeline's
default.

```json
{
  "when": { "id": "<uuid>", "field": "/request/server", "operator": "equals", "value": "stripe" },
  "pipeline_key": "payments"
}
```

- `pipeline_key`: a live pipeline other than `entry` and other than the
  pipeline holding the block. Saving a revision requires the key to
  exist; activating requires it to be active, refuses a cycle
  (`request_approval_pipeline_delegation_cycle`), a chain deeper than 8
  (`request_approval_pipeline_delegation_too_deep`), and an entry
  pipeline whose delegations together exceed 10,000 blocks
  (`request_approval_pipeline_too_large`).
- No `reason` and no `escalation_path`: the target pipeline owns its end.
- Archiving a pipeline (`archive_pipeline`) is refused while an active
  revision delegates to it (`request_approval_pipeline_in_use`).

## Worked example

Deny refunds over $500 for every agent in the entry pipeline, and hand
every Stripe call to a `payments` pipeline the Payments team owns, which
approves refunds up to $20 and asks Payments about the rest:

Entry pipeline (`update_pipeline` with `pipeline_key: "entry"`):

```json
{
  "blocks": [
    { "id": "big-refunds", "name": "Deny refunds over $500", "type": "branch", "enabled": true, "timeout": "50ms", "max_attempts": 1,
      "config": { "when": { "id": "<uuid>", "all": [
        { "id": "<uuid>", "field": "/request/tool", "operator": "equals", "value": "stripe__refund" },
        { "id": "<uuid>", "field": "/request/arguments/amount", "operator": "greater_than", "value": 500 } ] },
        "blocks": [
          { "id": "deny-big-refunds", "name": "Deny", "type": "outcome", "enabled": true, "timeout": "50ms", "max_attempts": 1,
            "config": { "outcome": "deny", "reason": "Refunds over $500 are never automatic." } } ] } }
  ]
}
```

Add the delegate after `big-refunds` in the entry pipeline:

```json
{ "id": "stripe", "name": "Stripe calls go to Payments", "type": "delegate", "enabled": true, "timeout": "50ms", "max_attempts": 1,
  "config": { "when": { "id": "<uuid>", "field": "/request/server", "operator": "equals", "value": "stripe" },
    "pipeline_key": "payments" } }
```

The `payments` pipeline (`create_pipeline` with `pipeline_key: "payments"`, then `update_pipeline`):

```json
{
  "name": "Payments",
  "blocks": [
    { "id": "refunds", "name": "Refunds", "type": "branch", "enabled": true, "timeout": "50ms", "max_attempts": 1,
      "config": { "when": { "id": "<uuid>", "field": "/request/tool", "operator": "equals", "value": "stripe__refund" },
        "blocks": [
          { "id": "small-refunds", "name": "Approve refunds up to $20", "type": "branch", "enabled": true, "timeout": "50ms", "max_attempts": 1,
            "config": { "when": { "id": "<uuid>", "field": "/request/arguments/amount", "operator": "less_than_or_equal", "value": 20 },
              "blocks": [
                { "id": "approve-small-refunds", "name": "Approve", "type": "outcome", "enabled": true, "timeout": "50ms", "max_attempts": 1,
                  "config": { "outcome": "approve", "reason": "Small refunds are approved automatically." } } ] } },
          { "id": "other-refunds", "name": "Payments reviews larger refunds", "type": "outcome", "enabled": true, "timeout": "50ms", "max_attempts": 1,
            "config": { "outcome": "human", "reason": "A person checks refunds over $20.", "escalation_path": "payments-review" } } ] } }
  ],
  "default_escalation_path": "on-call"
}
```

A Stripe call the `payments` blocks do not decide falls through to a
person on the `on-call` path, the pipeline's own default; every other
tool call falls through the entry pipeline to its default reviewers. Add
a final `outcome` block with `outcome: approve` to the entry pipeline to
approve the rest automatically.

## Worked example with a branch

Only for refunds: ask the fraud service, which may approve or deny outright
or pass the refund on; then approve what is left under $10 and ask
Payments about the rest. Every other tool call skips the branch and
reaches the final `outcome` block:

```json
{
  "blocks": [
    { "id": "refunds", "name": "Refunds", "type": "branch", "enabled": true, "timeout": "50ms", "max_attempts": 1,
      "config": {
        "when": { "id": "<uuid>", "field": "/request/tool", "operator": "equals", "value": "stripe__refund" },
        "blocks": [
          { "id": "fraud-check", "name": "Ask the fraud service", "type": "webhook", "enabled": true, "timeout": "5s", "max_attempts": 3,
            "config": { "endpoint_key": "fraud-service", "allowed_outcomes": ["next", "approve", "deny"],
              "include": { "agent_reasoning": false, "context": false, "previous_metadata": true },
              "reason": "The fraud service decided this refund." } },
          { "id": "small-refunds", "name": "Approve refunds under $10", "type": "branch", "enabled": true, "timeout": "50ms", "max_attempts": 1,
            "config": { "when": { "id": "<uuid>", "field": "/request/arguments/amount", "operator": "less_than", "value": 1000 },
              "blocks": [
                { "id": "approve-small-refunds", "name": "Approve", "type": "outcome", "enabled": true, "timeout": "50ms", "max_attempts": 1,
                  "config": { "outcome": "approve", "reason": "Small refunds the fraud service passed on are approved automatically." } } ] } }
        ],
        "reason": "Refunds the checks did not settle go to Payments.",
        "escalation_path": "payments-review"
      } },
    { "id": "everything-else", "name": "Approve everything else", "type": "outcome", "enabled": true, "timeout": "50ms", "max_attempts": 1,
      "config": { "outcome": "approve", "reason": "Other tool calls are routine." } }
  ]
}
```
