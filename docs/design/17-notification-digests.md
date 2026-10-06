# withHuman: notification windows and digests

Status: accepted · Last updated: 2026-10-06

> **Prototype.** Same caveat as
> [00-general-design.md](00-general-design.md): everything here can change
> without migration paths or backwards-compatibility constraints. Optimize for
> the right model.

## Summary

Every approval request runs its own workflow, and every workflow fires its
own notification steps. Nothing sees across requests, so an agent that asks
fifty times in a minute pages each reviewer fifty times on every channel
their ladder names, and the "email after five minutes" rung fires fifty
more. That is the reviewer's problem (the prompts stop meaning anything)
and ours (bulk email and text messages cost sender reputation and money).

This design puts a window per reviewer per channel underneath the ladder.
The ladder still decides who is told, when, and on which methods. The
window decides whether a due message goes out now or folds into a digest:

- The first message in a quiet period goes out at once. A lone request is
  never delayed: latency is what a human in the loop costs, and the first
  ping is what brings the reviewer to the queue.
- Messages due while the window is open are held. When the window closes,
  one digest goes out naming the requests that still need someone, and a
  longer window opens. A burst keeps widening the window up to the
  channel's longest; a window that closes with nothing held resets the
  channel to sending at once.

Defaults are fixed. Nobody configures windows until somebody asks to.

```text
t=0     request 1 due on email -> sent at once, 5 min window opens
t=0..5  requests 2..30 due      -> held
t=5     window closes           -> one digest: "29 requests waiting", 15 min window opens
t=5..20 requests 31..40 due     -> held
t=20    window closes           -> one digest: "10 requests waiting", 60 min window opens
t=20..80 nothing due            -> at t=80 the window closes empty; the next request sends at once
```

## Windows

A window belongs to one reviewer (user), one organization and one contact
kind. The kind is the unit people feel spam on and the unit sender
reputation is kept on. Two organizations keep separate windows for the same
person: it keeps every row and every send inside one organization, as the
rest of core is, and a simultaneous burst across organizations is rare.

| Kind | Windows while a burst continues |
|---|---|
| Phone app (push) | 1 min, 5 min |
| Slack, Discord | 1 min, 5 min, 15 min |
| Email | 5 min, 15 min, 60 min |
| Text message, WhatsApp | 15 min, 60 min |

The last window is the ceiling, so it is also the cap: a reviewer in a
sustained burst gets at most one digest an hour on email and on a phone
number, plus the message that opened the burst. No separate hourly cap is
needed. Core defines the windows for its own kinds (email, Slack, Discord).
An edition kind declares its windows when it registers
(`domain.ContactKind.DigestWindows`); registration refuses a kind without
them, so a new channel cannot forget to be throttled.

Urgency does not change the windows. An interrupt burst over text message
is the worst case this design exists for.

## What a window holds

- **Eligible requests.** A held request is in a digest only if it is still
  pending and nobody holds an active claim on it when the window closes.
  This is the ladder's own stop rule (a decision or any claim stops later
  rungs) applied at send time, so a digest never names something already
  handled.
- **One message for one request.** A digest that ends up naming a single
  request is sent as that request's ordinary prompt.
- **No decisions.** A digest summarises and links; it never carries
  approve or deny. Bulk approval from a notification would let someone
  approve calls they never looked at.
- **No arguments.** Like every prompt, a digest names the tool, the agent
  and the deadline, never the request arguments.

Transactional messages (invitations, verification codes, password resets)
are not approval prompts and never pass through a window.

## Storage

`notification_windows` holds one row per open window:

| Column | Meaning |
|---|---|
| `organization_id`, `user_id`, `kind` | the key |
| `level` | index into the kind's windows, 0 for the first |
| `closes_at` | when the window closes; also the token that makes a close idempotent |

`notification_holds` holds one row per request waiting in a window:
`organization_id`, `user_id`, `kind`, `request_id`, `held_at`. A hold
always belongs to the key's current window; closing the window deletes
them.

Both are working state, not history. History is the audit event below.

## Execution

The approval workflow's step runner is the one place every notification
step passes through (ladder rungs and escalation levels alike). It no
longer sends a step directly:

1. When a step is due, `AdmitNotificationStep` splits it by recipient and
   by kind and asks the store about each pair, under a row lock on the
   window:
   - no window, or a window that closed with no eligible holds (stale
     holds are cleared): open the first window and **send now**;
   - an open window, or a closed one that still has eligible holds whose
     close never ran: **hold** the request, answering the window's
     `closes_at`.

   The activity sends everything admitted for now in one dispatch and
   returns the holds.
2. For each hold the workflow sleeps until `closes_at` (inside the same
   cancellable context as the rest of its steps, so a claim or decision
   stops it) and then runs `CloseNotificationWindow` with the
   recipient narrowed to that kind.
3. The close locks the window and does nothing if `closes_at` no longer
   matches: another held request's workflow closed it first. Otherwise it
   reads the eligible holds, deletes every hold, and either moves the
   window to the next level (if anything was eligible) or deletes it. It
   commits, then sends the digest and records the audit events.

Every request with an eligible hold has a live workflow that will close
the window, so the window always closes; whichever arrives first does the
work and the rest are no-ops. A request that was decided or claimed has
stood its workflow's steps down, and it is not eligible either, so it is
not missed. A worker outage only delays the close: the first workflow to
wake closes the window late, and a new message due on an overdue window
with eligible holds is held and closes it at once.

Delivery stays at most once. The close commits before it sends and runs
with a single attempt, like every prompt; a failed digest is logged, never
retried, because a retry re-sends through every channel that succeeded.

The Slack disconnection fallback is unchanged for steps that are not yet
due, and a fallback email is sent at once (it replaces a message the
person never got). A Slack digest whose workspace disconnected while it
was held fails like any other send: it is logged, and the requests are in
the queue.

Recipients who are not people yet (an invited address with no user) have
no window and are sent to directly.

## Channels

`application.NotificationChannel` gains `SendApprovalDigest`, which
receives the organization, the narrowed recipient and the eligible
requests, newest first. The registry dispatches it like a prompt, and the
log channel logs it.

| Kind | Digest |
|---|---|
| Email | "7 approvals waiting": one line per request (tool, agent, deadline, link), at most ten, then "and 3 more", then a link to the queue |
| Slack, Discord | the same list as one message, one link per request, a button to the queue |
| Text message | one segment: "withHuman: 7 approvals waiting, 5 from deploy-bot. Review: <queue link>" |
| WhatsApp | the approval prompt template for the newest request, until a digest template is approved |
| Phone app | "7 approvals waiting", the newest tool and agent in the body, no request id, so a tap opens the queue |

Each phone-pool message counts once against the organization's pool,
recorded against the newest request. Push notifications also take the
agent as their thread identifier, so the phones stack a burst's prompts by
agent.

## Visibility

When a digest goes out, each request it names gets a
`notification.digested` audit event (subject the request; data: the
membership, the kind, when the window opened, when it closed, how many
requests the digest named). It answers "why did I only get one email for
this?" from the audit log. The hosted request timeline lists these next to
its escalation steps.

A message sent at once writes nothing new: it is what the ladder already
promised.

## Rollout

The approval workflow's activity sequence changes, so a worker running the
new code cannot replay an approval that started on the old code. Deploy
with no pending approvals, or expire the in-flight ones, as with earlier
workflow changes.

## Not in this design

- An organization-wide cap per channel. Per-reviewer windows bound volume
  to roughly members times the slowest window, and the phone pool already
  caps text messages. Add one if a single organization still hurts sender
  reputation.
- Configurable windows, and a digest-only mode ("never page me, send a
  summary every hour").
- A WhatsApp digest template (needs Meta approval).
