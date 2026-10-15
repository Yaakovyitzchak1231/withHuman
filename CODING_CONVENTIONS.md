# withHuman Coding Conventions

This is the default engineering contract for every withHuman repository. Apply
it to all new and changed code. A repository may deviate only when a local
`CLAUDE.md` records the reason and the replacement rule. Prefer one canonical
name across API, database, events, and logs. When a name or interface changes,
change all callers in the same change and delete the superseded form.

## Naming

| Surface | Convention | Example |
| --- | --- | --- |
| HTTP path segments | lowercase snake_case, plural nouns | `/api/v1/agent_instances/{id}` |
| Query parameters and JSON keys | lowercase snake_case | `organization_id`, `created_at` |
| Headers | standard HTTP title case | `Idempotency-Key`, `X-Request-ID` |
| Enum and error-code values | lowercase snake_case | `sso_required`, `membership_inactive` |
| Permission and event names | dot-separated lowercase namespaces | `request.create`, `approval.created` |
| PostgreSQL tables and columns | lowercase snake_case; plural tables | `approval_requests.deadline_at` |
| Go | `PascalCase` exports, `camelCase` locals, standard initialisms | `RequestID`, `httpClient` |
| TypeScript | `PascalCase` types/components, `camelCase` values | `ApprovalRequest`, `requestId` |
| CLI commands and flags | lowercase kebab-case | `withhuman migrate`, `--dry-run` |
| Environment variables | uppercase snake_case with `WITHHUMAN_` prefix | `WITHHUMAN_DATABASE_URL` |

Keep wire DTOs in their exact snake_case form. Map them explicitly to
camelCase TypeScript domain models when that improves application code. Use
`ID`, `HTTP`, `API`, and `URL` in Go names, but `id`, `http`, `api`, and `url`
in wire and database names.

File names follow the language and framework rather than one universal case:

- Go files use lowercase snake_case, tests end in `_test.go`, and package
  directories use short lowercase names without separators.
- TypeScript and JavaScript files use lowercase kebab-case, including React
  component files; exported component and type names remain `PascalCase`.
  Framework-reserved file names keep the framework's required spelling.
- Swift and Kotlin files that define a primary type use the matching
  `PascalCase` type name. Swift extensions use `Type+Concern.swift`. Android
  resource files use lowercase snake_case.
- SQL migrations use `<sequence>_<snake_case_description>.sql`. For an
  unlisted language or tool, follow its official ecosystem convention and
  record any repository-wide choice in the local `CLAUDE.md`.

## HTTP and JSON APIs

- OpenAPI is the source of truth. Design or update the contract, handler,
  client, and contract tests in one change. Do not ship undocumented fields or
  routes.
- Put public product endpoints under `/api/v1`; put protocol endpoints under
  their protocol namespace. Model collections and resources with nouns, and
  use subresources for commands when CRUD is not honest, such as
  `POST /requests/{id}/decision`.
- Use HTTP semantics deliberately: `GET` is safe, `POST` creates or commands,
  `PUT` replaces, `PATCH` partially updates, and `DELETE` is idempotent. Return
  `201` for creation, `204` for success without a body, and the most specific
  `4xx` or `5xx` status for failure.
- Return a resource directly. Return collections as a named object, for
  example `{ "requests": [...] }`, adding `next_cursor` when pagination is
  needed. Return errors only in this shape:

  ```json
  {
    "error": {
      "type": "invalid_request",
      "code": "membership_inactive",
      "message": "The membership is inactive.",
      "param": "membership_id",
      "request_id": "request-123"
    }
  }
  ```

  `type`, `code`, `message`, and `request_id` are required. `type` is one of
  `invalid_request`, `authentication`, `authorization`, `not_found`,
  `conflict`, `rate_limit`, `dependency`, or `internal`. `code` is the
  specific machine-readable reason; `message` is safe human-readable text.
  `param` is optional and identifies the invalid snake_case input field.
  Optional `details` must be documented, structured, and safe. Never expose
  secrets, internals, stack traces, or raw provider errors. Every response
  includes `X-Request-ID`; an error body's `request_id` must contain the same
  value.
- Use UUIDs named `id` or `<resource>_id`. Encode timestamps as UTC RFC 3339
  strings named `*_at`. Encode durations as documented strings such as `30s`
  or `15m`, never as unitless numbers. Use ISO 8601 dates named `*_on`.
- Require `Idempotency-Key` for consequential or retryable commands. The key
  is scoped to the authenticated organization and operation, and reuse with a
  different intent returns `409 Conflict`.
- Authenticate at the boundary, authorize against current organization state,
  validate the complete input before side effects, and scope every protected
  lookup by `organization_id`. Unknown JSON fields should be rejected unless
  the schema explicitly declares an extensible object.

## PostgreSQL schemas

- Use plural table names, singular column names, UUID primary keys named `id`,
  and foreign keys named `<referenced_resource>_id`. Every organization-owned
  row contains a non-null `organization_id`; include it in lookups, uniqueness
  constraints, and foreign-key boundaries where cross-organization linkage is
  possible.
- Use `timestamptz` for instants with `*_at` names, `date` for calendar dates
  with `*_on` names, and integer columns with an explicit unit suffix for
  stored durations, such as `session_max_age_seconds`. Store instants in UTC.
- Default to `NOT NULL`. Use `NULL` only when absence has domain meaning. Do
  not use an empty string, zero UUID, zero time, or magic number to mean
  missing. Boolean names must read affirmatively, such as `active`,
  `email_verified`, or `personal_api_keys_allowed`.
- Prefer normalized relational columns. Use `jsonb` only for genuinely
  unstructured or external payloads; its owned keys still use snake_case.
  Represent bounded states as `text` plus a `CHECK` constraint rather than a
  PostgreSQL enum. Enforce invariants with constraints and transactions, not
  application checks alone.
- Name explicit indexes `<table>_<purpose>_idx`. Index foreign keys and actual
  query shapes, and back every uniqueness or idempotency promise with a unique
  constraint. Avoid redundant indexes.
- Application migrations never modify Temporal-owned schemas. Never edit a
  migration that has shipped in a tagged release; put schema changes in a new
  numbered file.

## Boundary discipline

Validate once at each trust boundary and keep domain types independent of
transport, database, and provider SDK types. Database writes and external
effects must be retry-safe and idempotent. Never log credentials, secrets, raw
request payloads, or sensitive metadata. Log attribute keys use snake_case,
and correlation IDs must propagate through HTTP, Temporal, database activity,
and audit records.

Log messages must be useful in a message-only log view: name the operation
and its outcome, include safe distinguishing context such as a tool, channel,
provider, status or count, and explain a fallback or consequence when known.
Distinguish attempts from completed actions; claim success only after the
operation succeeds. Keep
correlation IDs and diagnostic details in structured attributes; never copy
raw errors, payloads, credentials or personal contact details into the message.
HTTP completion messages use `http request - {path}`, omit query strings,
and use error severity for 5xx responses.
