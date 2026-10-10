# Routing

Automatic dispatch is off unless the workspace turned it on in Settings →
Routing. Once it is on, the server asks one question on issue creation and on
every status change: given this status, who should be holding this issue. It
answers by filling slots. It has exactly one status write, described under
「停滞巡检」 below, and that write can only ever align a status to an acceptance
the reviewer already gave on the ticket — routing never decides for itself that
work is finished.

What it may do, and only when the slot is still **empty**:

- **Top-level issues only own acceptance.** An issue with `parent_issue_id` is
  a sub-issue: it may receive an executor at `todo`, but it never gets an
  independently routed 验收席, never enters an acceptance handoff, and is not
  considered by the stale-review sweep. Its terminal result feeds the parent
  stage/barrier; the parent is the single issue that later enters `in_review`
  for a unified review of the full child tree.
- **`todo`** — fill the assignee with a seat from the tier ladder (seats with
  `dispatch_mode: mention_only` are never on it, whatever their tier, and a
  seat with `dispatch_projects` set is on it only for those projects' issues). For a
  top-level issue, also fill the issue's 验收席 with a seat or 「不需要验收」.
  **Routing never writes a person
  into 验收席**: an issue a person holds is one routing never touches again, so
  a person in that slot freezes the issue there. When the judge decides the
  acceptance needs a human call, the slot still gets a seat and the decision
  comment tells that seat to @ the person instead.
- **`in_review` (top-level only)** — hand the issue to the seat in 验收席
  (which starts its run). To wake that seat by hand, use `multica issue
  handoff <id> --to reviewer` — never a hand-written @验收席.
  「不需要验收」 is left alone. A 验收席 a person filled with a **person** is
  notified with an @ and a subscription, and the issue is **not** reassigned —
  whoever is holding it keeps it, so its status can still be moved.
- **`blocked`** — post one advice comment and @ somebody. **No value is
  changed.**
- **`in_progress` / `done` / `cancelled` / `backlog`** — nothing at all. A
  ticket meant to start now must be `todo`; an agent putting one in `backlog`
  has to name what it waits for (`--waiting-for`), or the server refuses.

There is a fourth trigger that is not a status change. A top-level ticket sitting
in `in_review` with nothing happening on it and no run working on it is **stalled**,
and a periodic sweep looks at it once the quiet passes the workspace's stall
threshold (Settings → Routing, default 24 hours):

- The default is to **wake the 验收席 and change no status** — reassign the seat,
  which starts its run, or tell a named person once and @ them.
- The status is moved to done **only** when the 验收席 already left a pass verdict
  on the ticket, and the comment recording it says which remark it read. With
  nothing from the reviewer on the ticket this branch cannot be taken at all.
- The sweep does not merge, and it is not the release path. The release path is
  the acceptance seat's pass: a comment with a standalone `verdict: pass` line
  (`multica issue comment add <id> --verdict pass`) makes the platform merge the
  open linked PR and set `done` right away. Waiting for this sweep leaves the PR
  open.
- A top-level ticket that reached `in_review` before its 验收席 was ever decided gets the
  slot filled now and is handed on — this is the same row as `in_review` above,
  and it runs even when the assignee is a person.
- With routing switched off, the sweep does not run.

The same sweep also backfills quiet legacy `todo` issues whose executor or
reviewer slot is empty. It only considers `todo`, skips backlog, blocked, and
human-held issues, and shares a 25-issue per-workspace budget with the
stale-review row. Re-running it is safe because `Route` only fills empty slots.

Routing-owned fields follow three gates: a downstream consumer must read the
field; the value must be deterministic or verifiable (otherwise it stays
empty); and the existing fill-only, conditional-write, one-comment rules
remain in force. Projects are inherited from a parent issue, or from the one
project attached to the source issue/chat of an agent run; multi-project chats
remain empty. A child with priority `none` inherits its parent's priority.
Labels and due dates are not routed, and status remains governed by server
gates.

验收席 is a native **top-level issue** field, not a workspace property:
`reviewer_type` + `reviewer_id` on the issue, shaped exactly like `assignee_type` +
`assignee_id`, plus one extra type. Set it by hand with

```bash
multica issue update <issue-id> --reviewer <member-or-agent-name>
multica issue update <issue-id> --reviewer none   # 不需要验收
multica issue update <issue-id> --reviewer ""     # back to undecided
```

Because it is a reference and not a copy of a name, renaming the agent or
person it points at changes nothing on the issue, and archiving them releases
the slot so routing can pick a live seat next time the issue moves.

## Who picks the executor (DENE-1033)

**Do not name an executor yourself — leave the slot empty and routing picks.**
With routing on, an executor an agent writes on a `todo`/`backlog` ticket
(`multica issue create|update|assign --assignee X`, `--to X`) is ignored: the
server does not write it, routing judges from scratch and says in one sentence
that a pick was ignored, without saying who was suggested. The CLI prints the
same hint on stderr and `--output json` carries `assignee_ignored: true`. A
tier label you attach (e.g. `strongest`) is ignored the same way; only a label
a person attached counts.

The only exception is words from the person you are talking to. When THAT
person said it out loud earlier in the same direct chat or issue thread, pass
their exact words (the server checks the original message and its author):

```bash
multica issue create --title "..." --assignee "贝吉塔游戏" --per-quote "这张交给贝吉塔游戏做"
multica issue assign <id> --to "贝吉塔游戏" --per-quote "交给贝吉塔游戏"
```

The server checks all three: the quote is a passage of an earlier user message
in that direct chat or issue thread; that message was written by the person who
started the run (any member, not a particular one); and the quote contains the
assigned agent's name. In a direct chat, “你来做”, “你自己做”, and “指派给你”
are accepted as self-assignment when the target is the current agent. If it
passes, the ticket records「按 <名字> 原话指派」and the pick stands — with one
domain rule: a quote that names only a base role (「交给孙悟空」) on an issue
whose scene has a domain lands on that base role's 对口 specialisation
(孙悟空出海 on an 出海 issue or project); a quote that names the specialisation itself is
kept as said. If it does
not — a made-up quote, someone else's comment, another agent's relay, or a
quote without the name — it counts as no quote: the pick is set aside, routing
fills the slot, and the response and stderr say so. Nobody waits on you; do
not go back to the person for words. If the person names someone later,
reassign with `--per-quote` then. When the person never named anyone, create
the ticket without an executor. Do not quote comments from third parties or
other agents: they never count.

A seat routing filled is a stand-in. When a person's decision replaces it —
their own hand, or their words through `--per-quote` — the runs that seat's
assignment started are cancelled, so two seats never work the same ticket. Its
other runs (a mention, a squad) are left alone, and so is the run making the
request. A cancelled run that had started keeps its work on its
`agent/agent/<issue>` snapshot branch. Replacing a seat a person chose cancels
nothing.

Past `todo` the rule is stricter: on a ticket that is `in_progress`,
`in_review`, `blocked` or later, an agent cannot put a **different** agent or
squad in the executor slot at all. The slot keeps its current holder, the
response carries `assignee_ignored: true` and an `assignee_ignored_reason`
naming the way out, and the CLI prints it on stderr. The ways out:

| You want | Run |
|---|---|
| a stronger seat | `multica issue escalate <id> --reason "..."` |
| advice from a stronger seat, keeping the ticket | `multica issue consult <id> --question-file <path>` (`references/consult.md`) |
| someone else to take it | `multica issue close <id> --outcome blocked --evidence-file <path> ...` — routing advises |
| a person to decide | `multica issue summon <id> --to <member> --reason "..."` |
| the person already named the new owner | `--per-quote "<原话>"`, checked as above |

Re-sending the executor already in the slot, handing the ticket to a person,
and the server's own moves (acceptance handoff, quota relay, reviewer relay,
`issue handoff`) are not affected. A person reassigning by hand never is.

Every 「自动选派」 comment opens with one line, **为什么是他**, naming the source
of the executor: `原话` (a person's verified words), `人工` (a person or their
automation put it there), `档位` (routing's tier ladder, or a tier label a
person attached), `兜底` (the verdict was too weak, so the fallback rung),
`接着做` (the seat that did the related earlier work continues it), `负载`
(the ladder's seat was busy, so a less busy seat of the same rung and
direction took it).

**接着做 (DENE-1202).** A ticket continuing earlier work — a sibling one stage
earlier under the same parent, the parent itself, or a ticket created by the
same agent run — goes back to that work's executor when the seat is on the
judged rung or stronger, online, not disabled, not out of quota, and generic
or in the ticket's direction. It ranks after the person's words and a person's
hand (including a person's tier label), and before the tier ladder. It is a
workspace switch, **off by default = shadow mode**: routing writes its own
pick and only adds a line to the 「自动选派」 comment saying who the rule would
have picked, or why no earlier executor qualified. Check or flip it without
the browser:

```bash
multica workspace routing get                     # prefer_continuation, continuation_mode: shadow | on
multica workspace routing set --continuation on   # or off to go back to shadow
```

The same commands also expose the seat-table switches. `usage_priority` is
shown as `true` when omitted (the web and server default it on), and
`allow_upshift` defaults to `false`:

```bash
multica workspace routing get
multica workspace routing set --usage-priority off
multica workspace routing set --allow-upshift on
```

Both flags write the existing `settings.routing` fields used by the web
settings page; they do not create a CLI-only policy.

The routing model's confidence floor and the stale-review sweep window are
also part of the same settings block. `confidence_threshold` accepts a value
in `(0, 1]`; `stale_review_hours` accepts a positive number of hours up to one
year. The get command prints the effective defaults (`0.6` and `24`) when a
legacy workspace has no saved value:

```bash
multica workspace routing get
multica workspace routing set --confidence-threshold 0.75
multica workspace routing set --stale-review-hours 48
```

These flags update `settings.routing.confidence_threshold` and
`settings.routing.stale_review_hours`, the fields used by the settings page,
server routing, and the stale-review sweep.

So when you split work into stages, leave each child to routing: the next
stage reaches the seat that did the previous one by itself once the switch is
on. Do not assign it by hand to get the same effect.

**负载分流 (DENE-1203).** Without it, routing always takes the first seat of
its rung × direction cell, so a batch of independent tickets lands on one
seat. With it, the cell's seat with the fewest unfinished runs (queued or
running) takes the ticket; seats that cannot take work are skipped, and when
all are equally busy the usual order stands. It is a separate workspace
switch, **also off by default = shadow mode**: the 「自动选派」 comment only
says 「按新规则会选 X（负载：Y 正在跑 N 个活…）」. With both switches on,
接着做 wins.

```bash
multica workspace routing get                # prefer_idle, load_mode: shadow | on
multica workspace routing set --load on      # or off to go back to shadow
```

So create independent tickets in one batch and leave them to routing; do not
hand-assign them to different seats to spread the load.

**从结果里学 (DENE-1722).** A ticket the rule table tiered is remembered by
class: direction × tier × scope / clarity / risk. `issue escalate` and an
acceptance `verdict: hold` mark it judged low. Once a class has at least 5
tickets in the window and the judged-low share reaches the threshold
(defaults 30% over 30 days), its next ticket goes one rung higher. Also **off
by default = shadow mode**: the comment only says 「按新规则会上调一档——同类票近
30 天 4/10 张判低…」.

```bash
multica workspace routing learning --output json   # classes, counts, would-raise
multica workspace routing set --learn on           # or off to go back to shadow
multica workspace routing set --learn-low-rate 0.4 --learn-window-days 14
```

So when a ticket is beyond your seat, say so with `issue escalate`; that call
is what teaches routing, a comment saying "too hard" is not.

**按判断配验收 (DENE-1252)** has no effect since DENE-1677: the rule table
below decides whether a ticket gets a 验收席. `--judged-review` is still
accepted so old configs read, and changes nothing.

If a ticket turned out too hard for its seat, do not pick a stronger one.
Ask routing to re-judge:

```bash
multica issue escalate <id> --reason "what is beyond this seat"
```

It takes a reason only — no person, no tier. The server moves the ticket up the
ladder, writes one comment, and reports `at_top: true` when nothing is
stronger. A person picking on the web or desktop, or an automation a person
configured, is never second-guessed.

Consequences for how you work:

- A value you set yourself is never overwritten. Assigning an issue, or
  filling 验收席 by hand, permanently opts that slot out.
- An issue whose assignee is a **person** is not touched in any way — no
  slot, no comment, no mention. The one exception is an issue in `in_review`
  whose 验收席 has never been decided: that slot is still filled and the issue
  still handed on, because「这个人在干活」and「这个人在验收」are different
  situations. The status is untouched either way.
- Routing comments are capped at one of each kind per issue, so flipping a
  status back and forth does not re-dispatch or re-notify.
- Every slot routing writes shows up in `multica issue timeline`, so a routed
  owner is distinguishable from one a person set.
- `multica issue route <id>` re-runs the same pass by hand and prints what it
  did — the same code the hooks run.
- Read `action` in `--output json` as what was WRITTEN. `assigned`: a slot was
  filled. `declined`: nothing was written, which now means somebody else won
  the write. `noop`: nothing to decide. Only `assigned` is a dispatch.
- **The tier comes from the rule table, not a model (DENE-1677).** The
  analysis model answers four numbered questions (改动范围, 需求, 出错代价,
  要人拍板); only the option number is read. The first matching row of the
  table names the tier and whether a 验收席 is needed. An answer that is not a
  number in range, a timeout or a garbled reply counts as 答不出, and 答不出 or
  cross-module never lands on the weakest rung. The reviewer seat follows the
  executor: same rung from another model family, else one rung down. A judge
  answer below confidence threshold is ignored and the table stands.
- The issue's **scene** decides which specialisation on a rung gets the
  work; it never changes the rung or the confidence. The scene is the issue's
  own domain; else all of its project's domains; else generic. Every
  automatic pick (executor, 验收席, quota relay, re-dispatch, escalate,
  suggest, the quoted base-name swap) groups agents the same way: **对口**
  (`match`, a specialisation for a scene domain — in a generic scene, a base
  role) first, **通用** (`generic`, a base role) as the fallback, **其他**
  (`other`, a specialisation for another domain) last. A project with no
  domain is generic and the dispatch comment says 「方向：通用」. See the
  groups for any work with `multica agent list --for-issue <key> | --for-project
  <id> --output json` (each agent gets `fit`, sorted by it). Domains are one
  workspace list (`multica domain list | add | rename | delete`); a project
  carries several (`multica project update <p> --domain 出海 --domain 自媒体`),
  an issue picks one of its project's (`multica issue create|update --domain
  自媒体`, filled automatically when the project has exactly one; `--domain ""`
  clears it). A project with no domain falls back to the name table:
  `multica workspace routing-projects list | set <project> <domain> | unset
  <project>` (exact name or `prefix*`; a workspace domain, or `通用`).

When routing is **not working** — the model was rejected, is unreachable, the
breaker is cooling down after repeated failures, or the deployment never
configured a server-internal LLM — issues are left entirely
alone, exactly as if routing were off. Nothing is posted on a ticket about it.
The reason is shown in one place only: Settings → Routing, which reports the
state, the reason, when the model last answered, and offers a re-check. If
automatic dispatch seems to have stopped, that section is where to look.

Read the table without the browser (`--output json` for the same shape the
settings page reads):

```bash
multica workspace routing rules
```

With the judge on (mode `judge` or `both`) it may only **raise** the table's
tier by one rung, with a reason; a lower or two-rung answer is not used, and
the decision comment says so. `multica issue route <id> --output json`
reports `tier` (used), `judged_tier` (set only when the judge's answer was
held to the table), and `trace`: the questions with the raw reply and the
number read, the rule row, the judge's effect (`raised` / `agreed` /
`ignored` / `failed`) and the tier. The decision comment carries the same
trace in prose.

Agent-created tickets should pass `--routing-facts` with scope, clarity, risk,
and needs_human (plus an optional summary). The creator facts are accepted
immediately and skip a duplicate analysis call. The analysis source can be
changed without the browser: `multica workspace routing set --source
runtime_subscription --runtime <id> --model <id> --thinking low`. Use
`api_gateway` to retain the OpenAI-compatible path.
