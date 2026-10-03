# Issue wakeups

Use `multica issue wakeup` to arrange a future ordinary run, then finish the
current run. A wakeup persists on the issue; it is not a sleeping process.

- `wakeup events` lists supported business facts. These work with plugins disabled.
- `wakeup create <issue> --agent-id <target> --kind event --event task.completed,task.failed,task.cancelled --task-id <run> --instruction-file ./instruction.md` wakes once. Omit `--agent-id` only when acting as the authenticated agent. A specific run must belong to this issue; if already terminal, registration captures its matching state immediately.
- For a continuing subscription use `--mode continuous`. For task events, use `--filter-agent-id` to match that agent's future runs; this does not replay historical runs. For comment/issue/reaction/attachment changes, use `--filter-actor-type member|agent --filter-actor-id <user-or-agent-id>` to match the actual author/editor. Mutation-only `--filter-agent-id` remains a legacy alias for actor=agent; do not combine it with actor flags.
- A condition lets the platform check a stored fact itself and wake the target only when it holds. Pass exactly one `--until-*` flag and no `--event`, `--task-id` or actor/agent filter; the rule stays `kind=event`. The platform checks about every 30 seconds, so a condition that already holds fires on the next check, except `--until-pr checks`, which ignores results that finished before registration. A condition wakes once by default. With `--mode continuous` it fires again only after the predicate turns false or the facts behind it change (a new check result or PR head, another linked PR merging, the number of sub-issues changing).

```bash
multica issue wakeup create <issue> --until-status in_review --instruction-file ./instruction.md
multica issue wakeup create <issue> --until-pr checks --expires-in 2h --on-timeout wake --instruction-file ./instruction.md
multica issue wakeup create <issue> --until-pr merged --instruction-file ./instruction.md
multica issue wakeup create <issue> --until-children-done --instruction-file ./instruction.md
multica issue wakeup create <issue> --until-children-done --stage 2 --instruction-file ./instruction.md
multica issue wakeup create <issue> --until-issue <other-issue-id> --until-issue-state done --instruction-file ./instruction.md
```

- `--until-status KEY` — this issue's status is that key (built-in or one of the workspace's statuses).
- `--until-pr checks` — a linked pull request's checks finished, passing or failing, on its current head. `--until-pr merged` — a linked pull request merged. With several linked pull requests, any one satisfies it.
- `--until-children-done` — every sub-issue, staged or not, is closed (`done` or `cancelled`). With `--stage N` it waits only for staged sub-issues up to stage N, and stage N must have at least one. A parent with no sub-issues never fires. The parent's assignee is already notified when sub-issues and stages finish (see `references/sub-issues.md`); do not add this condition for the same wake.
- `--until-issue ISSUE` — another issue in this workspace (identifier or UUID) reaches `--until-issue-state`: `done` (default), `ended` (done or cancelled), or `in_review`.
- The same form also accepts `--until-assignee member|agent|squad:ID`, `--until-label LABEL_ID`, and `--until-property PROPERTY_ID=VALUE` (VALUE may be JSON). `multica issue wakeup create --help` lists them.
- `wakeup create <issue> --kind at --after 10m --instruction-file ./instruction.md` schedules one run. Alternatively use `--at <RFC3339>`.
- `wakeup create <issue> --kind every --every 1h --instruction-file ./instruction.md` schedules a repeating check. Or use `--kind cron --cron '0 * * * *' --timezone Asia/Shanghai`.
- `--max-fires N` (1–1000) caps how many runs a repeating rule starts: every, cron, or `--mode continuous`. A once rule rejects it. Continuous event rules, conditions included, default to 20. The run that reaches the cap is still created, then the rule pauses with `paused_reason=max_fires`. Turning the rule back on clears the pause and restarts the count.
- `wakeup checkin <issue> <wakeup-id> --note "..."` ends a scheduled check (every or cron) that found nothing worth a reply. Only the running run that rule started may call it, and that run's `[WAKEUP]` block gives the exact command. The note (1–500 characters) is kept on the run and shows in the rule's run history and the issue timeline. The run then ends without a comment. When something changed, needs attention, or the check is done, post a comment instead.
- `wakeup list <issue>` / `wakeup get <issue> <id>` show the saved configuration, next time and latest run. `wakeup runs <issue> <id>` lists the rule's latest ten runs with their triggers, check-in notes and whether each commented. Only promise that a reminder is arranged after creation succeeds.
- `wakeup trigger <issue> <id>` queues one run now, as if the rule fired. It is refused on a closed issue, or while the rule is turned off or paused for a loop or burst. `wakeup delete <issue> <id>` removes the rule and its pending inputs and withdraws its runs that have not started.
- `wakeup update <issue> <id>` uses the same flags as create and replaces the whole configuration, explicitly re-enabling it. Supply all intended fields. Old unclaimed work is withdrawn.
- `wakeup disable <issue> <id>` stops future triggers and withdraws unclaimed work. Users can also turn it off in the issue sidebar. Closing/cancelling/completing the issue disables its wakeups; reopening does not restore them.
- `--parent <comment-id>` keeps result delivery in the original thread.
- Give waits an end: `--expires-in 72h` (restarts if the rule is re-enabled) or `--expires-at <RFC3339>`. With `--on-timeout wake`, an event rule runs the target once with a `wakeup.timeout` fact when the deadline passes first; the default `end` stops quietly. Recurring checks should carry an end date.
- Members create the same rules from the issue sidebar. The parent's stage notification is not a wakeup rule on this platform: the server's sub-issue barrier (see `references/sub-issues.md`) owns it, so it does not appear in the wakeup list and cannot be turned off there.

Read current state with issue get, comment list, and run inspection before
judging business completion. Wait for a linked pull request with `--until-pr`
(`checks` or `merged`) rather than polling it from a timer. There is no separate
CI event; when the woken run needs check details or logs, use the existing
GitHub tools. A failed run does not imply its business goal is complete.
Automatic retry chains are not followed by event filters; subscribe to a new run
if needed. Once the goal is met, disable any continuous configuration. Every
wakeup runs under ordinary execution and comment delivery rules; the one
exception is a scheduled check that ends with `wakeup checkin`.

Self-trigger protection excludes the registering run and runs started by the
same rule when their source identity is available. Your own comments and issue
changes never wake you, and a condition your own unfinished run satisfies does
not wake you when you or the platform set the rule up. A wakeup that fires
while a run of yours for the same person is waiting to start on the issue
joins that run instead of starting another: its instruction and facts appear
in that run's `[WAKEUP — joined this run]` block, so handle them there.

Event rules, conditions included, also have runaway protection. A rule pauses
with `paused_reason=loop` when its trigger chain passes through it a third time
without a person in between, and with `rate` when it has already started 12 runs
in the past hour. A paused rule stays off until someone turns it back on. Still
avoid mutually triggering continuous comment subscriptions; when waiting for a
person's reply, filter that member explicitly.
