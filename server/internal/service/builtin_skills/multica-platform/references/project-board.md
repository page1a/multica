# The project task board, told back

When the user asks what is happening across one or more projects, or wants the
whole workspace's open tickets, read the project board instead of assembling an
answer from `multica issue list`:

```sh
multica project board --output json
multica project board <project-id-or-name> [<another-project>] --output json
```

The command is read-only. With no project arguments it covers the whole
workspace; with arguments it covers exactly those projects. The JSON has five
lanes: `waiting`, `stalled`, `running`, `todo`, and `stale`. Every row includes
`last_activity_at`, the person or agent in `next_name` when known, and
`stalled_reason` when the platform has one.

`stale` means more than 36 hours (1.5 days) without issue activity. The server
uses the issue's recorded parking and wait metadata: deliberate pauses with a
resume time, a recorded wait, or a return to planning are kept out of `stale`
and carry `paused` plus `pause_reason` in the `todo` lane when they remain open.
Do not infer a new blocker from age alone.

Tell the result in three practical groups: what is blocked, what can move next,
and what may be ready to clear. If a status or close change is useful, ask Kun
in the conversation first; this board command never mutates tickets.
