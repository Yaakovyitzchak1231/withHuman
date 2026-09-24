# Pipeline blocks

A pipeline revision is `blocks` (an ordered list) plus an optional
`default_escalation_path`. Blocks run in order. The first block whose
outcome is `approve`, `deny`, or `human` ends the pipeline; a block whose
outcome is `next` passes the request on. A `branch` block holds blocks of
its own that run only for the requests its condition matches (see
`branch`). A request no block decides falls through to human review: the
revision's `default_escalation_path` names who, or any authorized
reviewer when there is none. The organization's pipeline runs before the
agent's own; if the organization pipeline decides, the agent pipeline
never runs.

Unknown fields are refused everywhere. Every block has this envelope:

```json
{
  "id": "refunds-over-500",
  "name": "Deny refunds over $500",
  "type": "cel",
  "enabled": true,
  "timeout": "50ms",
  "max_attempts": 1,
  "config": { ... }
}
```

- `id`: key-shaped (`^[a-z0-9][a-z0-9._-]{0,62}$`), unique within the
  revision, blocks inside branches included. Metadata a block returns is
  readable by later blocks at
  `/previous_metadata/<scope>/<id>/...` where scope is `organization` or
  `agent`.
- `name`: shown to reviewers and in the audit log.
- `type`: `always`, `cel`, `webhook`, or `branch`, plus any kind this
  deployment adds (listed after the core grammar below).
- `enabled`: must be `true` (omit or set true).
- `timeout` and `max_attempts`: per type. `always`, `cel`, and `branch`:
  timeout up to `1s` (use `50ms`), exactly 1 attempt. `webhook`: default
  `5s`, up to `30s`, 1 to 3 attempts. An added kind states its own.
- Every config carries a `reason`: the sentence reviewers and the audit
  log see for the block's decision.
- A config may carry `escalation_path` (a path key) only where the block
  can hand the request to a person; it overrides the revision's
  `default_escalation_path` for that block.

## always

Decides every request that reaches it. Put one at the end as the
catch-all when the rest should be automatic.

```json
{ "outcome": "approve", "reason": "Everything else is routine." }
```

`outcome` is `approve`, `deny`, or `human`. `escalation_path` is allowed
only with `human`.

## cel

Tests the request with a condition tree (see the `conditions` topic) and
decides when it matches; a request that does not match passes to the
next block.

```json
{
  "when": {
    "id": "6f1d2a4e-3c5b-4a7d-9e8f-0a1b2c3d4e5f",
    "all": [
      { "id": "1b2c3d4e-5f60-4718-8a9b-0c1d2e3f4a5b", "field": "/request/tool", "operator": "equals", "value": "stripe__refund" },
      { "id": "2c3d4e5f-6071-4829-9bac-1d2e3f4a5b6c", "field": "/request/arguments/amount", "operator": "greater_than", "value": 500 }
    ]
  },
  "on_match": "deny",
  "reason": "Refunds over $500 are never automatic."
}
```

`on_match` is `approve`, `deny`, or `human`; `escalation_path` requires
`on_match: human`. Every condition `id` must be a UUID, unique across the
whole revision.

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

Runs its own ordered `blocks` for the requests its condition matches (the
same condition tree as `cel`, see the `conditions` topic); every other
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
- An `always` block inside a branch ends every request that reaches it,
  so the branch's later blocks and its own end are never reached.

## Worked example

Deny refunds over $500 for every agent, let the support agent refund up to
$20 on its own, and send the rest to Payments review:

Organization pipeline:

```json
{
  "blocks": [
    { "id": "big-refunds", "name": "Deny refunds over $500", "type": "cel", "enabled": true, "timeout": "50ms", "max_attempts": 1,
      "config": { "when": { "id": "<uuid>", "all": [
        { "id": "<uuid>", "field": "/request/tool", "operator": "equals", "value": "stripe__refund" },
        { "id": "<uuid>", "field": "/request/arguments/amount", "operator": "greater_than", "value": 500 } ] },
        "on_match": "deny", "reason": "Refunds over $500 are never automatic." } }
  ]
}
```

Support agent pipeline:

```json
{
  "blocks": [
    { "id": "small-refunds", "name": "Approve refunds up to $20", "type": "cel", "enabled": true, "timeout": "50ms", "max_attempts": 1,
      "config": { "when": { "id": "<uuid>", "all": [
        { "id": "<uuid>", "field": "/request/tool", "operator": "equals", "value": "stripe__refund" },
        { "id": "<uuid>", "field": "/request/arguments/amount", "operator": "less_than_or_equal", "value": 20 } ] },
        "on_match": "approve", "reason": "Small refunds are approved automatically." } },
    { "id": "other-refunds", "name": "Payments reviews larger refunds", "type": "cel", "enabled": true, "timeout": "50ms", "max_attempts": 1,
      "config": { "when": { "id": "<uuid>", "field": "/request/tool", "operator": "equals", "value": "stripe__refund" },
        "on_match": "human", "reason": "A person checks refunds over $20.", "escalation_path": "payments-review" } }
  ],
  "default_escalation_path": "on-call"
}
```

Every other tool call falls through to a person on the `on-call` path.
Add a final `always` block with `outcome: approve` to the agent pipeline
to approve the rest automatically.

## Worked example with a branch

Only for refunds: ask the fraud service, which may approve or deny outright
or pass the refund on; then approve what is left under $10 and ask
Payments about the rest. Every other tool call skips the branch and
reaches the final `always` block:

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
          { "id": "small-refunds", "name": "Approve refunds under $10", "type": "cel", "enabled": true, "timeout": "50ms", "max_attempts": 1,
            "config": { "when": { "id": "<uuid>", "field": "/request/arguments/amount", "operator": "less_than", "value": 1000 },
              "on_match": "approve", "reason": "Small refunds the fraud service passed on are approved automatically." } }
        ],
        "reason": "Refunds the checks did not settle go to Payments.",
        "escalation_path": "payments-review"
      } },
    { "id": "everything-else", "name": "Approve everything else", "type": "always", "enabled": true, "timeout": "50ms", "max_attempts": 1,
      "config": { "outcome": "approve", "reason": "Other tool calls are routine." } }
  ]
}
```
