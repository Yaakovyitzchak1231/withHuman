# withHuman: CEL request approval block

Status: accepted · Last updated: 2026-09-23

## Summary

withHuman uses the [Common Expression Language
(CEL)](https://github.com/cel-expr/cel-go) through `cel-go` for the first
`RequestApprovalBlock` type. CEL evaluates a withHuman-owned, Zapier-style
condition tree. It is the execution substrate, not the public authoring format
or the request approval pipeline itself.

A CEL block has one fixed `on_match` outcome: `approve`, `deny`, or `human`.
A match returns that outcome. A non-match returns `next`, which advances to the
next block in the ordered pipeline. An error returns `human`. There is no
cross-block precedence resolver and no CEL expression can select its own
outcome, next block, reviewer, or arbitrary side effect.

withHuman owns block storage, sequential execution, revision activation,
audit, and explanations. A compiled CEL program is process-local cache state,
never the source of truth, persisted product data, or AAP output. Storage and
execution are defined in
[request approval pipeline storage and APIs](05-request-approval-pipeline-storage.md).

The same condition envelope is the `when` of a `branch` block, which runs
its own blocks only for requests the condition matches
([branch blocks](15-branch-blocks.md)).

## Block input

The pipeline executor constructs this logical input for every block:

```json
{
  "request": {
    "tool": "stripe.refund",
    "server": "stripe",
    "arguments": {
      "amount_cents": 900,
      "customer": {
        "email": "buyer@example.com",
        "labels": ["trial", "priority"]
      }
    }
  },
  "agent": {
    "slug": "billing-agent",
    "name": "Billing agent"
  },
  "previous_metadata": {}
}
```

`request.tool`, `request.server`, and `request.arguments` come from the
canonical AAP request. `request.server` is the MCP server that defines the
tool, as the adapter observed it (the gateway reports its own server slug),
and is absent for a runtime's built-in tools.
Argument keys, nested objects, arrays, values, and nulls are preserved exactly.
The authenticated credential supplies the canonical agent's `agent.slug` and
`agent.name`; callers cannot set or override those fields.

v1 CEL blocks may match:

- `/request/tool`;
- `/request/server`;
- `/request/arguments` and its descendants;
- `/agent/slug`;
- `/agent/name`; and
- `/previous_metadata/<scope>/<block_key>` and its descendants, where the
  scope is `organization` or `agent`.

`/request/tool`, `/request/server`, `/agent/slug`, and `/agent/name` are known
strings, so validation refuses operators that cannot apply to a string. In v1
the validator rejects every other path, including `/request/agent_reasoning`,
`/request/context`, and `/request/timeout`. Agent reasoning remains an
unverified claim and adapter context has no approved trust model yet.

## Why CEL

- **Go-native.** `cel-go` runs inside the provider without a separate policy
  service or network hop.
- **Apache 2.0.** withHuman can distribute the embedded engine under a
  permissive license.
- **Side-effect-free.** An expression cannot modify data, call APIs, or write
  to storage.
- **Terminating.** CEL excludes unbounded loops and recursion; withHuman also
  applies explicit size and cost limits.
- **Gradually typed.** Stable request and agent fields can be typed while
  arbitrary tool arguments remain dynamic JSON.
- **Familiar syntax.** Generated expressions resemble
  `request.tool == "stripe.refund"` or
  `request.arguments.amount_cents < 1000`.
- **Designed for embedding.** CEL fits behind the request approval block model
  rather than becoming another control plane.
- **Checked before activation.** Invalid syntax and incompatible known types
  fail before a real approval request depends on the revision.

## Authoring model

The Go SDK and visual editor produce the same serializable block
configuration:

```text
Go block DSL ─────────┐
                     ├─> condition tree JSON ─> validation ─> CEL program
Visual block editor ──┘
```

The Go SDK builds data rather than accepting executable callbacks:

```go
b := approvalblock.Define(
	"small-priority-refund",
	approvalblock.Name("Approve small priority refunds"),
	approvalblock.OnMatch(approvalblock.Approve),
	approvalblock.When(approvalblock.All(
		approvalblock.Field("/request/tool").In(
			"stripe.refund",
			"stripe.partial_refund",
		),
		approvalblock.Field("/request/arguments/amount_cents").LessThan(1000),
		approvalblock.Field("/request/arguments/customer/email").Regex(
			`^[^@]+@example\.com$`,
		),
		approvalblock.Field("/request/arguments/customer/labels").Contains("priority"),
	)),
	approvalblock.Reason("Small priority refunds are pre-authorized."),
)

if err := b.Validate(); err != nil {
	return err
}
```

The builder can be validated locally, submitted as part of a complete pipeline
revision, rendered by the visual editor, and replayed during an audit. It
exposes the underlying structs and constants for mechanical generation. A callback
such as `When(func(Input) bool)` is unsupported because a closure cannot be
serialized, inspected, rendered, or reproduced centrally.

The visual editor follows the
[Zapier Filters](https://help.zapier.com/hc/en-us/articles/8496276332557-Add-conditions-to-Zaps-with-filters)
interaction model:

1. Select a field, such as `Request > Arguments > Customer > Email`.
2. Select an operator, such as `matches regex`, `is in`, or `less than`.
3. Enter the comparison value.
4. Combine conditions with AND or OR groups.
5. Preview the complete revision document against a transient sample before
   creating or activating it.

Preview inputs are never persisted as pipeline test cases.

## Canonical CEL block representation

```json
{
  "id": "small-priority-refund",
  "name": "Approve small priority refunds",
  "type": "cel",
  "enabled": true,
  "timeout": "50ms",
  "max_attempts": 1,
  "config": {
    "on_match": "approve",
    "when": {
      "id": "21aa670a-734c-454c-a1e9-653d54be054b",
      "all": [
        {
          "id": "5d79dc6a-5f7d-4e70-aeb1-dcd682e77322",
          "field": "/request/tool",
          "operator": "in",
          "value": ["stripe.refund", "stripe.partial_refund"]
        },
        {
          "id": "833f08e6-c7b4-47c6-927b-1d27a93cd957",
          "field": "/request/arguments/amount_cents",
          "operator": "less_than",
          "value": 1000
        },
        {
          "id": "38665bad-40e1-4c28-af4a-9f0bc7c4890c",
          "field": "/request/arguments/customer/email",
          "operator": "regex",
          "value": "^[^@]+@example\\.com$"
        }
      ]
    },
    "reason": "Small priority refunds are pre-authorized."
  }
}
```

Allowed `on_match` values are `approve`, `deny`, and `human`. Non-match always
means `next`. The block `id` is stable across pipeline versions, while each
stored block snapshot has its own UUID. Every condition node also has a stable
UUID `id`, unique across the complete pipeline version, including the `when`
trees of `branch` blocks and of every block nested inside a branch, and
preserved through read-edit-write round trips.

## Field paths and condition tree

Fields use [JSON Pointer](https://www.rfc-editor.org/rfc/rfc6901). For example,
`/request/tool` selects the tool name and
`/request/arguments/customer/email` selects a nested argument. The editor
shows readable breadcrumbs and may learn candidate paths from samples or a
future tool schema. Manual paths remain available because samples are not
complete schemas.

A condition node has an `id` and exactly one of:

- a leaf with `field`, `operator`, and an optional `value`;
- `{ "all": [...] }`, requiring every child to match;
- `{ "any": [...] }`, requiring at least one child to match;
- `{ "some": { "field": "...", "where": {...} } }`, requiring a
  matching element in a non-empty array; or
- `{ "every": { "field": "...", "where": {...} } }`, requiring every
  element in a non-empty array to match.

Inside `some` or `every`, paths are relative to the current array element. The
empty path `""` selects the element itself. Both predicates return false for
an empty array, so emptiness must be handled explicitly.

## Operators

| Type | Operators |
| --- | --- |
| Any JSON value | `exists`, `not_exists`, `equals`, `not_equals`, `is_null`, `is_not_null` |
| String | `in`, `not_in`, `contains`, `not_contains`, `contains_any`, `contains_all`, `starts_with`, `ends_with`, `regex` |
| Number | `greater_than`, `greater_than_or_equal`, `less_than`, `less_than_or_equal` |
| Boolean | `is_true`, `is_false` |
| Array | `contains`, `not_contains`, `contains_any`, `contains_all`, `is_empty`, `is_not_empty` |

`in` and `not_in` compare one scalar field with a configured list. `contains`
means substring containment for strings and element membership for arrays;
`contains_any` and `contains_all` apply the same reading to a configured
list, so `command contains_any ["sudo", "rm -rf"]` matches a shell command
holding either substring. `starts_with` also takes a list: `tool starts_with
["stripe.", "paypal."]` matches a tool name beginning with any listed prefix,
while `ends_with` compares one suffix.
Regular expressions use RE2-compatible syntax and have explicit size and cost
limits. Equality uses JSON types: number `10`, string `"10"`, and boolean
`true` are distinct. v1 performs no implicit coercion.

## Missing data and errors

Missing and `null` are different. `exists` is true when a path is present even
if its value is `null`; `is_null` checks the value. `not_exists` is the only
leaf operator that matches a missing path. Every other operator, including
`not_in` and `not_equals`, returns false for missing data. A built-in tool has
no `request.server`, so `not_exists` is how a condition names built-in tools,
and a `not_equals` test on the server never matches them.

Wrong runtime types, cost exhaustion, compilation failure, or generated-program
failures are block errors. They record bounded safe evidence and return
`human`. They never return `next`, because silently skipping a broken block
could bypass a safety check.

## Compilation and evaluation

Revision creation validates the block envelope, IDs, field roots, tree,
operators, comparison values, and known types. It parses and checks the
generated CEL, then constructs an executable `cel.Program` in memory. Neither
generated source, checked-expression bytes, nor executable programs are
persisted.

Server replicas cache programs under the pipeline revision and a runtime
fingerprint covering `cel-go`, declarations, helper semantics, and compiler
options. Each server compiles every active revision before opening readiness,
and compiles a target before activation or rollback. A request-path cache miss
may perform one bounded singleflight compilation while handling the first
request for a revision activated through another replica. A new runtime
fingerprint always uses distinct cache entries, so rolling deployments never
reuse programs compiled under different declarations, helpers, or limits.
Failure, timeout, unavailable compiled state, or unsupported configuration
returns `human`. Temporal workers compile the same way when they resume a
pipeline suspended before an asynchronous block. The lifecycle
is defined in
[request approval pipeline storage and APIs](05-request-approval-pipeline-storage.md#in-memory-compiled-state).

At runtime, the pipeline executor invokes a CEL block only when sequential
execution reaches its position. The compiler resolves allowlisted JSON Pointer
paths into typed presence and value bindings. A generated CEL leaf program sees
only those bindings and its configured comparison value. Earlier blocks'
metadata is exposed the same way, under
`/previous_metadata/<scope>/<block_key>/...`, so a condition can threshold
what a webhook or judge returned. The full request, adapter context, agent
reasoning, and timeout never enter the CEL activation. The Go-owned condition envelope
combines leaf results and implements bounded `some` and `every` traversal in
document order. The engine records value-free outcomes against stable node and
block IDs.

A boolean match returns the configured `on_match` result. A boolean non-match
returns `next`. An error returns `human` and stops the pipeline. The executor,
not CEL, applies these transitions and persists the ordered evidence.

A `branch` block's `when` compiles and evaluates exactly like a CEL block's
condition: the same allowlisted fields, limits, value-free trace, 50 ms
default and one-second maximum timeout, and exactly one attempt. It also
draws on the same budget: CEL blocks and branch conditions together perform
at most 1,000 condition evaluations in one run segment. Only the
transitions differ. A match returns the evidence-only outcome `enter` and
runs the branch's blocks, a non-match returns `next` and skips them, and an
error returns `human`, routed to the pipeline default and never treated as
a skip. See [branch blocks](15-branch-blocks.md#semantics).

A CEL block never executes or modifies the proposed call.

## Performance and resource controls

The implementation must:

- compile once per server replica and reuse immutable programs;
- cache complete ordered pipeline versions rather than load blocks separately;
- limit request size, condition depth, expression size, comprehensions, regex
  size, evaluation cost, and total condition evaluations (CEL blocks and
  branch conditions) per run segment;
- stop loading later executable state after a terminal outcome when practical;
  and
- benchmark exact, `in`, regex, nested-object, and array conditions across
  pipelines of 1, 10, 100, and 1,000 CEL blocks.

Benchmarks record pipeline and per-block latency percentiles, CPU, allocations,
CEL cost, cache memory, and cold compilation time. They include early
terminal outcomes and complete fall-through to human review.

The v1 runtime limits are fixed and included in the runtime fingerprint:

| Resource | Limit |
| --- | ---: |
| Canonical trusted request input | 64 KiB |
| Canonical block document | 64 KiB |
| Condition depth | 12 nodes |
| Condition nodes per block | 128 |
| Aggregate generated CEL source per block | 4 KiB |
| Regex source | 512 bytes |
| `some` and `every` entries per block evaluation | 1,000 |
| CEL plus envelope evaluation cost per block | 10,000 |
| Condition evaluations per run segment (CEL blocks and branch conditions) | 1,000 |

CEL blocks have a 50 ms default timeout, a one-second maximum timeout, and
exactly one attempt. Input or limit violations, context cancellation, CEL
errors, non-boolean or otherwise ambiguous results, and internal panics all
produce `human` with a bounded safe error code. Raw runtime errors and request
values are not returned as evidence.

## Tradeoffs and mitigations

- **CEL is one block runtime, not a pipeline system.** withHuman owns ordered
  execution, outcomes, storage, versioning, audit, retries, and handoff.
- **Dynamic arguments weaken compile-time checking.** Runtime guards and
  fail-to-human behavior protect approval. Transient previews and future tool
  schemas catch more authoring mistakes.
- **CEL does not produce business explanations.** The configured block reason
  explains a match; value-free node traces explain evaluation mechanics.
- **Schema drift can break blocks.** Changed payload shapes stop at human
  review and raise an operational warning with safe affected-path references.
- **Advanced joins and external data are not CEL's strength.** Required
  provider-owned values must be prepared before evaluation. CEL performs no
  network or database access.
- **Custom functions reduce portability.** Extensions remain a small,
  versioned set of pure helpers included in the runtime fingerprint.
- **Safe execution does not guarantee correct intent.** Transient preview,
  change review, audit, and future shadow evaluation remain necessary.

## Alternatives considered

| Candidate | Strengths | Why not selected |
| --- | --- | --- |
| **[OPA/Rego](https://www.openpolicyagent.org/docs)** | General policy language over JSON, embedded Go or service deployment, structured outputs, reusable modules, rule indexing, bundles, status, and decision logs. | More language and runtime than one bounded deterministic block needs. Dynamic input, missing-data semantics, visual translation, pipeline transitions, and audit still remain withHuman responsibilities. |
| **[Cedar](https://docs.cedarpolicy.com/)** | Analyzable authorization language with schemas and clear `permit` and `forbid` rules. | Its principal-action-resource model does not naturally represent arbitrary tool payload filters or the pipeline's `next` and `human` transitions. |
| **[Cerbos PDP](https://docs.cerbos.dev/cerbos/latest/policies/)** | YAML policies, CEL conditions, scoped versions, rule outputs, and authorization APIs. | Introduces a second decision service while still requiring withHuman's pipeline, block, versioning, and evidence model. |
| **[Apache Casbin](https://casbin.org/)** | Lightweight library for ACL, RBAC, and ABAC. | Optimized for subject-object-action checks rather than arbitrary nested payload classification and node explanations. |
| **[OpenFGA](https://openfga.dev/docs/concepts)** | Strong relationship-based authorization for users, groups, and objects. | Useful later for reviewer eligibility, but not for numeric thresholds or arbitrary tool arguments. |

Reconsider Rego if customers require importing existing Rego, sharing bundles
with other OPA systems, or writing blocks with substantial joins and reusable
modules.

## Deferred capabilities

- Comparing one request field with another request field.
- Conditions over adapter context, agent reasoning, or request timeout.
- Date and time parsing beyond explicitly typed timestamps.
- Per-tool JSON Schema registration for stronger validation and discovery.
- Raw CEL authoring. It cannot bypass fixed block outcomes, sequential pipeline
  transitions, evaluation limits, or audit behavior.
