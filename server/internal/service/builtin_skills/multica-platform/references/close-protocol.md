# Close protocol

A close is conclusion + status + evidence + next owner + wake action. **A
comment alone is not a close.** Missing any of the eight `close.*` keys means
this issue has not been closed under the protocol. `in_review` is not a stage
terminal; only `done` / `cancelled` close a stage barrier.

This reference is an excerpt of `docs/kun/scheduling-close-protocol.md`. Do not
invent field names, write order, statuses, or wake actions.

One call — `multica issue close` (DENE-859). It writes the evidence comment,
the status, and every `close.*` key in one transaction, and validates the
record under this protocol before anything lands. A close missing a piece is
rejected naming the missing item; nothing is half-written. The outcome table
and every refusal live on the server only (DENE-1183): the CLI relays the
server's reason as-is, and before a close that may merge a PR locally it asks
the server's read-only shape check first. A refusal is the server's own
sentence — fix what it names and run the same command again.

```bash
multica issue close <id> --outcome done      --evidence-file ./close.md            # delivered; an open linked PR is merged first, or the ticket stays in_progress on a wake (see below)
multica issue close <id> --outcome in_review --evidence-file ./close.md            # top-level, awaiting acceptance: needs a linked PR (or --no-code <reason>); empty reviewer slot is filled, then routing hands over
multica issue close <id> --outcome blocked   --evidence-file ./close.md --blocked-by DENE-196   # or --wake-at / --wait-condition + --wait-timeout / --needs-human
multica issue close <id> --outcome cancelled --evidence-file ./close.md            # dropped on purpose: say why in the evidence
multica issue close <id> --outcome backlog   --evidence-file ./close.md            # back to planning on purpose (DENE-1002): a reason, no PR, nobody woken
multica issue close <id> --outcome todo      --evidence-file ./close.md            # back to the ready list on purpose: same shape as backlog
multica issue close <id> --outcome in_progress --evidence-file ./close.md --wake-at 2026-10-01T09:00:00Z   # stay in progress, and say who continues
multica issue close <id> --outcome done --verdict pass --evidence-file ./close.md  # acceptance seat: merge the open PR, then done
```

- `--evidence` (or `--evidence-file` / `--evidence-stdin`) is required: the PR
  link, test conclusion, or blocker it rests on. `--summary` goes above it.
  Keep `--parent` when this turn has a trigger; a comment-triggered run on the
  same issue defaults to that thread. Headings inside the evidence, in this
  order: `## 结论` / `## 状态` / `## 证据` / `## 下一责任人` / `## 唤醒动作`.
- `--outcome blocked` must say what it waits for — the same wait DENE-850
  requires on `issue status blocked`: `--blocked-by <issue>`, `--wake-at
  <RFC3339>`, `--wait-condition "..." --wait-timeout <dur>`, or
  `--needs-human <member>`. Without one the close is rejected.
- `--outcome in_review` is top-level only; a sub-issue asking for it is
  rejected (use `done` or `blocked`). It runs the same review gate as
  `issue status in_review` (DENE-869): an agent's close is refused unless the
  issue has a linked open/draft/merged PR, or `--no-code <reason>` says why
  the ticket carries no code (docs, research). `--needs-human <member>` turns it into
  `awaiting_human`; otherwise it is `awaiting_review` with `wake_action=route`
  — routing hands the ticket to the acceptance seat, no executor @mention.
- `--outcome backlog` / `--outcome todo` are the deliberate return: the
  ticket goes back to `backlog` / `todo` with a `deferred` conclusion, a
  `none` next owner and no wake. It needs no PR (nothing shipped) — the
  evidence says why the work goes back. This is the right close for "a reason,
  no continuation"; do not park a ticket in `in_progress` for it.
- `--outcome in_progress` keeps the ticket in flight while this run stops:
  conclusion `continuing`, and it **must** name who continues, using the same
  wait flags as `--outcome blocked` — `--wake-at <RFC3339>`,
  `--wait-condition "..." --wait-timeout <dur>`, `--blocked-by <issue>`, or
  `--needs-human <member>`. Without one the close is rejected ("放回进行中必须
  写明「接下来谁继续」"). `--wake-at` writes `wake_action=clock`: the platform
  wakes the next owner when the clock comes due, so a paused ticket resumes
  instead of sitting until somebody notices. The next owner defaults to the
  issue's assignee; `--needs-human` names the person instead.
- `--verdict pass` is the acceptance seat's release and only pairs with
  `--outcome done` on an `in_review` ticket: the platform merges the open PR
  and writes `done`; if the merge cannot happen it is a PR stop below, not
  `blocked`. A failed acceptance is not a close: `multica issue comment
  add <id> --verdict hold --content-file ./review.md` wakes the executor.
- **PR stops are answered on the spot** (DENE-1219). `blocked` means a person
  has to act: missing permission or `--needs-human`. Anything the executor can
  clear never writes a status:
  - Checks still running: the CLI waits in place (refreshes the PR and asks
    again every 30 seconds, about 15 minutes at most) and merges when green.
    If CI is still running after that, run the same close again later.
  - Checks red, a conflict with the base, a draft PR, the PR or delivery line
    unreadable: the close is refused with HTTP 409 and a `code`
    (`close_checks_red`, `close_conflict`, `close_draft`, `close_read_failed`,
    `close_delivery`). Red checks name the check and where its logs are
    (`gh pr checks <url>`). Fix the cause and close again; if the same check is
    already red on the base branch the gate lets it through by itself, and if
    it could not read the base, merge with `gh pr merge --squash <url>` once
    you have confirmed it, then close again.
  - The platform's merge fails: it retries once; still failing, the close is
    refused with `close_merge_failed` and the local command
    `gh pr merge --squash <url>` — run it, then close again.
  - A `--verdict pass` that hits a stop is refused before the pass is recorded
    (same codes). A pass recorded earlier by comment whose merge then fails
    keeps the ticket `in_review`, and the reply's `hold` (`kind`, `reason`,
    `next`) says what is missing; the executor is woken to fix it and the
    patrol merges once the PR can go in.
- `--pr <pull-or-mr-url>` declares the delivery when the platform has not
  linked one yet (DENE-961). The server checks that URL against the
  repository connection, registers it when the check succeeds, then runs the
  normal gate. If it cannot verify the URL, the close still proceeds and the
  ticket records `close.pr_unverified` (未核实); that link is not merged.
  Do not block on “等待平台关联 PR” or any equivalent — that wait is rejected.
  Use `--pr` instead. `multica issue pull-requests` reports the same gap.
- Knowledge audit is required on every close, including a ticket with no pull
  request. `--knowledge-none` declares that nothing qualified for project
  memory. Repeat `--knowledge <key>=<summary>` for each checklist slot this
  close wrote. The keys are the project-memory checklist (`agents`, `context`,
  `adr`, `docs_index`, `evidence_index`); do not invent another list. The
  server stores `close.knowledge_audit` in the same transaction as the
  evidence comment, the status, and the other `close.*` keys. A missing
  audit, an unknown location, an empty summary, a duplicate location, or both
  forms at once is rejected and nothing is written. Declaring no qualified
  knowledge is a valid close. This key is not one of the original eight:
  older closes stay readable without it. A heading in a pull-request body is
  not a second gate.
- The reply reports the status actually written, whether the PR merged, and
  who is woken. Quote it; do not restate it from memory.

Waking the next owner without closing is `multica issue handoff` (DENE-863),
not a hand-written @mention. The server routes, dedupes, and replies with what
actually landed (`target_name`, `run_created`, `duplicate`):

| you want | call |
|---|---|
| a named agent picks it up (Reviewer `needs-work` back to Builder, a concrete sub-task) | `multica issue handoff <id> --to <agent-name>` — no second run if that agent already has one active on this issue |
| the dispatcher decides who is next | `multica issue handoff <id> --to dispatcher` |
| an `in_review` issue whose acceptance seat never started | `multica issue handoff <id> --to reviewer` — refused with 409 when a person holds the seat; routing never writes a person there |

A close already hands over what it closes: `--outcome in_review` routes the
seat itself, so do not follow it with `handoff --to reviewer`.

Both calls take a repeatable `--decision "..."` for what this round settled,
and `handoff` takes `--summary` for what the next owner needs to know. The
next owner reads both back with `multica issue context <id>`
(`references/state-card.md`).

Calling a person is `multica issue summon <id> --to <member> --reason "..."`
(DENE-880, `POST /api/issues/{id}/summon`), not a hand-written @mention. One
call writes their inbox row (`needs_you`, highest severity), subscribes them,
leaves a visible @ on the ticket, and records an open call; a second call
before they reply comes back as `duplicate: true`. Their reply wakes the
executor, and if that run ends with the ticket still `blocked` the executor
is reminded once to close again. `--needs-human <member>` on `issue close` or
`issue status` already calls that person — do not summon them again.

Legacy path, still accepted: `multica issue status <id> <status>` (with the
wait flags when `blocked`), then the evidence comment (`--content-file`),
then the eight keys via `multica issue metadata set` with `close.status`
equal to the status written and `close.evidence_comment_id` equal to the
comment id. If `wake_action=mention`, the body must contain a live
`[@Name](mention://agent|squad/<uuid>)`; `mention://member/…` and
`mention://issue/…` do not wake anyone.

| key | allowed values |
|---|---|
| `close.conclusion` | `delivered` `blocked` `awaiting_review` `awaiting_human` `deferred` `continuing` |
| `close.status` | the issue's status key after step 1 |
| `close.evidence_comment_id` | comment UUID |
| `close.next_owner_type` | `agent` `squad` `member` `none` |
| `close.next_owner_id` | UUID; `""` when type is `none` |
| `close.wake_action` | `stage_done` `mention` `route` `clock` `none` |
| `close.waiting_on` | identifier such as `DENE-196`, or `""`. Prefer a real parent + stage for same-family waits; server wakes the waiter on `done`/`cancelled` unless that `(issue, agent)` already has a queued or running task |
| `close.at` | RFC3339 UTC |
| `close.block_kind` | `decision` `permission` `external` `dependency` `capacity`; required for new blocked closes |
| `close.block_action` | Non-empty unblock action, at most 80 characters; required for new blocked closes |

Blocked close records must write `close.block_kind` and `close.block_action` together. `dependency` additionally requires a non-empty `close.waiting_on`; `decision` and `permission` require a concrete `member`, `agent`, or `squad` next owner. Legacy blocked records that predate these two keys remain readable when both are absent. For non-blocked conclusions, the fields must be empty or absent. Human review is overdue after 24 hours without activity; `capacity` blockers do not count toward “needs you”.

Decision table (first match). `needs_acceptance` means the top-level parent
still requires Reviewer / human / device confirmation. A sub-issue never owns
an acceptance conclusion: it reports its execution result and closes as
`delivered`; the parent reviewer checks the parent together with every child.
Staged child = has a parent and (own `stage` or any staged sibling).

| conclusion | when | status | next owner | wake |
|---|---|---|---|---|
| `delivered` | ask delivered, no acceptance, staged child | `done` | parent assignee, or `none` | `stage_done` — do **not** mention the parent assignee |
| `delivered` | ask delivered, no acceptance, not staged | `done` | `none` unless AC names someone | `none` or `mention` |
| `awaiting_review` | top-level parent acceptance is an agent Reviewer | `in_review` | that Reviewer, or `none` and let routing fill the seat | `route` (what `issue close --outcome in_review` writes) or `mention` — **not** `done`; child barrier is already closed |
| `awaiting_human` | top-level parent acceptance is a human | `in_review` | that member | `none`. Optional dispatcher: `mention` that agent and name the human in `waiting_on` or the evidence |
| `blocked` | missing auth / human decision / external dep | `blocked` | who can unblock | `mention` if agent/squad, else `none` |
| `deferred` | the work goes back to the plan or the ready list on purpose (`--outcome backlog` / `todo`) | `backlog` or `todo` | `none` | `none` — nothing shipped, nobody woken; no PR needed |
| `continuing` | this run stops mid-work but the work goes on (`--outcome in_progress`); a continuation must be written | `in_progress` | the issue's assignee, or the `--needs-human` member | `clock` when the continuation is `--wake-at`; `none` when it is a person, a condition, or another issue |
| (no close) | 交付查询报没权限 | do not change status | 仓库登记人（服务端 summon） | 先 `multica connection add --from-gh --yes`（本机有发起人的 gh 登录）；不行就交给服务端叫人，不要自己设等待条件 |
| (no close) | this turn did not deliver this issue's ask | do not change status | — | do not write `close.*` |
Four closing scenes:

- **done** — observable delivery is in; no acceptance left on this issue.
  Status `done`. Staged children use `stage_done` and leave parent wake to the
  server.
- **in_review (agent Reviewer)** — PR / design / implementation needs
  Reviewer. Status `in_review`. `issue close --outcome in_review` routes it to
  the seat; a hand-written record wakes it with `issue handoff --to reviewer`. Do not `done`.
- **in_review (human acceptance)** — device, balance, third-party account, or
  a named human. Status `in_review`. `wake_action=none`. Barrier stays open
  on purpose.
- **blocked** — missing permission, product decision, or external dependency.
  Status `blocked`. Barrier stays open. A wait that the platform can watch
  (`--wake-at`, `--wait-condition` + `--wait-timeout`, `--blocked-by`) resumes
  itself; a person wait is comment-only.
- **backlog / todo (deliberate return)** — the work goes back to planning or
  the ready list, with the reason recorded. Status `backlog` / `todo`.
  Conclusion `deferred`. No PR, no wake, nobody is asked to continue.
- **in_progress (paused, continues)** — the run stops but the work goes on,
  and this close writes who continues with which wait. Status `in_progress`,
  conclusion `continuing`. `--wake-at` wakes the next owner on the clock; a
  person continuation (`--needs-human`) only leaves the record and the call. A
  run that ends `in_progress` with no such close is the stall the completion
  path above signals.
Role defaults:

| role | default conclusion | default status | wake |
|---|---|---|---|
| Builder (PR / needs review) | `awaiting_review` | `in_review` | `issue close --outcome in_review`; routing hands it to the Reviewer. Title carries the identifier. Do not `done` while waiting. Leave `Closes` for merge |
| Builder (no acceptance gate) | `delivered` | `done` | `stage_done`; do not mention the parent assignee |
| Reviewer pass, owned checks green, no explicit human hold | `delivered` | `multica issue close <id> --outcome done --verdict pass` (or a `multica issue comment add <id> --verdict pass` comment); the platform merges the open linked PR and sets `done` in that same call | `stage_done` |
| Reviewer pass, but the ticket explicitly names a person and a decision only they can make | `awaiting_human` | `in_review` | `none`. The comment names that person and the decision. A routing note that says 需要人拍板 is not this row |
| Reviewer pass, but a check this change owns is red | not a close | `in_progress`, mention Builder | `mention` |
| Reviewer pass and already merged | `delivered` | `done` if the webhook did not | `stage_done` |
| Reviewer `needs-work` | not a close | `in_progress` or keep, mention Builder | `mention` |
| Operator ship/ops delivery | `delivered` | `done` | `stage_done`, or mention the next seat if AC says so |
| Dispatcher promoting the next stage | do not write child `close.*` | child `backlog → todo` | server enqueues. Keep the parent `in_progress` until the chain is done |

A pass does not stop at `in_review`, and it is a verdict line, not a sentence.
Only a comment by the issue's 验收席 on an `in_review` issue that carries a
standalone `verdict: pass` line (what `--verdict pass` writes) releases it:
the platform merges the open linked PR and sets `done`, or sets `done` directly
when there is no open PR. When the merge fails — no merge permission on this
server, PR not mergeable, merge error — the platform moves the issue to
`blocked` with a structured wait and, for missing permission, wakes the
executor to merge. 通过 inside a sentence merges nothing. The only pass that
stays in `in_review` is the explicit human row above.

A red check that is already red on the base branch belongs to the base branch,
not to this change (DENE-892). Both merge gates — `--outcome done` and
`--verdict pass` — compare the PR's red checks by name with the latest CI on
the PR's base branch (`kun`): when every red check is also red there, nothing
is still running, and the PR has no conflict or branch rule, the platform
merges anyway, opens or reuses the one open `<base> 基线 CI 红` fix issue, and
the reply and the issue say `因主线原有失败放行：<checks>` with the fix issue.
A red check that is green or absent on the base, or a base that cannot be read,
still blocks. Do not withhold a verdict for a base-branch failure, and do not
merge by hand to get around the gate. The comparison is per job: a new failure
inside a job that is already red on the base passes too, so the executor's
evidence still says the failing tests were compared with the same tests on
`kun`.

A Dispatcher advancement turn is only: `multica issue children`, read `close.*`
on the parent and the current stage's children, then either promote the next
stage's `backlog` children to `todo` or post a short conclusion. Do not rebuild
a panorama board (no unbounded comment history, no workspace-wide dump). Bound
comment reads with `--roots-only --summary` then `--thread <id> --tail N`. CLI
commands already carry `APITimeout()`; do not wait on a hung long list.

Dispatcher must not promote Stage N+1 while Stage N still has a child in
`in_review` / `blocked` / `in_progress`. Promote only when that stage's `done`
count equals `total` (cancelled counts as done).

There is no scan that retries a failed wake (the Stage 4 watchdog was removed
in DENE-520). Two narrow backstops look at issue state instead:

- **Block-wait patrol** (server, every minute). A `blocked`, `in_review`, or
  `in_progress` issue that has been quiet for 30 minutes with no run working on
  it is woken from its `block.*` wait: the executor, or the 验收席 for review.
  An `in_progress` row is only a candidate when a `--outcome in_progress` close
  marked it watched (DENE-1002), and the watch is dropped once it has been
  woken, so an ordinary active ticket is never interrupted. One wake per wait
  segment; when the wait is a person, it only comments and starts no run. It
  never promotes `backlog` children and never changes models.
- **Completion path**, fired by the run's `/complete` rather than by the
  clock: a run that ends cleanly while the issue is still `in_progress` with no
  active task behind it posts a
  `completion-stall:run-completed-without-terminal-status` system comment
  naming the assignee and the parent issue, then queues **one recovery run for
  the same assignee** that is told to finish through this protocol. It moves no
  status, and one issue is signalled at most once per 30 minutes. A parent that
  still has a non-terminal child is excluded: dispatching sub-issues and staying
  `in_progress` is how "work continues below" is recorded, so the parent is
  only signalled once every child is `done` or `cancelled`. Ending a turn in
  `in_progress` after a partial delivery therefore costs another run; close
  the issue yourself instead.

Checks: `close.status` equals `issue.status` and is a built-in key;
`wake_action=stage_done` implies status in {`done`,`cancelled`} and
`conclusion=delivered`; `wake_action=mention` implies `next_owner_type` in
{`agent`,`squad`}, a non-empty `next_owner_id`, and the evidence body contains
`mention://agent\|squad/<next_owner_id>`; `wake_action=route` implies status `in_review`;
`conclusion=awaiting_review` implies
`in_review` and `wake_action` in {`mention`,`route`}; `conclusion=awaiting_human` implies
`in_review` and `next_owner_type=member` (or a dispatcher agent with
`wake_action` in {`mention`,`route`} and the human named in `waiting_on` or the evidence);
`conclusion=blocked` implies `blocked`; non-empty `waiting_on` forbids `done`;
`conclusion=deferred` implies status in {`backlog`,`todo`}; `conclusion=continuing`
implies `in_progress` and at least one of a non-`none` next owner or a non-empty
`waiting_on`; `wake_action=clock` implies `in_progress`. A `continuing` close whose
live status is `in_progress` also marks the ticket watched, so the block-wait
patrol wakes it when its clock comes due and stops watching once it has.
