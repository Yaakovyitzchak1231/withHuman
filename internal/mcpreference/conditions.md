# Conditions

`cel` and `branch` blocks (`config.when`) and escalation path `request`
conditions (`when`) share one condition tree. Every node has an `id` that is a UUID
you generate, unique across the whole pipeline revision or path document,
and exactly one shape:

- Leaf: `{ "id", "field", "operator", "value"? }`
- All of: `{ "id", "all": [ ...conditions ] }`
- Any of: `{ "id", "any": [ ...conditions ] }`
- Some element: `{ "id", "some": { "field": "/request/arguments/items", "where": <condition> } }`
- Every element: `{ "id", "every": { "field": "...", "where": <condition> } }`

Inside `some` and `every`, `field` names an array and `where` is evaluated
against each element with `field` paths relative to the element (for
example `/price`).

Limits: depth 12, 128 nodes, regex 512 bytes.

## Fields

Fields are JSON pointers into the request:

- `/request/tool`: the tool name (a string): the runtime's own name for a
  built-in tool (`Bash`, `Edit`, `Write` for coding agents), or the name
  an MCP server gives its tool.
- `/request/server`: the MCP server that defines the tool (a string), by
  the alias the runtime configured; gateway tools carry the gateway's
  server slug. Built-in tools have no server, so only `not_exists`
  matches them.
- `/request/arguments/...`: the tool call's arguments, any depth.
- `/agent/slug`, `/agent/name`: the requesting agent (strings).
- `/previous_metadata/<pipeline key>/<block id>/...`: metadata an
  earlier block returned (a `webhook`, or a kind the deployment adds),
  under the key of the pipeline that holds it (`entry`, or a delegated
  pipeline's).

## Operators

Without a value: `exists`, `not_exists`, `is_null`, `is_not_null`,
`is_true`, `is_false`, `is_empty`, `is_not_empty`.

With a value:

- `equals`, `not_equals`: any JSON value (a string for string fields).
- `in`, `not_in`: an array of strings.
- `contains`, `not_contains`: a string (substring of a string field, or
  element of an array field).
- `starts_with`: an array of strings (any prefix matches).
- `ends_with`: a string.
- `regex`: a string, RE2 syntax.
- `greater_than`, `greater_than_or_equal`, `less_than`,
  `less_than_or_equal`: a JSON number.
- `contains_any`, `contains_all`: a non-empty array.

A missing field does not match, except for `not_exists`. On the known
string fields (`/request/tool`, `/request/server`, `/agent/slug`,
`/agent/name`) only the string operators are accepted; the numeric
comparisons and `is_true`, `is_false`, `is_empty`, `is_not_empty` are
refused.

## Examples

Shell commands that touch git history:

```json
{ "id": "<uuid>", "all": [
  { "id": "<uuid>", "field": "/request/tool", "operator": "equals", "value": "Bash" },
  { "id": "<uuid>", "field": "/request/arguments/command", "operator": "regex", "value": "git\\s+(push|reset|rebase)" } ] }
```

Any Stripe tool call for a flagged customer tier:

```json
{ "id": "<uuid>", "all": [
  { "id": "<uuid>", "field": "/request/server", "operator": "equals", "value": "stripe" },
  { "id": "<uuid>", "field": "/request/arguments/customer/tier", "operator": "in", "value": ["enterprise", "strategic"] } ] }
```
