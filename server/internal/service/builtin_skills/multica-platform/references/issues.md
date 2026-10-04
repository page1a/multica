# Issues

Product contracts the runtime brief does not fully encode.

- [PR linking](#pr-linking)
- [Reading a linked PR's real state](#reading-a-linked-prs-real-state)
- [Custom properties: typed workflow state](#custom-properties-typed-workflow-state)
- [Status changes have server side effects](#status-changes-have-server-side-effects)
- [Who else is running right now](#who-else-is-running-right-now)
- [Sub-issues: todo starts work now, backlog parks it](#sub-issues-todo-starts-work-now-backlog-parks-it)
- [Charts and files in a comment](#charts-and-files-in-a-comment)
- [Incorrect to correct](#incorrect-to-correct)

Closing is its own contract; read `references/close-protocol.md` for its `close.*` keys, decision tables, and dispatcher promotion rules.

Create a goal task with `multica issue create --title "..." --goal`. This
creates a draft completion line with starter checks; a human must edit and
confirm it through the shared goal panel before execution begins.

`multica issue wait <id> --output json` is the read-only status view for a
blocked issue's wait. It reports the wait condition, optional `wait_probe`,
deadline, last probe status (`ready`, `pending`, or `failed`), check timestamp,
and bounded output. A probe uses exit code `0` for ready, `10` (or GitHub CLI's
`gh pr checks` code `8`) for pending, and
any other code for failed. Do not add a follow-up stage-advance command after a
close; the server advances stages as part of its close protocol.

To move several issues to one lifecycle status at once, use the same batch
endpoint as the web and desktop inbox actions:

```bash
multica issue status-batch done DENE-12 DENE-13 --output json
multica issue status-batch todo DENE-12 --no-start
```

The first argument is the status key and the remaining arguments are issue keys
or UUIDs. `--no-start` adds the same `suppress_run` control as single-issue
status changes. `--output json` returns the resolved issue IDs and server batch
result. `batch-status` is accepted as an alias.

## Sub-issues: todo starts work now, backlog parks it

The steps are in `references/sub-issues.md`. `--status backlog` parks a child instead of starting it. `` `--stage <N>` `` groups children into a stage, and the parent is woken when a whole stage finishes. Promote one parked child with `multica issue status <child-id> todo`.

## Editing comments without overwriting concurrent work

Read the comment's current `revision`, then supply it when updating. Agent
bodies must use `--content-file`.

```bash
multica issue comment list <issue-id> --output json
multica issue comment update <comment-id> --content-file ./comment.md --expected-revision <revision>
```

If another editor changed the comment, reconcile the latest body before retrying; do not advance the revision and overwrite it. Authors, workspace owners, and admins can edit comments; attachments remain unchanged.

## PR linking and close intent are two distinct contracts

To attach a local file to an existing issue description, use `multica issue update <id> --attachment <local-path>`. The CLI appends the file's Markdown reference to the end of the description; to replace an image, also use `--description-file` to remove the old reference. Do not put local filesystem paths in the description.

## PR linking

A PR is linked to an issue when its **title** or **branch name** contains a
routable issue key (`PREFIX-NUMBER`, e.g. `MUL-123`), or when its title or body
puts the key **right after a closing keyword** (`Closes` / `Fixes` /
`Resolves`, optional `:` then whitespace). A key that appears in the body as a
bare mention links nothing. People can also link a PR by URL or remove one on
the issue page; a removed PR is not linked again by later webhooks.

```text
MUL-123: add the thing the issue asks for     # key in title  → links
agent/dana/mul-123-add-the-thing              # key in branch → links
Closes MUL-123   (body)                       # key after a keyword → links
Related to MUL-123   (body only)              # no link
```

While a PR is open, its automatic links follow the live title, branch, and
body: removing the key drops the link. After merge or close, existing links stay.

### Default for code-changing issue work

When an issue run changes code in a checked-out GitHub repo, the default handoff
is to open or update a PR before posting the final Multica issue comment, unless
the user explicitly asked for a local-only change or no PR. This is a default, not
an unconditional command: if no code changed, say no PR is needed; if PR creation
is blocked by auth, failing tests, or missing remote state, report that blocker
instead of pretending the run is complete.

To make the PR show on the issue, put a routable issue key in the PR **title**
(preferred) or the **branch**. A key that appears only as a bare mention in the
body links nothing.

```text
MUL-123: fix login redirect        # key in title → links
Part of MUL-123                    # body mention only → no link at all
```

In the final issue comment, include the PR URL when a PR exists. If the task did
not produce a PR because no code changed or the user asked not to create one, say
that explicitly.

## Reading a linked PR's real state

When a step depends on PR state, query Multica's link table — do not infer it
from branch names, GitHub search, memory, or stale values left on the issue by
an earlier run.

```bash
multica issue pull-requests <issue-id> --output json
```

Returns `{"pull_requests": [...], ...}`. Each element of `pull_requests` exposes:

- `number`, `html_url`, `title`
- `link_source` — why the PR is on the issue: `title`, `branch`, `manual`, or
  `auto` (any other automatic link, such as a closing keyword in the body).
- `state` — the PR lifecycle as a **single enum**, one of `merged`, `closed`,
  `draft`, `open`. There is no separate `draft` or `merged` boolean in the
  response; the server folds them into `state` (merged wins, then closed, then
  draft, else open).
- `merged_at` — non-null once merged; a second confirmation of `state: merged`.
- `provider` — `github`, `forgejo`, `gitea`, or `gitlab`.
- `mergeable_state` — mirrors GitHub (`clean` / `dirty` surfaced; other values
  round-trip as unknown; retained for compatibility).
- GitHub API snapshot fields: `snapshot_available`, `mergeable`,
  `merge_state_status`, `checks_rollup`, `checks_total`, `checks_passed`,
  `checks_failed`, `checks_running`, `failed_check_names`,
  `snapshot_fetched_at`, and `snapshot_stale`. `snapshot_available == true`
  means the feature is enabled and the snapshot matches the PR's current head.
  Only then does `checks_rollup == null` mean "no checks"; false means the
  snapshot feature is disabled, has not fetched yet, or only has an old head.
- `checks_conclusion` — coarse CI compatibility status: `passed`, `failed`,
  `pending`, or `null`. GitHub derives it from the current API snapshot;
  Forgejo/Gitea/GitLab derive it from webhook commit statuses. Backed by the
  provider-appropriate check counts.

So "is it merged?" is `state == "merged"` (or `merged_at != null`); "is it still
a draft?" is `state == "draft"`; coarse CI status is `checks_conclusion`.

The response may also include `gap` when nothing linked is merged or open
(DENE-961). `gap.reason` is `no_connection` (no token and no GitHub App the
server can query for that repository), `not_found` (a connection exists, but
no pull or merge request title contains the ticket), or `not_merged` (one was
found and it is still open or a draft). `gap.message` says which, and
`gap.next_command` is the command to run. The server fills this by reading
linked rows first and, when there are none, querying the repository
connection with the ticket key.

Do not block the ticket on “等待平台关联 PR” or any equivalent. That wait is
rejected. Declare the link on the close instead:

```bash
multica issue close <id> --outcome done --pr <pull-or-mr-url> --evidence-file ./close.md
```

The server checks the URL once. When the check succeeds it registers the pull
request and continues the normal gate. When the check cannot be done, the
close still proceeds and the ticket records `close.pr_unverified` (未核实).
An unverified link is not merged.

If the command returns no linked PRs after a PR was opened, check the syntax
first: the key must be in the PR title or branch, or right after a closing
keyword in the body — a bare body mention does not count. When the syntax is the
problem, editing the title re-runs the scan. If a person removed
the PR from the issue, it stays removed until someone links it again.

If the key is already written correctly and the list is still empty, stop editing
the PR blind: another no-op edit cannot fix an integration that never received the
event. Check the integration side instead — whether the app is installed on that
repository, whether the installation is bound to this workspace, whether
auto-linking is turned off for the workspace, and whether the event reached the
platform at all. A delivery that failed is not retried on its own, but it can be
redelivered once the receiving side is fixed. Report what you found in the result
comment rather than repeating the edit.

## Listing and ordering issues

`issue list` reads one page at a time, with a server maximum of 100 issues.

Pass `--goal` to keep only issues that have a completion-line goal:
`multica issue list --goal --output json`.
Advance `--offset` by the number of issues actually returned. If the server
cannot count matching issues, it returns `failed to count issues` as an error;
do not treat that failure as an empty or complete list. Older servers can
substitute the page length for a failed count, so that value alone is not proof
that all matching issues have been read.

`issue reorder` reads the issue's project-scoped status column before writing
its new position. When a legacy total is unavailable or no larger than its
page, it reads through an empty page. A failed request, malformed page, or
duplicate issue stops the operation before any position write. This protects
against truncated or repeated pages, but does not promise a snapshot across
concurrent edits. There is no CLI bulk-export or `--all` mode.

## Custom properties: typed workflow state

Workspaces may define custom issue properties (Severity, Environment, QA
Status, Reviewer, ...). They are the place for durable, typed issue state:
values are validated against the definition (select options, date format,
http(s) URL, member reference), visible in the issue sidebar, and addressed
by name.

- Read what exists before writing: `multica property list` shows the catalog;
  `multica issue property list <issue-id>` shows values set on the issue.
- Set values by property name and option name — the CLI translates to ids:

```bash
multica issue property set <issue-id> --name Environment --value staging
multica issue property set <issue-id> --name Platforms --value "iOS,Android"
multica issue property set <issue-id> --name Reviewer --value Bohan
multica issue property unset <issue-id> --name Environment
```

- A validation error lists the legal options — fix the value and retry.
- `actor` / `multi_actor` properties (Reviewer, Escalation contact, ...) hold
  workspace members only. `--value` takes a member name, email (email only resolves when the caller is a
  workspace owner), UUID, short id,
  or an explicit `member:<uuid>`; `multi_actor` takes a comma-separated list
  (duplicates dropped, order kept, max 20).
- Definitions may include an optional catalog icon for visual identification;
  it does not change the property's type or value validation.
- Agents cannot create or edit property definitions (owner/admin humans only).
  If a needed property does not exist, propose it in a comment instead.
- Where state belongs: workflow state a human should see and filter by goes in
  a property; the stage the issue is at goes in its status; a Close protocol
  finish writes the eight `close.*` keys (see `references/close-protocol.md`);
  everything else — what you did this run, what you found — goes in the result
  comment.
- `issue list` filters and sorts by property with the same name addressing:

```bash
multica issue list --property "Impact=High" --property "Impact=Medium" --output json
multica issue list --property "QA Status=__none__" --status in_review --output json
multica issue list --sort property:Impact --direction desc --output json
```

- `--property` takes one `Name=Value` per flag. Repeating the same property
  matches ANY of its values; different properties must ALL match. Values are
  option names or ids (select types), `true`/`false` (checkbox), a member
  name/email/id (actor types), or the value itself for text, url, number,
  and date (`YYYY-MM-DD`). The reserved value `__none__` matches
  issues where the property is unset (works for every type; it is not
  index-backed, so use it for targeted audits rather than as a default
  listing filter). Only `=` is supported today; the `>=`, `<=` and `!=`
  spellings are reserved for comparison filters and are rejected.
- `--sort property:<name-or-id>` orders select properties by option order —
  an ordinal scale (Low < Medium < High) sorts by meaning — and number/date/
  text/url by value; issues without the property sort last either way.
  Archived properties and types without an order (multi_select, checkbox,
  actor kinds) are rejected up front.
- `issue list` and `issue get` return `properties` as a map of definition id
  to stored value. Add `--resolve-properties` in JSON mode to get the rows
  `issue property list` prints instead (name, type, stored value, display
  names); the CLI makes at most one catalog request for the whole page, so
  no `property list` call is needed:

```bash
multica issue list --status in_progress --output json --resolve-properties
multica issue get <issue-id> --resolve-properties
```

  Read `display` for a single value and `display_values` for a multi_select
  or multi_actor value; `value` keeps the stored ids.

## Status changes have server side effects

A status change is not cosmetic — the server enqueues or skips agent work based
on it. These are the contracts, not advice.

The rules below name fixed built-in status keys, not category-wide behaviors.
Custom statuses have only lifecycle semantics: unstarted, started, done
(successful terminal), or closed (cancelled terminal). They do not inherit
Backlog parking, In Review completion, Blocked failure, or In Progress recovery.
Use the built-in key when its special behavior is needed. Built-in definitions
cannot be edited or archived.

Archive a custom status only after moving every issue off it, including
completed/canceled issues. An occupied status returns HTTP 409 with code
`issue_status_in_use` and `issue_count`; it remains active. Use Settings >
View issues to inspect and move its issues, then retry. For terminal-status
replacement, preserve the lifecycle meaning (`done` to `done`, `closed` to
`closed`); do not reopen or cancel completed work just to retire a status.
Archival does not move issues automatically. Historical issues on previously
archived statuses remain readable via an explicit status filter.

- **`backlog`** parks an agent-assigned issue: the assignee is set but no task
  fires. Moving `backlog → todo` (or any non-done/non-cancelled status) enqueues
  the assigned agent then.
- **`in_progress` / `in_review`** are agent-managed CLI mutations, not automatic
  side effects of a task starting or finishing. The runtime brief asks agents to
  write the state the issue is in whenever their work changes it — not from
  the trigger type or the run's lifecycle, and not gated on being the
  assignee. Writes happen whenever the state changes, mid-turn included: a
  turn that advances the issue's own ask sets `in_progress` as soon as that
  is known, so the board shows the work while it runs; a blocker is recorded
  when it is hit; and the turn must not exit with a stale value — delivered
  the issue's own ask → `in_review`; work continues beyond the turn
  (dispatched sub-issues, partial delivery) → `in_progress`; stuck →
  `blocked`. A turn that produces none of the issue's own deliverable —
  answering a question, consulting on work owned elsewhere — writes nothing
  at any point. The kind of activity never decides this: research, design,
  planning, and review all count as the work exactly when they are what the
  issue asks for (a review-the-PR issue is being worked the moment reviewing
  starts). Questions, discussion, or acknowledgements never move the status.
  Acceptance is parent-scoped: a sub-issue with `parent_issue_id` is execution-only
  and must not enter `in_review` or receive an independent 验收席. Its terminal
  state is only an input to the parent's stage/barrier; after the full child tree
  is complete, the parent is the one issue that moves to unified `in_review`.
  Squad leaders: dispatching members is not delivery — a dispatch turn
  leaves the parent `in_progress`, and it moves to `in_review` only when a
  later re-trigger confirms the overall goal is met.
- **`in_review`** is an accepted issue status. Some workflows use it while a PR
  is open and awaiting review; moving to it is an explicit mutation.
- **`done`** on a child issue posts a system comment on its parent. If a PR
  carries close intent (`Closes MUL-XXXX`), it advances the issue to `done`
  itself on merge — you do not also need to flip it manually.
- **`blocked`** requires the wait on the same `multica issue status <id> blocked`
  call: `--blocked-by <DENE-N>`, `--wake-at <RFC3339>`, `--wait-condition` with
  `--wait-timeout`, or `--needs-human <member uuid>`. An agent change without
  one is rejected (a member's only warns); "等 DENE-N" in a comment is only a
  suggestion. An agent also passes `--block-kind <decision|permission|external|dependency|capacity>`
  and `--block-action "<one-line next step>"` (≤80 chars), or the change is
  rejected — `multica issue close --outcome blocked` records both for you and
  is the preferred path. Routing seats an empty executor parked (no run) and the command
  says if it did. A cleared blocker or passed acceptance wakes the waiter (no
  executor: a person gets a 缺执行人 card); the patrol wakes a quiet one ~30 min.
  A member's reply that starts a run for the executor moves the issue back to
  `in_progress` and marks the blocked close superseded — do not re-block to
  "acknowledge" it.
- **`cancelled`** is a terminal, user-driven decision to close the issue. Like
  `done` it enqueues no new agent work, but it does **not** stop tasks already in
  flight — a run in progress keeps going. To stop a running task, cancel the
  task itself.
  A cancelled issue may also be marked as a **duplicate** of another issue.
  When you cancel an issue because the work already exists elsewhere, mark it
  with `multica issue status <id> cancelled --duplicate-of <original>` rather
  than cancelling and explaining in a comment: only the mark links the two.
  The original must not itself be a duplicate, and an issue that others are
  marked as duplicates of cannot be marked; the command reports both refusals.
  (`GET /api/issues/<id>/duplicates` shows both sides; issue responses carry
  the original as `duplicate_of` with its id, identifier, title and status
  while the mark counts). Moving it to any
  status other than `cancelled` removes the mark, so reopen a duplicate only
  when it is really separate work. Marking logs `duplicate_marked` on the
  duplicate and `duplicate_added` on the original; removing the mark logs
  `duplicate_unmarked` / `duplicate_removed` (`multica issue timeline --action`).
- **Failed issue-triggered tasks** may roll an issue from `in_progress` back to
  `todo` when no active task / retry remains — that is the main server-owned
  status write on the agent-run path.
- **Completed issue-triggered tasks** are the mirror case, and they write no
  status at all: a run that reaches `/complete` cleanly while the issue is
  still `in_progress` with nothing queued behind it leaves a system comment
  carrying `completion-stall:run-completed-without-terminal-status`, naming the
  current assignee and the parent issue, then queues one recovery run for that
  same assignee, told to finish the work and close through the close protocol.
  It never moves the issue. One issue gets at most one such
  signal per 30 minutes, and an issue that still has a non-terminal child is
  never signalled — dispatching sub-issues and staying `in_progress` is the
  documented way to record that the work continues below. A run ending is therefore still not the issue ending,
  and an agent that delivered part of its acceptance criteria must write the
  status itself: leaving it `in_progress` just buys another run.

## Automatic routing (off unless the workspace turned it on)

Routing fills the assignee and 验收席 slots on creation and on status changes,
and never writes a status. Its rules, the 验收席 field, and what a `route`
result means are in `references/routing.md`.

Do not name another agent as executor on your own: with routing on the server
ignores it. Only the words of the person you are talking to, passed as
`--per-quote "<原话>"`, carry a pick through; ask for a stronger seat with
`multica issue escalate <id> --reason "..."`. See `references/routing.md`.

## Who else is running right now

Nothing about concurrent runs is pushed into your prompt: the answer changes
while a turn is running, and most turns never need it. Ask the server on the
turns that do — before opening a PR against code a sibling issue also touches:

```bash
multica issue runs <issue-id> --active --output json     # in-flight runs on this issue
multica issue runs <issue-id> --siblings --output json   # ...and across the sub-issue family
```

`--active` drops the execution history and returns only `queued` / `dispatched`
/ `running` / `waiting_local_directory` runs. `--siblings` widens the same read
to the issue's family — its parent (or itself, when it has no parent) plus every
child of that parent — and labels each row with the issue it belongs to, which
is how you find another agent already working on a sibling sub-issue before you
open a second PR against the same code.

`waiting_local_directory` is the `in_place` path-mutex wait. Tasks on a
`shared` or `worktree` `local_directory` do not take that mutex, so they do
not enter this status for the directory lock. If several tasks on an umbrella
directory still serialise, the resource is probably still `in_place` — switch
it to `shared` only when the workspace already isolates each task (typically
a per-repo worktree) and you accept that two tasks editing the same checkout
are unprotected. Full contract: `references/projects.md`.

The family read returns a compact row — task, issue, agent, status, started —
not the full execution-log record. If you need a run's detail, follow the task
id with `multica issue run-messages`.

## Who can see an issue

`multica issue access <id> --output json` answers what the share button in the
issue header shows: `visibility` (`private` / `project` = specific people /
`workspace`), `audience_size`, and `can_change`. When `can_change` is false,
`reason` is `guest` or `not_creator`; tell the person to ask the creator, an
admin or the owner to change it rather than retrying. A link to a private
issue opens as "not found" for everyone else, so check this before pasting an
issue link for someone who may not be in its audience. `multica project access`
is the same read for a project.

## Stop every run on one issue

Use the issue-level guard when an agent chain must stop immediately:

```bash
multica issue halt <issue-id>    # cancel queued/dispatched/running runs and block agent triggers
multica issue resume <issue-id>  # clear the halt guard; a human comment is still needed to reset a chain budget
```

The guard is issue-scoped. A human comment clears it and resets the
delegation-chain budget; `resume` only clears an explicit halt and does not
reset an already-exceeded budget. Direct human-triggered runs are never
consumed by that budget. The default chain limit is thirty runs; a workspace
admin changes it under Settings → General → Agent run limits (stored as
`agent_chain_budget` in the workspace `settings` JSON, `0` = unlimited). Hitting
the limit posts a system comment in the triggering thread instead of stopping
silently.

The same settings section holds a run time limit
(`agent_task_timeout_minutes`, `0`/absent = none). A run that outlives it fails with reason `task_time_limit`:
a round boundary, not a wrong result. The platform continues the same CLI session and working directory
until the attempt budget is spent, and the continuation is told to close out finished work and split what remains.
When the budget is spent the issue becomes `blocked` with a comment instead of sitting in `todo`; a sub-issue also leaves a short note on its parent.

Rows come back running-first, newest-first within a status, and the family read
is capped at 20. When the cap truncates the answer the CLI prints a warning on
stderr — read it. Without that warning a short list means "nobody else is
there"; with it, the list proves nothing about the runs it did not return.

Both are advisory reads. Nothing here reserves an issue or serialises anything:
a run you see may finish a second later, and one you don't see may start a
second later. Coordinate through the issue's comments — the reads tell you whom
to coordinate with.

## Charts and files in a comment

Where content goes decides how it shows:

- **In the body, rendered in place** — a fenced ` ```html ` or ` ```mermaid `
  block in the comment content. It renders inside the comment with a title
  bar (Preview / Source, fullscreen, copy) and takes its content's height;
  anything taller than 480px collapses behind "Show all". Name it with
  `title="..."` on the fence line. HTML runs in a scripts-only sandbox (no
  cookies, storage or parent access; CDN `<script src>` works).
- **An attached file** — `--attachment <path>`. Every non-image file shows as
  a file card that opens in the viewer, **HTML included**: an uploaded
  `report.html` is a deliverable to open, not an inline chart. Use it for
  something the reader keeps or downloads.

For HTML that should follow light / dark mode, style it with the page's theme
variables: `var(--background)`, `var(--foreground)`, `var(--muted)`,
`var(--muted-foreground)`, `var(--border)`, `var(--primary)`,
`var(--chart-1)` … `var(--chart-5)`, `var(--font-sans)`. Using any of them opts
the block into the app's color scheme, so also set the page background
(`body { background: var(--background); color: var(--foreground) }`). HTML
that uses none keeps its own look. Size to the content, not the viewport:
`100vh` heights have no fixed viewport to fill here.

````markdown
```html title="p95 latency, last 7 days"
<canvas id="c"></canvas>
<script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
<script>/* draw with getComputedStyle(document.documentElement)
  .getPropertyValue("--chart-1") so it follows the theme */</script>
```
````

## Incorrect to correct

PR title (link the issue):

```text
Fix login redirect                  # incorrect — no issue key, won't link
Body-only "Part of MUL-123"         # incorrect — passing mention, won't link
MUL-123: fix login redirect        # correct — links the PR
```

Sub-issues, stages and their incorrect-to-correct examples live in
`sub-issues.md`.
When blocking on a one-line external check, pass it as `--wait-probe` together with `--wait-timeout`. Exit code `0` means ready, `10` means pending, and `gh pr checks` exit code `8` is also pending; any other exit code is failed.
For GitLab, `glab ci status` can be used directly when it returns non-zero while the pipeline is pending, or wrap its pending code as `10`.

### Report progress

`multica issue progress <issue-id> "<progress>" [--tone working|waiting|stuck|done] --output json` sets the line under the issue's title; `--history` reads earlier lines. Who wins between your line, a close summary and the stall patrol: `references/progress.md`.

`multica issue title <issue-id> --suggest "<title>" --output json` stores a pending title suggestion for a human-created issue. It does not rename the issue; the issue page's adopt action is the only writer that applies it.

## Issue wakeups

Event, condition and timer wakeups (`multica issue wakeup ...`) and scheduled check-ins are in `references/wakeups.md`.
