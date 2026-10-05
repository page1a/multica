# Drivers and dispose

Every open issue has a driver: something that will move it without anyone
remembering to. The platform computes it; agents read it and act only when
it says `none`.

## Reading the driver

`multica issue get <id> --output json` and `multica issue children <id>
--output json` carry a `driver` object on each open issue (closed and
`backlog` issues have none — they need no driver). The `issue children`
table shows it in the DRIVER column.

| `driver.kind` | Meaning |
| --- | --- |
| `run` | A run is running or queued on the issue |
| `wait` | A recorded wait: `in_review`, a structured block (`--blocked-by`, `--wake-at`, `--wait-condition`), an issue wakeup, or open children |
| `person` | A person: a member executor or reviewer, or `--needs-human` |
| `none` | Nobody. `reason` says why; `revives` and `escalated` say what the patrol already tried |

## What the patrol does with `none`

The block-wait patrol checks the blockers of every watched waiter and every
quiet `todo` issue with no run and no wait:

1. It reruns the undriven issue on its own seat, at most 2 times in 24 hours,
   at least 30 minutes apart.
2. Then it escalates once: the parent's agent (or squad) is woken with the
   dispose menu below; a member parent owner, or an issue without a parent,
   gets a person called in instead.
3. After that it holds until someone disposes of the issue or a driver
   appears.

A run that fails with no retry queued parks the issue as `blocked` with a
retry clock; it no longer drops back to `todo` with nothing behind it.

## Disposing of an undriven issue

The parent's owner picks one action with one command:

```bash
multica issue dispose <id> --action rerun                       # same seat, run again
multica issue dispose <id> --action reroute                     # clear the executor, let routing pick again
multica issue dispose <id> --action split --into "标题" [--into "标题"]  # create children, block on them
multica issue dispose <id> --action cancel --reason "..."       # cancel with the reason as a comment
```

The server checks it, so a refusal tells you what to fix:

- `409` — the issue is closed, parked, or already has a driver; read
  `driver` again before acting.
- `403` — an agent may dispose only of a child of an issue it owns (directly,
  or as the squad leader of the owning squad).
- `400` — `split` needs 1–10 `--into` titles, `--into` is only for `split`,
  and `cancel` needs `--reason`.

`--output json` returns the action, the issue's new `status`, its new
`driver`, and any `created` children. Disposing resets the patrol's revive
count.
