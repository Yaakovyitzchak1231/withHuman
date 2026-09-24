# Tool presentations

Status: implemented. 2026-09-15.

## The problem

A reviewer decides on the arguments of a tool call (`00-general-design.md`:
arguments are the ground truth, the agent never describes the action in its
own words). Until now every surface rendered those arguments generically: the
web app as a flat key/value table with a JSON toggle, the phone apps as
pretty-printed JSON, and the notification channels not at all. A Superhuman
`create_or_update_draft` call therefore showed a one-line truncated HTML
string where a reviewer needed to read an email, and a Claude Code `Edit`
showed two strings where a reviewer needed a diff.

## The decision

A **tool presentation** is provider-owned data that says how a tool's
arguments are shown: which argument is an email body to render, which is a
shell command, which pair of strings is a change, and what to call the
request. It never comes from the agent, for the reason `summary` and
`changes` were removed from the protocol: nothing an agent sends may shape
how a reviewer perceives the call except the arguments themselves.

Two rules hold whatever a presentation says:

1. **Nothing is hidden.** An argument the presentation does not name still
   shows, after the named ones, rendered by its shape. A presentation may
   fold a field into the details, never remove it. Otherwise a presentation
   becomes an attack surface: an agent adds a `bcc` the renderer never
   mentions.
   This also applies inside list items: extra keys remain visible beside
   the rendered item. A partial nested path keeps its containing value.
   Incomplete or non-string diff pairs fall back to the original fields.
2. **The raw JSON stays one click away** on every surface.

Presentations are declarative, so every client interprets the same
specification and renderers are data rather than code in three codebases.

## The specification

```jsonc
{
  "title": "Edit {{/file_path}}",
  "fields": [
    { "path": "/file_path", "kind": "path", "label": "File" },
    { "kind": "diff", "before": "/old_string", "after": "/new_string", "label": "Change" },
    { "path": "/edits", "kind": "list", "item": { "kind": "diff", "before": "old_string", "after": "new_string" } },
    { "path": "/replace_all", "kind": "text", "role": "detail" }
  ]
}
```

- `kind` is one of `text`, `prose`, `markdown`, `html`, `code`, `shell`,
  `diff`, `path`, `url`, `email`, `reference`, `enum`, `datetime`, `list`,
  `json`. The vocabulary is closed and validated server side
  (`toolpresentation.Kinds`).
- `path`, `before`, and `after` are RFC 6901 JSON pointers into the
  arguments. A diff is either a patch as text at `path` (unified diff,
  Codex's `apply_patch` envelope) or the pair at `before` and `after`,
  which the client lines up itself. Inside a list's `item` the pointers
  are relative to the element.
- `role` is `primary` (the block a reviewer reads first) or `detail`
  (folded away underneath).
- A field whose path is absent from a particular call is skipped, so a
  presentation written for one shape of a tool's payload can only fail to
  help, never invent anything, for another.
- `title` may interpolate scalar arguments with `{{/path}}`. Clients set
  interpolated values apart in mono, so a reader can tell the catalog's
  words from the agent's, and fall back to the humanised tool name when
  every placeholder is absent.

## Where presentations come from

- **The shipped catalog** (`withhuman_core/toolpresentation/builtin.go`):
  the built-in tools of every runtime in `agentruntime` (every shell and
  edit tool is covered, pinned by a test) and the MCP tools whose names are
  distinctive enough to recognise (Superhuman, GitHub, Slack, Linear,
  Stripe, the reference filesystem server).
- **The organization's own entries** (`tool_presentations` table,
  `/api/v1/tool_presentations`, the Tool presentations settings page).
  Reading needs `organization.read`, writing `organization.settings.write`,
  and every change is audited as `tool_presentation.created`, `.updated`,
  or `.deleted` with the binding, never the specification.

A binding is `(runtime, server, tool)`: the tool name is exact, an empty
runtime or server matches any. Resolution takes the most specific match, a
server binding counting for more than a runtime one (the server defines the
tool, the runtime only carries it), and an organization entry always beats a
shipped one however specific the shipped one is. The runtime is the
`runtime` the adapter recorded in `context`, else the request's agent slug
(onboarding gives each runtime's agent the runtime key as its slug, so the
fallback still finds the runtime's shipped entries);
the server is the request's `server`, the AAP tool identity field.

## Where the resolution happens

The server resolves the presentation on every product read of a request
(`internal/toolpresentations.Service`) and attaches it as `presentation` on
the `QueueApprovalRequest` representation, with `source` (`built_in` or
`organization`) and the organization entry's `id`. The AAP representation
never carries it: agents have no use for it, and it is a reading aid, not
part of the action. Organization entries are read through a five second
cache because the queue stream re-lists every second.

## What the clients do

`withhuman_core/frontend/src/app/tool-presentation.ts` is the reference
planner; the iPhone and Android apps port it. When no presentation names
the tool, or for arguments no field names, the clients infer a kind from
the value where they can (an HTML fragment, a unified diff, a URL, an
address, an ISO instant) and from the key otherwise (`command`, `*_id`,
`type`, `*_path`), so a tool nobody catalogued still reads better than JSON.

Rendered HTML is the one kind with a security surface: it is shown in a
frame with every sandbox restriction on and a policy that loads nothing
(no script, no image, no font, no navigation), so a tracking pixel in a
draft never fires from the review page, and the markup is one toggle away.
The web preview also copies only structural HTML into its frame, removing
author styles, active elements and URL attributes. Native web views reject
navigation and block all resource loads independently of the page policy.

The web app's request page tells people who can change organization
settings which presentation drew the arguments and links to editing it or
customizing the shipped one, with that request's arguments as the sample.
The editor's preview runs the same planner as the request page.

## Not done, on purpose

- Notification channels still carry the tool name and never the arguments,
  presentation or not; a rendered title would leak argument values into
  mail and chat.
- Agent- or adapter-supplied render hints, provider-side model summaries,
  and uploaded code renderers were all considered and rejected: the first
  for the trust reason above, the second because a hallucinated summary in
  an approval product is worse than JSON, the third because it is a
  security and three-platform problem the declarative vocabulary avoids.
- The catalog is curated in core; a community contribution flow can come
  with the open-source release.
