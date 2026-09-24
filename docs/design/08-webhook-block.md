# withHuman: webhook request approval block

Status: accepted · Last updated: 2026-09-02

> **Prototype.** Same caveat as
> [00-general-design.md](00-general-design.md): everything here can change
> without migration paths or backwards-compatibility constraints.

## Summary

`webhook` is the third `RequestApprovalBlock` type after `cel` and
`llm_judge`. A webhook block signs and posts the canonical request to an
HTTPS endpoint the organization operates, waits for a bounded JSON answer,
and maps that answer onto the ordinary block contract: `approve`, `deny`,
`human`, or `next` with metadata. The block configuration lists which of
those outcomes the endpoint may return, so an endpoint can never authorize
more than an administrator explicitly granted it. Every failure (network,
timeout, rejected status, malformed body, disallowed outcome) fails closed
to `human`.

The block builds entirely on the asynchronous substrate the LLM judge
shipped in [08-llm-judge-block.md](08-llm-judge-block.md): it registers as
an asynchronous runtime in `BlockRegistry`, the request path suspends in
front of it, and the worker's `ResumePipeline` activity runs it inline
with every later block on the pinned revisions. No new execution
machinery, tables for in-flight state, workflow phases, or audit event
types are needed. What is new:

1. **A delivery contract** organizations implement: a signed request, a
   closed response shape, and a documented status and error mapping.
2. **Webhook endpoints** as an organization-owned, revisioned resource
   (a stable key with immutable revisions of a name and URL, one active,
   and one sealed signing secret per key), referenced from block
   configuration by key.
3. **An egress policy** for the first outbound HTTP the product makes on an
   organization's behalf.
4. **Previous block metadata readable from CEL conditions**, so a webhook
   that enriches (`next` plus a score) can be thresholded by the block
   after it. This lifts one deferred CEL item and benefits judge blocks too.

## Goals

- Let an organization consult a service it already runs (fraud scoring,
  CRM lookups, budget checks, a policy engine) as one ordered pipeline
  step with the same explainability and audit record as any other block.
- Keep authorization bounded: the endpoint returns only outcomes the block
  allows, and the default grant is enrichment or escalation only.
- Make every delivery verifiable by the receiver (HMAC signature,
  timestamp, idempotency key) and every failure visible in evidence
  without leaking request values or secrets.
- Change nothing for pipelines that contain no asynchronous block.

## Non-goals (v1)

- Calling private or internal network addresses. Egress is public HTTPS
  by default.
- Redirects, streaming, or callback-style completion where the endpoint
  answers later on another channel. One request, one answer, inside the
  resume activity.
- Delivering decision events to adapters (an AAP transport feature,
  unrelated to blocks).
- Per-endpoint permissions. One organization-wide `webhook.write`
  permission covers every endpoint.
- Event webhooks (deliveries when something happens in withHuman). They
  are a separate design; this document only makes sure the endpoint
  resource can serve them.
- Exposing the pending stage to reviewers. The judge design deferred the
  "Evaluating" badge and lets a human decide during the pipeline stage;
  this design accepts the same trade-off for the same reasons.

## Reconciled with the judge substrate

What the webhook block reuses unchanged:

| Substrate | Where it lives | Used how |
| --- | --- | --- |
| Type-dispatched `Definition` envelope and per-type limits | `approvalblock` (`SetConfigJSON`, `LimitsFor`) | Adds the `webhook` variant and its limits |
| `BlockRegistry` with `asynchronous` runtimes and a composed fingerprint | `internal/approvalpipeline/registry.go` | Registers `webhook` as asynchronous with its own fingerprint and dependencies |
| Suspension in `Evaluate`, `approval_request_pipeline_suspensions`, `ResumeApproval` compare-and-set | `execution.go`, migration 0015, `internal/postgres/pipeline_suspensions.go` | Unchanged; a webhook block is simply another asynchronous block the executor suspends in front of |
| `ResumePipeline` and `AbandonPipeline` activities, resume before notification, routing after resume | `internal/orchestration` | Unchanged apart from the resume budget |
| `Attempts` on block results, `attempt_count` in evidence, `provider_error` | `block.go`, `execution.go` | Webhook results report real attempts; new error codes join the vocabulary |
| Capability gating and usage metering at create, activate, and preview | `management.go` (`requireJudgeCapability`) | Mirrored as `CapabilityWebhookBlock` and `DimensionWebhookDeliveries` |
| `BlockDraft` union, add-slot kind menu, `PipelineExits`, judge card anatomy | `frontend/src/app/pipeline-blocks.ts`, `ds/components/policy-block.tsx`, `judge-block.tsx` | Adds the `webhook` kind, card, summary, and exits |
| Sealed secrets at rest | `internal/secrets` under `WITHHUMAN_SECRETS_KEY`, per Slack bot tokens | Seals endpoint signing secrets, bound to the endpoint key |

Decisions taken from the judge design rather than re-decided here:

- A human may decide a request while its pipeline is suspended; the
  resume commits nothing when the request is no longer pending.
- Preview runs asynchronous blocks synchronously in the management
  request, against the real dependency. A webhook preview therefore calls
  the real endpoint, flagged as a preview.
- The static `reason` on the block is the reason recorded for an
  automatic decision; runtime text from the dependency is evidence.

## Block definition

```json
{
  "id": "fraud-check",
  "name": "Fraud service review",
  "type": "webhook",
  "enabled": true,
  "timeout": "5s",
  "max_attempts": 3,
  "config": {
    "endpoint_key": "fraud-service",
    "allowed_outcomes": ["next", "human"],
    "include": {
      "agent_reasoning": false,
      "context": false,
      "previous_metadata": true
    },
    "reason": "Fraud service reviewed the request."
  }
}
```

| Field | Meaning | Rules |
| --- | --- | --- |
| `timeout` | One delivery attempt, connect to full response | `1s` to `30s`, default `5s` |
| `max_attempts` | Delivery attempts for one evaluation | 1 to 3, default 3 |
| `config.endpoint_key` | The organization's webhook endpoint key | A key with at least one revision at revision create and preview; a key with an active revision at pipeline activation |
| `config.allowed_outcomes` | Outcomes the endpoint may return | Non-empty subset of `next`, `human`, `approve`, `deny`; `approve` and `deny` must be listed explicitly |
| `config.include` | Which untrusted or unmodeled fields are sent besides `tool`, `arguments`, and the provider-owned agent fields | Same shape and defaults as the judge: `agent_reasoning` false, `context` false, `previous_metadata` true |
| `config.reason` | Static reason recorded on an automatic decision | Required, like every other block |

Envelope limits, added to `approvalblock.LimitsFor`:

| Field | `cel` | `llm_judge` | `webhook` |
| --- | --- | --- | --- |
| `timeout` | 50 ms default, 1 s maximum | 30 s default, 60 s maximum | 5 s default, 30 s maximum |
| `max_attempts` | exactly 1 | 1 to 3 | 1 to 3 |
| Blocks per request | 1,000 | 5 | 5 |

`config_version` is `1`. The `allowed_outcomes` list is the configured
transition set that the
[lifecycle design](01-request-approval-pipeline.md#non-goals) requires
before any external judgment can authorize an action. The editor's default
is `["next", "human"]`, which makes a new webhook block an enrichment or
escalation step until an administrator deliberately grants automatic
outcomes.

The Go DSL grows `approvalblock.DefineWebhook(id, ...)` with
`Endpoint(key)`, `AllowedOutcomes(...)`, `Timeout`, `MaxAttempts`,
`Include`, and `Reason` options, next to `Define` and `DefineJudge`.

## Webhook endpoints

An endpoint is an organization-owned destination. It exists so that the
signing secret never enters an immutable, content-hashed, API-visible block
configuration, and so that a service move does not re-revision every
pipeline that references it. Endpoints are revisioned exactly the way
escalation paths are (design_doc/06): a stable `endpoint_key` that blocks
reference, append-only numbered revisions of the name and URL with at
most one active, activation and rollback under `If-Match`, a uses
listing, and an archive that deactivates the current revision. A destination
change is therefore a new revision, never an edit, and every evaluated
event records the endpoint key, the revision actually delivered to, and
the URL host.

The signing secret belongs to the key, not to a revision. It is minted
with the key's first revision (the one response that shows it), survives
every later revision so the receiver never reconfigures because the URL
moved, and is rotated in place. Deliveries resolve the key's active
revision at evaluation time, so activating a revision applies to the next
request without a pipeline revision, and an archived key is
`endpoint_unavailable` (human).

### Endpoints are shared destinations

Pipeline blocks are the first use of outbound webhooks, not the last: event
webhooks (a delivery when a request is created, decided, or expired, or
for every audit event) are an expected later feature. The endpoint
resource is therefore modeled the way Stripe and Svix model it: the
endpoint tables hold the destination and nothing else, and each use
references the key and carries its own semantics.

- `webhook_endpoint_revisions` holds name, URL, and provenance per
  revision; `webhook_endpoint_secrets` holds the key's sealed secret.
  Neither grows a column for one purpose: `allowed_outcomes` and `include`
  live on the block, an event-type filter and enabled flag would live on a
  future `webhook_subscriptions` row, and event delivery history would
  live on a future `webhook_deliveries` ledger. Block deliveries need no
  ledger because their evidence is in `audit_events`.
- One endpoint may serve several purposes. A receiver is one service with
  one URL and one secret; forcing one endpoint per purpose would mean
  duplicated secrets, duplicated rotations, and the same URL created
  twice. Reuse is allowed, not required: different paths on the same host
  are simply different endpoints.
- The payload `type` field is the discriminator across uses
  (`request_approval_block.webhook` and `webhook_endpoint.test` now, event
  types later), the envelope fields (`version`, `delivery_id`,
  `idempotency_key`) and the signature headers are the same for every
  use, and the contracts fail closed against each other: a receiver that
  treats a block delivery like an event and returns an empty 200 produces
  `invalid_block_output`, which is `human`.
- Every use registers a reference check. Archiving is refused while any
  active use references the key, and the endpoint's page lists its uses
  ("2 pipelines, 3 subscriptions") so the blast radius of a rotation or
  archive is visible before it happens. There is no delete: history is
  append-only.
- Endpoint management has its own permission, `webhook.write`, so
  ownership of a destination is independent of ownership of any one use.
  Referencing an endpoint from a block still requires
  `pipeline.write`. `webhook.write` is granted to the
  same roles that hold pipeline manage today.

Rotating a shared secret affects every purpose at once, which is why
overlapping secrets during rotation should be the first deferred item to
land once event webhooks arrive.

### `webhook_endpoint_revisions` and `webhook_endpoint_secrets`

`webhook_endpoint_revisions` mirrors `escalation_paths` column for column
(`05-request-approval-pipeline-storage.md` describes the pattern):

| Column | Type | Purpose |
| --- | --- | --- |
| `id` | `uuid` | Revision identity; the audit subject |
| `organization_id` | `uuid` | Tenant boundary |
| `endpoint_key` | `text` | The stable key every use references; `^[a-z0-9][a-z0-9._-]{0,62}$` |
| `revision` | `bigint` | Monotonic per key, enforced by a trigger under the advisory lock `webhook_endpoint:<org>:<key>` |
| `is_active` | `boolean` | At most one per key (partial unique index); the only mutable field |
| `name` | `text` | Display name |
| `url` | `text` | Absolute HTTPS URL; validated by the egress policy on write |
| `created_by_membership_id`, `created_at` | | Provenance |

`webhook_endpoint_secrets` is keyed by `(organization_id, endpoint_key)`
and holds `sealed_signing_secret` (sealed with `internal/secrets`,
associated data bound to the organization and key so a sealed value
copied under another key fails to open), `rotated_at`, and
`rotated_by_membership_id`. The row is inserted in the transaction that
creates the key's first revision and updated in place by rotation.

Archiving (`DELETE .../active`) is refused with `409` /
`webhook_endpoint_in_use` while any active use references the key,
mirroring how an escalation path archive is refused while a pipeline names
it. The only use in v1 is a webhook block in an active pipeline revision;
the check is an interface each use implements, and the error lists every
referencing use. Pipeline activation, in turn, takes each referenced key's
advisory lock inside its own transaction and refuses with
`webhook_endpoint_inactive` unless the key has an active revision, so a
archive and an activation never both commit. A suspended request whose
endpoint was archived before the resume fails that block with
`endpoint_unavailable`, which is `human`.

### Secrets and configuration

The secret is 32 random bytes shown once as `whsec_` plus URL-safe
base64, on the response that creates the key's first revision and at
rotation. Reads never return it. Rotation replaces the secret immediately
for every revision of the key; overlapping secrets are deferred.

The deployment secrets key, `WITHHUMAN_SECRETS_KEY`, is the feature's on
and off switch: it already seals Slack bot tokens through
`internal/secrets`, and endpoint signing secrets are sealed the same way
with the row-bound label. Without it the type is still known (stored
revisions decode and list), but endpoint creation fails with `dependency`
/ `webhook_unconfigured`, revisions containing a webhook block cannot be
created or activated (the same shape as `llm_judge_unconfigured`), and
`/api/runtime-config` reports `webhook_enabled: false` so the editor
disables the choice. (Decided during implementation: the general secrets
key had landed on main, so a webhook-specific key would have been a third
key for the same purpose.)

Two more settings govern egress: `WITHHUMAN_WEBHOOK_ALLOW_INSECURE`
(permit `http://`) and `WITHHUMAN_WEBHOOK_ALLOW_PRIVATE_NETWORKS` (permit
private, loopback, and link-local destinations). Both default to false and
exist for development and tests.

### Management API

Paths follow the coding conventions' snake_case rule. The existing
pipeline and gateway routes predate it and are the outliers.

| Method and path | Permission | Behavior |
| --- | --- | --- |
| `GET /api/v1/webhook_endpoints` | `webhook.read` | One summary per key (latest name and URL, active revision, secret rotation time), never secrets |
| `GET /api/v1/webhook_endpoints/{key}` | `webhook.read` | The active revision, with an `ETag` for `If-Match`; 404 while none is active |
| `POST /api/v1/webhook_endpoints/{key}/revisions` | `webhook.write` | Create the next inactive revision (name, URL); the key's first revision also carries `signing_secret` exactly once |
| `GET /api/v1/webhook_endpoints/{key}/revisions` | `webhook.read` | History, newest first |
| `GET /api/v1/webhook_endpoints/{key}/revisions/{n}` | `webhook.read` | One revision |
| `POST /api/v1/webhook_endpoints/{key}/revisions/{n}/activate` | `webhook.write` | Activate or roll back under `If-Match` ("0" when none is active) |
| `POST /api/v1/webhook_endpoints/{key}/revisions/{n}/test` | `webhook.write` | Send one signed synthetic delivery to that revision's URL now, active or not; returns status, latency, and the parsed or rejected answer |
| `GET /api/v1/webhook_endpoints/{key}/uses` | `webhook.read` | The active pipeline revisions posting to the key; the set that refuses an archive |
| `DELETE /api/v1/webhook_endpoints/{key}/active` | `webhook.write` | Archive under `If-Match`; `409 webhook_endpoint_in_use` while referenced by any active use |
| `POST /api/v1/webhook_endpoints/{key}/rotate_secret` | `webhook.write` | Replace the key's secret; returns the new one once |

Listing endpoints and reading a key's active revision to pick one in the
pipeline editor needs `webhook.read`, which the admin and owner roles
hold; a custom role for pipeline authors must include it. Those reads
never include more than key, name, URL, and whether a revision is active.

Audit events: `webhook_endpoint.revision_created`, `.activated`,
`.deactivated`, `.secret_rotated`, `.tested`, all with the key, the
revision, and the URL host only.

## Delivery contract

This is the contract an organization implements. It is versioned by the
`version` field in the body, and `type` says which use produced the
delivery so one receiver can serve blocks and, later, events.

### Request

```http
POST /approvals/fraud-check HTTP/1.1
Host: fraud.example.com
Content-Type: application/json
User-Agent: withHuman/<version>
WithHuman-Signature: t=1756771200,v1=5257a869e7ecebeda32affa62cdca3fa51cad7e77a0e56ff536d0ce8e108d8c2
WithHuman-Delivery-Id: 0f1e2d3c-4b5a-4c6d-8e7f-a0b1c2d3e4f5
WithHuman-Idempotency-Key: 7ab8c8ec-7b2d-4fd6-9b52-752f9515eb71:fraud-check
```

```jsonc
{
  "version": "2026-09-01",
  "type": "request_approval_block.webhook",
  "preview": false,
  "delivery_id": "0f1e2d3c-4b5a-4c6d-8e7f-a0b1c2d3e4f5",
  "idempotency_key": "7ab8c8ec-7b2d-4fd6-9b52-752f9515eb71:fraud-check",
  "organization_id": "ddacbef5-4c99-4125-aa1b-b70b1b518b16",
  "block": { "id": "fraud-check", "name": "Fraud service review", "attempt": 1, "max_attempts": 3 },
  "pipeline": {
    "evaluation_id": "6b2b6a0e-4f0c-4b34-9a0f-5f0d5d3e0a11",
    "scope": "organization",
    "revision": 7
  },
  "request": {
    "id": "7ab8c8ec-7b2d-4fd6-9b52-752f9515eb71",
    "tool": "stripe.refund",
    "arguments": { "amount_cents": 4900, "reason": "duplicate_charge" },
    "agent_reasoning": "…",          // only with include.agent_reasoning
    "context": { "run_id": "4821" },  // only with include.context
    "created_at": "2026-09-01T12:00:00Z",
    "deadline_at": "2026-09-02T12:00:00Z"
  },
  "agent": {
    "slug": "billing",
    "name": "Billing agent"
  },
  "previous_metadata": {               // only with include.previous_metadata
    "organization": { "risk-classifier": { "risk": "low" } },
    "agent": {}
  }
}
```

- `WithHuman-Signature` is `t=<unix seconds>,v1=<hex HMAC-SHA256>` where
  the message is `<t>.<raw body>` and the key is the endpoint secret.
  Receivers verify with a constant-time comparison and reject timestamps
  outside a five-minute window. The construction is the one the codebase
  already verifies for directory sync in `internal/polis/dsync.go`; the
  header name, second-granularity timestamp, and `v1` tag are withHuman's
  own. `internal/webhookdelivery` exports `VerifySignature` so receivers
  written in Go can use it directly and the tests use it as the reference
  verifier.
- `delivery_id` is unique per attempt. `idempotency_key` is stable across
  attempts of one evaluation (`<request_id>:<block_id>`), so a receiver
  that already answered can return the same answer. Preview deliveries use
  `preview:<uuid>` and set `preview: true`; receivers may answer them
  without side effects.
- `previous_metadata` is nested by scope and block key, the same shape CEL
  blocks read (below) and the judge renders.
- `request.arguments` are sent verbatim. Sensitive-field redaction, when it
  exists, applies before the pipeline and therefore before delivery.
- The request section is the same block input the judge receives, so a
  webhook after a judge sees the judge's verdict in `previous_metadata`,
  and a judge after a webhook sees the webhook's metadata.

### Response

The endpoint answers `200 OK` (any 2xx is accepted) with a JSON object:

```json
{
  "outcome": "next",
  "reason": "Risk score 12, below the review threshold.",
  "metadata": { "risk_score": 12, "risk_band": "low" }
}
```

| Field | Rules |
| --- | --- |
| `outcome` | Required; one of `next`, `human`, `approve`, `deny`; must be in the block's `allowed_outcomes` |
| `reason` | Optional string, at most 1,024 characters; recorded as delivery evidence |
| `metadata` | Optional object, at most 16 KiB encoded; becomes the block's metadata, available to later blocks under this block's key |

Unknown fields are rejected.

### Status and error mapping

| Condition | Retry | Block result |
| --- | --- | --- |
| Transport error, TLS failure, DNS failure | yes | `human`, `endpoint_unreachable` |
| Attempt exceeds `timeout` | yes | `human`, `timeout` |
| `408`, `425`, `429`, any `5xx` | yes | `human`, `endpoint_error` |
| Any other `4xx` | no | `human`, `endpoint_rejected` |
| Any `3xx` (redirects are not followed) | no | `human`, `endpoint_rejected` |
| Body over 64 KiB, not JSON, unknown field, missing or unknown outcome, oversize reason or metadata | no | `human`, `invalid_block_output` |
| Outcome not in `allowed_outcomes` | no | `human`, `outcome_not_allowed` |
| Endpoint key has no active revision, secret cannot be opened, URL fails the egress policy at call time | no | `human`, `endpoint_unavailable` |

Retries run inside the block's `Evaluate`, exactly as the judge retries
provider calls: up to `max_attempts` with backoff of 1 s, 2 s, 4 s (10 s
cap), stopping early when the context is cancelled. The worst case for the
maximum configuration is about 93 seconds per block. The completion
reason for a block whose budget ran out is `retries_exhausted`, and its
`attempt_count` is real.

`endpoint_unreachable`, `endpoint_error`, `endpoint_rejected`,
`outcome_not_allowed`, and `endpoint_unavailable` join `BlockErrorCode`;
`timeout` and `invalid_block_output` already exist. They are added to the
`error_code` enum in `openapi.yaml`.

### Delivery evidence

`BlockEvidence` gains an optional `Delivery` record, serialized as
`delivery` on the `request_approval_block.evaluated` event and the preview
block result:

```json
{
  "endpoint_key": "fraud-service",
  "endpoint_revision": 3,
  "endpoint_host": "fraud.example.com",
  "http_status": 200,
  "duration_ms": 412,
  "reason": "Risk score 12, below the review threshold."
}
```

It never contains the request body, response body beyond the parsed
fields, headers, or the secret. `trace` is empty for webhook blocks. Like
the judge rationale, the endpoint's `reason` is runtime text from the
organization's own dependency and is recorded because the reviewer needs
it; it is not copied into `decisions.note`, so AAP responses match CEL and
judge decisions.

## Egress policy

The worker's resume activity and the server's endpoint test share one
client, `internal/webhookdelivery`. It enforces:

- Absolute `https://` URLs with a host, no userinfo, no fragment;
  `http://` only with `WITHHUMAN_WEBHOOK_ALLOW_INSECURE`.
- Resolution of every A and AAAA record before dialing; the request is
  refused if any address is loopback, unspecified, RFC 1918 private,
  link-local, unique-local, multicast, or the cloud metadata address
  `169.254.169.254`. IP-literal hosts get the same check.
  `WITHHUMAN_WEBHOOK_ALLOW_PRIVATE_NETWORKS` lifts it.
- Dialing the validated IP directly with the TLS server name set to the
  original host, so a DNS answer cannot change between check and connect.
- No redirects. Response bodies read through a 64 KiB limit.
- The block timeout as the per-attempt context deadline; standard
  `HTTPS_PROXY` support so a deployment behind an egress proxy needs no
  withHuman configuration.

The URL is validated against the same policy when an endpoint revision is
created, so an administrator learns about a rejected address immediately
rather than on the first request.

## Execution

Nothing changes on the request path: `runBlocks` already stops in front of
any block whose runtime is `asynchronous`, commits the suspension, and
`ResumePipeline` runs the rest inline. The webhook-specific parts are:

- **Registration.** `NewBlockRegistry` registers `webhook` as
  asynchronous with fingerprint `withhuman-webhook/v1;delivery/v1;limits/v1`
  and two dependencies supplied by the composition root:
  `WithWebhookDelivery(client)` and `WithWebhookEndpoints(reader, sealer)`.
  With no sealer the runtime compiles to `ErrWebhookUnconfigured`, the
  same shape as the judge without a model.
- **`CompiledWebhookBlock.Evaluate`.** Consumes a webhook counter on
  `BlockExecutionBudget` (five per request), loads the key's active
  endpoint revision within the organization, opens the key's secret,
  builds and signs the payload, runs the attempt loop, parses and
  validates the answer against `allowed_outcomes`, records usage, and
  returns the result with `Attempts` and delivery evidence. It performs
  the same reads the judge performs (usage metering); loading the endpoint
  is the one extra read, and it is a read of an immutable revision, not of
  request state.
- **Resume budget.** `pipelineResumeBudget` is currently a constant sized
  for five judge blocks. It becomes a value derived from the per-type
  limits in `approvalblock` (sum over types of blocks per request times
  worst-case attempt time plus backoff), which is a pure function of
  constants and therefore deterministic on replay. With webhooks it grows
  by about eight minutes.
- **Dependency checks at management time.** `CreateRevision` and
  `Preview` verify every referenced endpoint key has a revision in the
  organization (`webhook_endpoint_not_found`) and check
  `CapabilityWebhookBlock`, mirroring `requireJudgeCapability`; `Activate`
  requires each key to have an active revision, checked by the store
  inside the activating transaction (`webhook_endpoint_inactive`). The
  revision references report (`/references`) lists each key with `exists`
  and `active`, exactly like escalation paths.

## Previous metadata in CEL conditions

Enrichment is the primary use of a webhook block, so a CEL block after it
must be able to read what it returned. This lifts one deferred item from
the [CEL design](02-cel-policy-engine.md#deferred-capabilities):

- Field paths `/previous_metadata/<scope>/<block_key>/...` are valid, with
  `<scope>` one of `organization` or `agent` and at least one further
  token. They are dynamic like `/request/arguments`.
- `buildSafeInput` nests the executor's namespaced map by scope and key.
  Missing-path semantics are unchanged: only `not_exists` matches an absent
  block or field, so a CEL block that reads a webhook's score falls
  through to `next` when the webhook errored, and the pipeline ends in
  `human`.
- `CELRuntimeFingerprint` becomes `withhuman-cel-block/v2` because the
  declarations changed.
- Preview namespaces metadata by the previewed scope instead of the bare
  block ID, and the preview sample accepts an optional `previous_metadata`
  object so the frontend's stitched effective test can carry the
  organization preview's metadata into the agent preview.

## Preview and testing

Preview follows the judge: it runs the webhook block synchronously in the
management request against the real endpoint, with `preview: true` and a
`preview:` idempotency key so receivers can recognize it. The test replay
in the editor shows the delivery evidence (status, latency, attempts,
reason) on the webhook step, and the block's exit chip lights according to
the outcome the endpoint returned. Connectivity and signature verification
are tested on the endpoint itself with the synchronous test delivery,
which sends a `webhook_endpoint.test` payload with a synthetic request and
reports status, latency, and whether the answer parsed.

Stubbed responses for previewing without a deployed receiver are deferred.

## Product frontend

- **Kind menu.** `PipelineAddSlot` gains a third `PipelineAddChoice`,
  Webhook, disabled with a hint when runtime config reports
  `webhook_enabled: false`, exactly as the judge choice keys off an
  empty `llm_judge_models`.
- **`WebhookBlockCard`.** Same anatomy as `JudgeBlockCard` (grip,
  position, name, summary line, overflow menu; expanded fields in
  evaluation order). Summary line: endpoint name, host, and budget, for
  example `fraud-service · fraud.example.com · up to 3 attempts, 5s each`.
  Expanded: Name, Endpoint (select over the organization's endpoints with
  a "Manage endpoints" link), Allowed outcomes (four toggles whose
  `approve` and `deny` carry the decision tones), Attempt timeout, Max
  attempts, the three include switches, and Reason.
- **Exits.** `webhookExits(block)` feeds `PipelineExits` one branch per
  allowed terminal outcome labeled "Endpoint says", with the rail
  continuing down only when `next` is allowed. A test run lights the exit
  the endpoint chose.
- **`WebhookBlockSummaryCard`** for the folded organization section.
- **Endpoints page.** A top-level `Webhook endpoints` screen in the
  configuration group of the sidebar, not a pipelines sub-page, because
  the resource will outgrow pipelines: a table of name, host, last
  updated, and uses; a create dialog that reveals the secret once with a
  copy affordance and an explicit "You will not see this again" line;
  rotate and test in the row menu; delete refused with every referencing
  use listed. The detail page is where event subscriptions would appear
  later.
- **Styleguide.** The Policy builder section gains the card, the summary
  card, the endpoint select, and the multi-exit demo, each with example
  usage, per the repository rule that a primitive is demoed there first.

Copy uses no em dashes.

## Deployment

The Compose worker sits only on the `private` network, which is
`internal: true` and has no egress. The worker joins `public` (the server
already does, and needs it for the endpoint test), or the deployment
supplies `HTTPS_PROXY`. Called out in the Compose file and README.

Metrics: deliveries by outcome and error code, attempt counts, delivery
latency, and resumes that included a webhook block.

## Implementation plan

All five phases landed on 2026-09-02 in one change; the notes below are the
work order as planned, kept for the file map.

Each phase ends green on `make verify`. Files are relative to
`withhuman_core/` unless noted.

### Phase 1: block type and endpoints (no execution yet)

1. `approvalblock/webhook.go`: `TypeWebhook`, `WebhookConfig` with
   `Validate`, limits in `typeLimits`, `DefineWebhook` and options,
   `ConfigJSON` and `SetConfigJSON` cases; tests in `definition_test.go`.
2. Migration `db/migrations/0016_webhook_endpoints.sql`.
3. `domain.WebhookEndpoint`, `application.WebhookEndpointRepository` with
   validated inputs, `internal/postgres/webhook_endpoints.go` including
   the active-revision reference query for delete refusal.
4. `config`: `WebhookConfigured()` on `WITHHUMAN_SECRETS_KEY`, the two
   egress flags; runtime-config `webhook_enabled`.
5. `internal/webhookendpoints` management service: create with one-time
   secret sealed under `(organization_id, id)`, update, delete, rotate,
   test; `httpapi` routes and representations; `openapi.yaml`; audit
   events; `CapabilityWebhookBlock` and `DimensionWebhookDeliveries` in
   `application/entitlements_inputs.go`; `PermissionWebhookManage` in
   `domain/identity.go` granted alongside pipeline manage, and the
   reference-check interface with its pipeline implementation.
6. Registry: register `webhook` as asynchronous with a compiler that
   validates and returns `ErrWebhookUnconfigured` without a sealer; the
   endpoint existence check and capability gate in `management.go`.
   Revisions with webhook blocks can be created and activated, and the
   executor suspends in front of them, but the resume produces
   `endpoint_unavailable` until phase 2 wires delivery.
7. Tests: definition validation, endpoint integration tests (uniqueness,
   secret never read back, delete refusal, sealed value bound to its row),
   HTTP contract tests, registry and management tests for the unconfigured
   and missing-endpoint rejections.

### Phase 2: delivery client and the webhook runtime

1. `internal/webhookdelivery`: URL policy, resolving and pinning dialer,
   signer and `VerifySignature`, request builder, response parser with the
   limits above, a `Client` interface with a fake for tests.
2. `internal/approvalpipeline/webhook.go`: `CompiledWebhookBlock`, the
   attempt loop, the status and error mapping, `Delivery` evidence,
   budget counter, usage metering, registry options `WithWebhookDelivery`
   and `WithWebhookEndpoints`; new error codes in `block.go` and
   `openapi.yaml`; `delivery` on the evaluated event and preview block.
3. Derived `pipelineResumeBudget` in `internal/orchestration/workflow.go`.
4. Composition: `runtime/server.go`, `runtime/worker.go`, and
   `singlebinary` construct the client and sealer and pass them to
   the registry; the endpoint test in the management service uses the same
   client.
5. Tests: policy table tests (private ranges, IP literals, rebinding,
   redirects, body limit), signature round trip, parser limits, runtime
   tests with the fake client covering every mapping row and
   `allowed_outcomes`, executor tests for a webhook suspended and resumed
   between CEL blocks, workflow test for a resume containing a webhook,
   `httptest`-backed delivery tests including headers and idempotency.

### Phase 3: previous metadata in CEL

1. `approvalblock/definition.go` `validateField`, `cel.go`
   `buildSafeInput`, fingerprint bump, preview namespacing and
   `previous_metadata` in the sample (`management.go`, `openapi.yaml`,
   `pipeline-view.tsx` stitched test, `suggestPaths`).
2. Tests: `cel_test.go` presence and threshold semantics across scopes,
   `management_test.go` forwarding, a pipeline test of webhook `next` then
   CEL `approve`.

This phase is independent and can land first; it is listed third only
because it is the smallest.

### Phase 4: product frontend

1. `types.ts` union member, `pipeline-blocks.ts` mapping, envelope
   defaults, and validation for the `webhook` kind.
2. `ds/components/webhook-block.tsx`: card, summary card, `webhookExits`;
   endpoint select; styleguide demos in `policy-builder.tsx`.
3. The add-slot choice, the pipeline view edit and read paths, JSON mode,
   test replay delivery evidence.
4. `api.ts` methods, the Webhook endpoints screen and route, sidebar
   entry, create-and-reveal dialog, rotate and test flows.
5. `make frontend`, typecheck, and a run through the full stack.

### Phase 5: verification, deployment, documentation

1. Single-binary end-to-end test with an in-process endpoint: one that
   enriches then approves via a following CEL block, one that returns a
   disallowed `approve` and lands in human review, one that times out and
   records `retries_exhausted`. `scripts/smoke.sh` extension.
2. Compose worker egress; README configuration table; `NEXT_STEPS.md`.
3. Amend `01-request-approval-pipeline.md` (webhook no longer deferred),
   `05-request-approval-pipeline-storage.md` (deferred list),
   `02-cel-policy-engine.md` (`/previous_metadata`), and the core
   `CLAUDE.md` invariants (three block types; the webhook key; the egress
   policy).

## Acceptance

- A pipeline with only CEL blocks behaves exactly as before: same events,
  same inline automatic decisions, no workflow start.
- A request reaching a webhook block commits as `pending` with an open
  suspension, `suspended` evidence, no routing snapshot, and no decision;
  the AAP response is `pending`.
- The endpoint receives a delivery whose signature verifies with the
  reference verifier, with a stable idempotency key across attempts and a
  unique delivery ID per attempt.
- Each row of the status and error mapping produces the stated block
  result, evidence, and completion reason, and never `approve`.
- An outcome outside `allowed_outcomes` is `human` with
  `outcome_not_allowed` even when the endpoint answers `approve`.
- A `next` answer's metadata is visible to a following CEL block through
  `/previous_metadata/<scope>/<key>/...` and to a following judge through
  `include.previous_metadata`, and a following block's `approve` records a
  `pipeline` decision from the worker.
- A human decision before the resume wins; the resumed segment commits no
  decision. A request whose endpoint never answers expires at its deadline.
- The egress policy refuses private, loopback, link-local, and metadata
  destinations, IP literals in those ranges, redirects, and oversize
  bodies, and pins the resolved address.
- Secrets are returned exactly once, stored only sealed, bound to their
  row, and never appear in audit data, logs, evidence, or list responses.
- Without `WITHHUMAN_SECRETS_KEY`, endpoints cannot be created,
  revisions with webhook blocks are rejected with `webhook_unconfigured`,
  and the editor disables the choice.

## Alternatives considered

| Alternative | Why not |
| --- | --- |
| Synchronous delivery in the create handler | Holds the AAP connection through retries, exposes uncommitted request IDs, and contradicts the substrate the judge established |
| One Temporal activity per webhook attempt | Gives Temporal per-attempt history, but splits the resume into a workflow loop the judge does not have; the inline attempt loop inside `Evaluate` matches the judge and keeps `ResumeApproval` a single commit |
| URL inside the block configuration | Every service move re-revisions every referencing pipeline; keeping the URL on the endpoint mirrors routing targets and is audited |
| A general `KeyManager` port now | `internal/secrets` under `WITHHUMAN_SECRETS_KEY` already seals stored credentials; the port can wrap it later |
| One endpoint table per purpose (pipeline endpoints, event endpoints) | Duplicates secret handling, egress validation, rotation, and the settings screen, and makes users create the same receiver twice; per-purpose semantics belong on the referencing row, not the destination |
| Forbidding one endpoint from serving several purposes | A receiver is one service with one secret; the `type` discriminator, fail-closed contracts, and the uses listing make sharing safe |
| Static bearer token instead of an HMAC signature | Replayable, not bound to the body, and not what receivers already verify from withHuman |
| Blocking reviewers during the pipeline stage | Decided the other way by the judge design; revisit together with the deferred "Evaluating" badge |
| Stubbed responses in preview | Live preview matches the judge and tests the real receiver; stubs are a later convenience |

## Deferred

- Overlapping secrets during rotation; first in line once event webhooks
  share endpoints with blocks.
- Event webhooks: `webhook_subscriptions`, a `webhook_deliveries` ledger
  with long backoff, dead-lettering, and replay. Their own design.
- Callback-style completion and streaming responses.
- Stubbed responses for preview without a deployed receiver.
- Per-endpoint permissions and per-scope pipeline permissions.
- Mutual TLS, custom headers, and static extra credentials on endpoints.
- An organization-wide allowlist of endpoint hosts.
- The "Evaluating" badge and reviewer gating during the pipeline stage,
  shared with the judge.
- Consolidating the gateway and webhook sealing keys into one data key.
