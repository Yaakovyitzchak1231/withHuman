# Escalation path documents

An escalation path says who reviews a request once a pipeline hands it
to a person, in what order, and what happens when nobody answers. A
pipeline block names a path by key (`escalation_path`), and a pipeline
revision names one for fall-through (`default_escalation_path`). A path
can be shared by many pipelines. Beside `ask_agent_owner` (or
`default_ask_agent_owner`) a path is only the fallback for requests
whose agent has no owner able to decide; the owner reviews the rest. Who is notified and who may decide are
the same set: every target on the branches the request reaches.

A document is `nodes` (an ordered list), `working_hours` (named sets the
nodes refer to; pass `[]` when none), and an optional `repeat`. Unknown
fields are refused. Every node `id` is a UUID you generate, unique in the
document.

## Level nodes

A level notifies its targets and waits `escalate_after` (a duration from
`1m` to `168h`) before the next node runs. A decision or a reviewer's
claim stops the clock.

```json
{ "id": "<uuid>", "type": "level",
  "targets": [ { "type": "user", "id": "<membership id>" }, { "type": "team", "id": "<team id>" } ],
  "escalate_after": "15m",
  "raise_urgency": false }
```

- `targets`: 1 to 25, no repeats. `user` targets carry a membership id
  from `list_members` (never a user id); `team` targets carry a team id
  from `list_teams`. A team target reaches every current member, or runs
  the team's own escalation policy when it has one.
- `raise_urgency: true` raises the request to `interrupt` urgency when the
  level is entered, so people's interrupt notification ladders apply.

## if_else nodes

Branches on conditions; every condition must hold for `then`, otherwise
`else` runs. Either branch may be empty. Branches nest up to depth 4.

```json
{ "id": "<uuid>", "type": "if_else",
  "conditions": [ { "type": "urgency", "urgency": "interrupt" } ],
  "then": [ ...nodes ],
  "else": [ ...nodes ] }
```

Condition kinds (1 to 10 per node, each exactly one kind):

- `{ "type": "working_hours", "working_hours_id": "<set id>", "active": true }`
  tests whether the named window is open (`active: true`) or closed.
- `{ "type": "urgency", "urgency": "standard" | "interrupt" }` tests the
  request's current urgency.
- `{ "type": "request", "when": <condition tree> }` tests the request
  itself with the same tree branch conditions use (see the `conditions`
  topic); evaluated once when the request is routed.

## defer nodes

Holds the request until the named working-hours window is open, then
continues.

```json
{ "id": "<uuid>", "type": "defer", "working_hours_id": "business-hours", "standard_only": true }
```

`standard_only: true` lets interrupt-urgency requests skip the wait.

## Working hours

```json
{ "id": "business-hours", "name": "Business hours", "timezone": "Europe/London",
  "intervals": [ { "weekdays": ["mon", "tue", "wed", "thu", "fri"], "start": "09:00", "end": "17:30" } ] }
```

Ids are key-shaped; `timezone` is an IANA name; up to 21 intervals per
set, weekdays `mon` to `sun`, times `HH:MM` with start before end.

## Repeat

When the last node has run and nobody answered:

```json
"repeat": { "times": 2, "after": "30m" }
```

walks the nodes again up to `times` (1 to 9) more times, waiting `after`
(`1m` to `168h`) first. Without `repeat` the walk ends and the request
waits for any of its targets until it expires.

## Worked example

Payments review: page the payments team, then the finance lead after 15
minutes, treat large refunds as interrupts, and hold overnight requests
until the morning unless they are urgent.

```json
{
  "working_hours": [
    { "id": "office", "name": "Office hours", "timezone": "America/New_York",
      "intervals": [ { "weekdays": ["mon", "tue", "wed", "thu", "fri"], "start": "09:00", "end": "18:00" } ] }
  ],
  "nodes": [
    { "id": "<uuid>", "type": "if_else",
      "conditions": [ { "type": "request", "when": { "id": "<uuid>", "field": "/request/arguments/amount", "operator": "greater_than", "value": 250 } } ],
      "then": [
        { "id": "<uuid>", "type": "level", "targets": [ { "type": "team", "id": "<payments team id>" } ], "escalate_after": "10m", "raise_urgency": true }
      ],
      "else": [
        { "id": "<uuid>", "type": "defer", "working_hours_id": "office", "standard_only": true },
        { "id": "<uuid>", "type": "level", "targets": [ { "type": "team", "id": "<payments team id>" } ], "escalate_after": "15m" }
      ] },
    { "id": "<uuid>", "type": "level", "targets": [ { "type": "user", "id": "<finance lead membership id>" } ], "escalate_after": "1h" }
  ],
  "repeat": { "times": 1, "after": "30m" }
}
```

The revision's `warnings` report a targeted team whose members cannot
decide requests (`team_members_cannot_decide`) or a layout some request
could walk without meeting a level (`path_may_reach_nobody`). Warnings do
not block activation.
