# Specialisations (two-level inheritance)

How an agent that hangs off a base role behaves: the two-level cap, what a child
inherits and what stays its own, the runtime-follow flag, the response fields
that expose the link, and the solidify / archive escape hatches.

Creating the agent itself — fields, secrets, MCP config and skill binding —
belongs to `references/agents.md`; the create call there accepts
`parent_agent_id` and `runtime_inherited` alongside its other fields.

- [What is inherited](#what-is-inherited)
- [Response fields](#response-fields)
- [Solidify and the archive guard](#solidify-and-the-archive-guard)

A **base role** is an agent with `parent_agent_id IS NULL`. A **specialisation**
(中文：特化) hangs off exactly one base role and stores only its difference.
Inheritance is LIVE, not a copy: nothing is snapshotted at attach time, so
editing the base role's prompt or skill bindings reaches every specialisation on
its NEXT task.

The tree is hard-capped at TWO levels — a specialisation can never be a parent.
Both mistakes are refused on the write that would create them: pointing an agent
at a parent that is itself a child, and giving an agent a parent while it
already has children of its own. A base role must be active to be attached to
(restore it first), and the caller must be able to VIEW it — an unviewable
parent answers "not found", exactly like a missing one, so attaching cannot be
used to probe for private agents.

Attaching is not a create-only decision: an agent that already exists is
re-parented with the same field, on `PUT /api/agents/{id}`. `parent_agent_id`
there is a tri-state keyed on the field being present in the body — absent means
no change, `""` detaches, an id attaches.

From the CLI:

```bash
multica agent create --name "..." --parent-agent-id <base-role-id>              # follows the base role's runtime
multica agent create --name "..." --parent-agent-id <base-role-id> \
  --runtime-id <id> --runtime-inherited=false                                  # owns its own runtime instead
multica agent update <id> --parent-agent-id <base-role-id>   # attach or re-point
multica agent update <id> --parent-agent-id ""               # detach, DROPPING the inherited prompt
multica agent update <id> --runtime-inherited=false          # stop following; keep what it runs with now
multica agent solidify <id>                                  # detach, KEEPING it (see below)
```

From the UI, the base-role picker on the agent detail page's Instructions tab
does the same two writes: choosing a base role attaches, choosing "independent
base role" routes through solidify so the running behaviour does not change.

## What is inherited

Inherited, and read-only on the child:

- **Prompt.** The effective prompt is the parent's `instructions` + `"\n\n"` +
  the child's `instructions` — pure append. A child cannot override or delete a
  parent paragraph. An empty half contributes nothing at all, so an empty parent
  leaves the child's own text untouched rather than orphaning a separator.
- **Skills.** The effective set is the UNION of both rows' enabled bindings,
  deduplicated by skill id, with the base role's bindings first as a stable
  prefix. v1 keeps an inherited skill read-only on the child: it cannot be
  disabled or removed there.
- **Runtime configuration** (DENE-505), unless the child opts out: `runtime_id`,
  `runtime_mode`, `runtime_config`, `model`, `thinking_level` and `service_tier`
  are a COPY of the base role's, taken when the child is created, when a base
  role's profile changes, when the child is re-parented, and when it is switched
  back to following. The copy is materialised into the child's own row, so
  dispatch needs no parent lookup — but unlike the prompt it is a copy, not a
  live read: a base-role edit that happens while the child is archived reaches
  it on restore, not before, and a direct SQL edit of the base role's columns
  reaches it on the next API write.
  - `runtime_inherited` is the flag. True = following. Every specialisation
    created through the API follows by DEFAULT, which is also why `runtime_id`
    may be omitted on create — and why a `runtime_id` sent alongside
    `parent_agent_id` is NOT an override (`runtime_inherited: false` is).
  - Setting it false is lossless: the child keeps exactly the values it was
    running with, the runtime counterpart of a solidified prompt. Setting it
    true re-copies the base role's profile immediately.
  - A base role cannot inherit (`runtime_inherited: true` without a
    `parent_agent_id` is a 400), a following child refuses runtime edits in the
    same request (400 — pass `runtime_inherited: false` first), and detaching
    from the base role clears the flag.
  - A child can only follow onto a runtime its owner may use: the base role's
    runtime must be public or owned by the agent's owner (the same rule the
    daemon claim enforces). Creating a specialisation of a base role whose
    runtime is another member's private runtime therefore needs an explicit
    `runtime_id` — which lands as an independent runtime, not an error.
- **Execution config** (DENE-854): `custom_env`, `custom_args` and `mcp_config`
  ride the same flag and the same copy points, but only when the child and the
  base role have the same owner — env and MCP config carry the base role's
  credentials, and another member's follower must not reveal them through its
  own env endpoint. A same-owner follower refuses edits to them (`PUT
  /api/agents/{id}` with `custom_args`/`mcp_config`, and `PUT
  /api/agents/{id}/env`, are 400); edit the base role, or set
  `runtime_inherited: false` first. Across owners they stay the child's own.

Everything else stays INDEPENDENT per agent — a specialisation is the same role
with its own configuration, not a clone. In particular `max_concurrent_tasks`
and invocation permissions are set separately on the child and are NOT
inherited from the parent, and the child does not override the parent's values
either.

`work_enabled` is the one exception, and only in one direction. Turning a base
role off also turns off its direct specialisations. Turning the base role back
on does not turn them back on; each specialisation keeps its own switch after
that and can be turned on by itself. Turning a specialisation off does not
turn off its base role or its siblings.

The prompt and skills are recomputed on every claim, so a change on either side
of the relationship lands on the specialisation's next task with no re-attach
step. The runtime copy lands through the write that changed it (create, attach,
re-parent, flip, or a base-role runtime edit).

## Response fields

| Field | Present on | Meaning |
|---|---|---|
| `parent_agent_id` | list + detail | the base role's id; empty for a base role |
| `parent_agent_name` | list + detail | the base role's display name, so a client can name it without a second request |
| `child_count` | list + detail | active specialisations hanging off this agent; always `0` for a specialisation |
| `runtime_inherited` | list + detail | true when this specialisation follows its base role's runtime configuration (see above); always false for a base role |
| `inherited_instructions` | detail only | the base role's own `instructions`, verbatim |
| `inherited_skills` | detail only | the base role's skill bindings, read-only |

`inherited_instructions` is served verbatim rather than pre-composed with the
child's text, because the detail surface renders it as its own read-only block —
"this half came from the base role" is the information. It is subject to the
SAME view gate as the base role: a viewer who can see the child but not a
private base role still gets `parent_agent_id`, and an empty
`inherited_instructions` — never the base role's text. So an empty inherited
prompt means "nothing is inherited, or you may not see it", with no separate
check needed.

## Solidify and the archive guard

`POST /api/agents/{id}/solidify` (固化并解绑) is the escape hatch for "this base
role is going away". One transaction writes the CURRENT effective prompt into
the child's own `instructions` and clears `parent_agent_id`. The frozen text
comes from the same composition the claim path uses, so what gets written is
exactly what the child had been running with. The child becomes a base role
itself — it can then be specialised in turn — and its skills are untouched,
because only the prompt was ever inherited as text.

Every refusal is a 409, not a 400: the request is well-formed, the child is
simply not in a state where solidifying means anything (it has no parent, the
parent is gone, or the parent has no instructions). A caller who may read the
child but not its base role gets a 403 and must detach instead (`PUT
/api/agents/{id}` with `parent_agent_id: ""`, which drops the inheritance
without copying anything).

Archiving a base role that still has ACTIVE specialisations is refused with 409
`agent_has_children`; the body carries `children`, a list of the blocking
specialisations' **names**, so a client can show what is in the way and offer a
per-child solidify without a second request. Archived specialisations do not
count — they no longer run, so they block nothing.
