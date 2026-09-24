# Invitations, membership management, and the enterprise connector

Status: draft · Last updated: 2026-08-28

> **Prototype.** Same caveat as
> [00-general-design.md](00-general-design.md): everything here can change
> without migration paths or backwards-compatibility constraints. Optimize for
> the right model.

## Summary

Today there is no way to get a second human into a withHuman organization
except direct SQL. This document designs the missing surface: invitations,
membership management, and the admission flow that turns an invited email
address into an active membership. It also records the enterprise connector
decision that the SaaS edition's SSO and SCIM path depends on.

The load-bearing decision, which everything else follows from:

**Invitations and memberships are canonical withHuman domain in both
editions. Any authentication provider answers exactly one question: who is
this person, with what verified email.** The open-source and hosted editions
run the same invitation and membership code; only the authenticator behind
the admission step differs. withHuman never uses a vendor's organizations,
invitations, or membership products, because memberships here are audit
substrate: decisions and audit events reference membership rows, the
last-owner invariant lives in database triggers, and reviewer semantics hang
off membership state. Mirroring that from a vendor over webhooks would put
eventual consistency exactly where an approval product needs transactional
truth, and it would fork the two editions.

For enterprise SSO and SCIM, the decision is to **self-host Ory Polis**
(formerly BoxyHQ SAML Jackson, Apache 2.0) as the connector layer behind
`EnterpriseSSOPort` and `DirectorySyncPort`, rather than renting WorkOS,
Clerk, or a comparable hosted connector. The rationale, spike evidence, and
non-negotiable operational requirements are in the second half of this
document.

## Goals

- A permission-guarded flow for inviting humans into an organization and
  managing their memberships, identical in both editions.
- Admission that composes with any authenticator: local password today,
  enterprise SSO through the same step later.
- Preserve the existing identity invariants: `external_identities`
  (provider, issuer, subject) is the durable identity link, email is only a
  matching hint, memberships are never row-deleted.
- Self-service SSO and SCIM setup for customer IT administrators with no
  withHuman engineer in the loop.
- Zero marginal infrastructure cost per enterprise connection, and no future
  migration that requires customers' IT teams to redo their IdP setup.

## Non-goals

- Open self-serve signup in the open-source edition. Self-hosted withHuman
  is bootstrap-then-invite only.
- Domain-based auto-join (`admission_mode`, `organization_domains`). The
  schema anticipates it; this design does not implement it.
- Team assignment in invitations. Invites carry roles only; team
  assignment happens after joining, on the Teams page.
- Password reset. The invitation token machinery (hashed, single-use,
  expiring token leading to a set-password page) is deliberately shaped so a
  reset flow can reuse it later, but reset is not in scope here.
- Hosted self-serve organization creation. That remains the
  `TenantLifecycle` port in
  [05-saas-plugin-points.md](05-saas-plugin-points.md); this design assumes
  the organization already exists.

## Part 1: Invitations and membership management

### Domain model

New `invitations` table. An invitation is addressed to an **email**, not a
user, because the invitee may not exist yet:

- `id`, `organization_id`
- `email` (citext)
- `roles text[]`, CHECK-constrained to the `membership_roles` vocabulary
- `invited_by_membership_id`
- `token_hash` (unique; raw token shown once, hashed at rest, same
  discipline as sessions and agent credentials)
- `status`: `pending | accepted | revoked | expired`
- `expires_at` (default seven days)
- `accepted_membership_id`, timestamps

Constraints and rules:

- Partial unique index on `(organization_id, lower(email))` where
  `status = 'pending'`: one live invitation per email per organization.
- Resend rotates `token_hash` and extends `expires_at` on the same row; it
  does not create a new row.
- The invite link is the whole proof of acceptance, so whoever holds it
  can accept as the invitee. The API therefore returns it to the inviter
  only on a deployment with no email delivery configured
  (`members.Config.ExposeInviteLinks`, set by the runtime from the SMTP
  configuration); everywhere else the link goes only to the invitee's
  mailbox and the response carries the invitation alone. Because only
  the hash persists, resending a pending invitation mints a fresh token
  (the rotation) and the previous link stops working.
- Resending re-arms the invitation's grants, so the resender passes the
  same kernel and freshness ceremony as the inviter: every grant is
  re-evaluated against the resender's live authority and a dangerous one
  needs a fresh session. An inviter who could not have created the
  invitation cannot re-arm it.
- `accepted` and `revoked` are terminal. Re-inviting after revocation or
  after a member was deprovisioned creates a fresh row.
- Expiry is enforced lazily at redemption. No sweep job.

**Membership status `invited` is deleted.** A membership now exists only
once someone has accepted; it is born `active` with
`admission_source = 'invite'`. Pre-creating users and memberships for
unaccepted invitations would pollute the canonical tables with people who
may never arrive. Prototype rules apply: remove the enum value, no shim.

### The flow

```mermaid
flowchart LR
    Admin["Admin with membership.invite"] -->|create| Invitation
    Invitation -->|notification port + copyable link| Invitee
    Invitee -->|/invite/token| Preview["Preview page (org, inviter, email)"]
    Preview --> Authn["Authenticate via Authenticator port"]
    Authn --> Admit["Admission transaction"]
    Admit --> Session["Org-scoped withHuman session"]
```

1. **Create.** An authenticated human with `membership.invite` posts email
   plus roles. Only owners may include the `owner` role. Audit event.
2. **Deliver.** Through the notification port. A deployment without SMTP
   gets the correlated logging adapter plus a copy-invite-link affordance
   in the UI, so self-hosters paste the link into their chat tool. Once
   email delivery is configured the link is mailed only and never shown
   to the inviter. Delivery is deliberately independent of the
   authentication provider.
3. **Accept.** The invitee opens `/invite/<token>`. The server renders a
   preview (organization name, inviter, invited email) without
   authentication. Redemption requires the person to authenticate as
   someone. This is the only provider-variable step in the design, and it
   goes through the `Authenticator` port.
4. **Admit.** One transaction: find-or-create user keyed by
   `external_identities` (issuer, subject), create the membership
   (`active`, `admission_source = 'invite'`), write `membership_roles`,
   mark the invitation accepted, append audit events, issue an org-scoped
   withHuman session.

**Email binding.** Redemption requires the authenticated identity's verified
email to match the invitation email case-insensitively. Otherwise the page
says "you are signed in as X but this invitation is for Y" and offers a
switch-account path. The token is a capability, but a forwarded invitation
email must not admit a third party under the invited person's intended
identity. SAML has no explicit verified-email flag; the enterprise IdP
asserting the email is the verification.

### The admission step per edition

**Open edition (local adapter).**

- New user: the accept page shows a set-password form (existing Argon2id
  parameters, twelve-character minimum). Submission creates user,
  `external_identities` row (`provider_key = 'local'`), `local_credentials`,
  and membership in the admission transaction. **This is the only signup
  path in the open edition.**
- Existing user: password prompt. This requires a redemption-scoped
  credential check, because `LoginLocal` requires an active membership in
  the target organization, which the invitee does not have yet. Verify the
  password against the user's global `local_credentials`, then admit, then
  mint the session.
- Local password becomes a real `Authenticator` adapter (`Begin` points at
  the local form flow, `Complete` verifies the password and returns an
  assertion). Invite acceptance is the first flow genuinely built on the
  port, which makes the claim in
  [02-auth-architecture.md](02-auth-architecture.md) that local auth is
  "the open adapter" true in code for the first time.

**Hosted edition (enterprise adapter).** `/invite/<token>` stashes the token
in flow state, `Authenticator.Begin` redirects to the SSO flow (or a hosted
login page), the callback reaches `Complete`, and the normalized assertion
feeds the same admission transaction. For an organization that requires
SSO, the accept page routes through SSO instead of any password form.
Same transaction, different authenticator.

**Existing members and SSO (amendment).** An organization that enables SSO
after its members exist is the normal enterprise rollout, and those
members have no provider identity yet. When a verified SSO assertion's
email matches an active member of the asserted organization, the identity
is linked and the member signs in; the organization's own IdP asserting
the email is the vouch, exactly as in the SAML argument above. This is
identity linking, never admission: no membership is created, so the
no-silent-JIT rule stands. Non-members still admit only through a pending
invitation.

**Email-first login (amendment).** Nobody types an organization slug to
sign in. The login flow is email-first, in the Slack and Notion shape:

1. The access screen asks for the email alone. `POST
   /auth/login/methods` answers with the methods available: whether a
   password field applies, and which of the user's organizations sign in
   through SSO. Unknown emails always get the password field, so the
   form never confirms whether an address exists; naming the SSO
   organizations pre-authentication is the deliberate, industry-standard
   leak that home realm discovery requires.
2. `POST /auth/login` verifies the password globally. A user with
   exactly one organization gets their session immediately. A
   multi-organization user gets back the organization list and a
   short-lived, single-use login ticket (`whl_` prefix, hash at rest,
   body-only, five minutes); `POST /auth/login/select` redeems the
   ticket for exactly one organization and mints the org-scoped session.
   The ticket is the smallest credential that bridges the two steps: the
   user never resubmits the password, and no half-authenticated session
   exists.
3. Whether an organization requires SSO is structural: an active
   enterprise connection on the connector is the entire policy, checked
   live per login. The `login_mode` policy column is deleted. A password
   never mints a session for a connected organization, whichever door it
   knocks on (login, ticket selection, or invitation acceptance), and
   deleting the connection restores password login by the same
   structural rule, which is also the lockout recovery path.

**Verified domains and just-in-time admission (amendment).** An
organization can claim an email domain and prove control of it with a
DNS TXT challenge (`_withhuman-challenge.<domain>`). A verified domain
belongs to exactly one organization, and consumer mailbox providers can
never be claimed. What makes domain proof valuable is exactly what the
flat no-JIT rule was guarding against: without it, an IdP could assert
any email it liked. With it, three things switch on, all conditional on
the organization currently having a live SSO connection (a verified
domain without a connection does nothing, so nobody is ever stranded):

1. **Home realm discovery.** The login methods step routes any email
   under the domain to the organization's SSO, including addresses that
   have never signed in. New hires need no provisioning ceremony.
2. **Signup capture.** Hosted self-serve signup under the domain is
   refused and pointed at SSO: the organization that proved ownership
   owns admission for its namespace, and shadow organizations are the
   thing enterprise admins buy this feature to prevent.
3. **Just-in-time admission, behind an explicit per-domain toggle
   (default off).** When the organization's own IdP asserts a verified
   email under the domain and no membership or invitation exists, the
   member is admitted through the same transaction shape as invitation
   acceptance, with the domain's default role (reviewer unless changed)
   and `admission_source: sso_jit`. Verifying a domain is a routing
   decision; letting every employee walk in unannounced is a bigger one,
   so it is a separate switch.

JIT provisions but never deprovisions: a leaver's SSO dies with their
IdP account, but the membership row stays until removed manually or by
directory sync, which remains the offboarding truth. Invitations remain
the only door for anyone outside the captured namespace.

withHuman issues its own sessions in both editions. The provider
authenticates; withHuman exchanges the assertion for its own cookie, and the
principal is resolved from the withHuman database per request. Assertion
methods and MFA claims map into session assurance, which is what finally
makes `mfa_mode` satisfiable in the hosted edition.

> **Why not vendor JWTs in the client.** The Metoro integration of Clerk is
> the cautionary reference: tenancy carried in JWT custom claims produced
> clock-skew leeway hacks, guards that block data fetching until the active
> organization resolves, duplicated race-condition workarounds, and
> email-as-join-key fragility with no stored provider foreign key. Every one
> of those bug classes is structurally impossible when the browser holds
> only a withHuman session cookie and identity linkage lives in
> `external_identities`.

### Membership management surface

Guarded by `membership.read` (list), `membership.invite` (invitations), `membership.write` (status and removal), and `membership.role.assign` (roles), all held by owner and admin; the vocabulary is [10-rbac.md](10-rbac.md)'s. These gain their first
real route checks:

- `GET /api/v1/members`: memberships joined with user display name and
  email, roles, status.
- `PATCH /api/v1/members/:id`: role changes, suspend, reactivate.
- `DELETE /api/v1/members/:id`: deprovision. Status change only; membership
  rows are never deleted because decisions and audit events reference them.
- `GET/POST /api/v1/invitations`,
  `POST /api/v1/invitations/:id/resend`,
  `DELETE /api/v1/invitations/:id` (revoke).
- `GET /api/v1/me` is enriched with display name, email, organization name,
  and roles, so the product can finally show who is signed in.

Guard rails:

- **Owner role is owner-gated.** Only an owner may grant or revoke `owner`,
  in invitations and in role edits. The API refuses cleanly before the
  last-owner database trigger would fire.
- **No self-mutation.** A member cannot change their own roles or
  deprovision themselves through this API. A separate leave-organization
  action can exist later with its own last-owner check.
- **`directory_managed` memberships are read-only here.** Once SCIM manages
  a membership, manual role and status edits are refused, so the directory
  pipeline and the UI never fight for authority.
- Suspension needs no session revocation to take effect: principals are
  re-resolved per request and require an active membership. Deprovisioning
  additionally revokes the member's sessions in that organization.
- **Escalation paths keep their reviewers.** Suspending or deprovisioning
  a member an active escalation path revision still names is refused with
  409 `membership_in_escalation_path`, the offending paths listed in the
  error details, so the administrator edits the paths first. A `force`
  flag (`force: true` in the PATCH body, `?force=true` on DELETE) overrides
  the guard for the administrator who accepts that those steps notify
  nobody until the paths are edited; the audit event then carries
  `forced: true` and the path keys. Reactivation is never guarded. The
  members page turns the refusal into a dialog that links each path and
  offers the forced action.
- Every mutation appends audit events: `invitation.created`, `.resent`,
  `.revoked`, `.accepted`, `role_assignment.granted`, `role_assignment.revoked`, `.suspended`,
  `.reactivated`, `.deprovisioned`.

### Frontend

A settings area with a Members page (list, role editor, suspend, remove), a
pending-invitations section (resend, revoke, copy link), and an invite
modal (email plus roles). A public accept page at `/invite/<token>`
handling four states: signed out and new, signed out and existing, signed
in as the invited person (one-click accept), signed in as someone else.

## Part 2: The enterprise connector

### Decision

The hosted edition self-hosts **Ory Polis** as the SAML/OIDC and SCIM
connector behind `EnterpriseSSOPort` and `DirectorySyncPort`. Image
`boxyhq/jackson` (still published under the pre-acquisition name), Apache
2.0, Node.js service plus PostgreSQL.

Why self-host instead of renting WorkOS, Clerk, Scalekit, or similar:

1. **Switching-cost asymmetry.** A hosted connector's coupling lives in
   each customer's IdP configuration: ACS URL, SP entity ID, SP
   certificate, SCIM base URL and bearer token, all vendor-issued. Leaving
   the vendor later means every enterprise customer's IT team redoes their
   setup, a cost that compounds with every connection. Leaving self-hosted
   Polis early is cheap. If self-hosting was ever going to happen, before
   the first connection is the moment.
2. **The supposed dealbreaker is absent.** Self-service setup was the
   hosted vendors' real moat. Polis ships it: expiring setup links open a
   guided per-IdP wizard (Okta, Entra ID, Google, JumpCloud, AD FS,
   OneLogin, PingOne, Rippling, generic SAML, OIDC) with copyable ACS URL
   and entity ID, and the Directory Sync setup link hands customer IT a
   per-tenant SCIM endpoint and bearer token. Verified end to end in a
   local spike (2026-08-28): SAML login against a mock IdP through the
   full OAuth exchange, and a simulated Okta SCIM lifecycle (create,
   group add, deactivate, delete) arriving as signed webhooks.
3. **Port fit.** Polis's API maps nearly one-to-one onto the existing
   ports: minting a setup link is `CreateSetupSession`; the OAuth
   authorize/token/userinfo exchange is `Begin`/`Complete`, returning
   subject, email, raw claims, and the tenant echo that binds to the
   organization; signed webhook events (HMAC, `t=,s=` format) are
   `VerifyAndNormalize` inputs feeding `provider_event_inbox`; the
   queryable directory state (users, groups, members) implements
   `Snapshot` for reconciliation.
4. **Economics and coherence.** Zero marginal cost per connection versus
   $60 to $250 per customer per month hosted ($125/mo each for SSO and
   SCIM connections at WorkOS, $75 at Clerk, ~$60 at Scalekit). An
   open-core company whose paid tier includes SSO/SCIM, running an
   open-source connector on its own infrastructure, keeps its own story
   coherent.

The endpoints, SP entity ID (`SAML_AUDIENCE`), certificates, and SCIM
tokens all live on withHuman-owned infrastructure, which closes the
provider-migration non-goal in
[02-auth-architecture.md](02-auth-architecture.md) as far as it can be
closed: replacing Polis would be withHuman's operational problem, not
customer IT's.

### Non-negotiable operational requirements

The spike falsification pass found two real weaknesses. Both have stock
fixes, and both are launch requirements, not hardening:

1. **Batch webhook mode is mandatory.** Default webhook delivery retries
   three times over roughly nine seconds, then drops the event
   permanently, and ordering is best effort. Set
   `DSYNC_WEBHOOK_BATCH_CRON_INTERVAL` so events are queued from the
   persisted log and delivered in order (maintainer-recommended
   configuration).
2. **Reconciliation sweep.** Every event is persisted with `delivered` and
   `status_code` regardless of delivery outcome and is queryable via
   `/api/v1/dsync/events`. A periodic worker re-fetches undelivered events
   and periodically reconciles full directory state against Polis's
   users/groups API. This is exactly the inbox-plus-reconciliation
   pipeline [02-auth-architecture.md](02-auth-architecture.md) already
   mandates; Polis simply proves the assumption that provider events are
   unreliable.

Additional deployment configuration:

- `SAML_AUDIENCE` set to a withHuman-owned entity ID from the first
  deployment. Changing it later invalidates customer IdP configuration.
- Telemetry off by default: `DO_NOT_TRACK=1`.
- Google Workspace directory sync is polling-based, not SCIM push:
  `DSYNC_GOOGLE_CRON_INTERVAL`.
- Horizontal scaling is unremarkable: the service is stateless and
  share-nothing; all state (connections, in-flight flow state, directory
  contents, event logs, certificates) lives in its PostgreSQL store.
  Replicas behind a load balancer share one database.
- Polis types stop at the adapter boundary, per the ownership rules in
  [02-auth-architecture.md](02-auth-architecture.md). Keeping the ports
  honest is what keeps the hosted fallback (WorkOS or Scalekit) a bounded
  adapter rewrite forever, which is what makes this decision safe.

### Risk register

- **Vendor direction.** Ory acquired BoxyHQ and clearly intends a
  commercial tier (an OEL enterprise image is built alongside the Apache
  build). Release cadence slowed after the acquisition, though the main
  branch is actively maintained (continuous security patching by Ory
  staff, features still landing from the original BoxyHQ founder).
  Separately, Ory launched Agent Security (June 2026), an agent IAM
  control plane with an "escalate for human approval" policy verdict:
  adjacent to withHuman's space today, plausibly convergent later. The
  mitigations are the same for both risks: Apache 2.0 core, fork-proven
  lineage (Polis itself is BoxyHQ's engine surviving by fork), and the
  port boundary keeping any replacement bounded.
- **Real-IdP coverage.** The spike used a mock IdP. Okta and Entra quirks
  remain the true test, mitigated by the engine's production exposure
  (Cal.com ships `@boxyhq/saml-jackson` in production; SuperTokens
  delegates SAML to it) and by the built-in SSO Traces debugging view.
- **Support burden.** Debugging a customer's SAML assertion or a silently
  stopped SCIM push is withHuman's job, with no vendor support behind it.
  At the current stage every enterprise customer is high-touch anyway;
  revisit if connection count grows past what founder-led support absorbs.
- **Stack.** A Node.js service in a Go shop: one more runtime to patch and
  monitor. Contained by the single-container deployment shape.

## Decisions and open questions

Decided here:

- Canonical invitations and memberships in withHuman, both editions; no
  vendor tenancy products.
- Delete membership status `invited`; invitations are a separate table.
- Strict verified-email match at redemption.
- Open edition remains invite-only.
- Self-hosted Ory Polis as the enterprise connector, with batch webhooks
  and the reconciliation sweep as launch requirements.
- Email-first login with server-side method discovery; no organization
  slug at sign-in. SSO requirement is the presence of an enterprise
  connection, not a policy knob (`login_mode` deleted).
- DNS-verified domains unlock home realm discovery, signup capture, and
  explicitly-toggled just-in-time admission; the no-silent-JIT rule is
  amended to no-JIT-without-domain-proof-and-opt-in.

Open:

- MFA for password-authenticated users (TOTP or passkeys, recovery codes,
  step-up). Polis is connector-only and does not provide it; SSO users
  inherit MFA from their enterprise IdP. Until this lands, `mfa_mode`
  remains satisfiable only via SSO assurance mapping.
- Account recovery for the local adapter (carried over from
  [02-auth-architecture.md](02-auth-architecture.md); the invitation token
  shape is designed for reuse here).
- Whether the hosted edition also uses Polis-fronted OIDC for
  non-enterprise login, or a plain hosted login page on the local adapter,
  until enterprise SSO is demanded.
