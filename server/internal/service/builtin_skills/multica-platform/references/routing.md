# Routing

Automatic dispatch is off unless the workspace turned it on in Settings →
Routing. Once it is on, the server asks one question on issue creation and on
every status change: given this status, who should be holding this issue. It
answers by filling slots. It has exactly one status write, described under
「停滞巡检」 below, and that write can only ever align a status to an acceptance
the reviewer already gave on the ticket — routing never decides for itself that
work is finished.

What it may do, and only when the slot is still **empty**:

- **`todo`** — fill the assignee with a seat from the tier ladder, and fill the
  issue's 验收席 with a seat or 「不需要验收」. **Routing never writes a person
  into 验收席**: an issue a person holds is one routing never touches again, so
  a person in that slot freezes the issue there. When the judge decides the
  acceptance needs a human call, the slot still gets a seat and the decision
  comment tells that seat to @ the person instead.
- **`in_review`** — hand the issue to the seat in 验收席 (which starts its run).
  「不需要验收」 is left alone. A 验收席 a person filled with a **person** is
  notified with an @ and a subscription, and the issue is **not** reassigned —
  whoever is holding it keeps it, so its status can still be moved.
- **`blocked`** — post one advice comment and @ somebody. **No value is
  changed.**
- **`in_progress` / `done` / `cancelled` / `backlog`** — nothing at all.

There is a fourth trigger that is not a status change. A ticket sitting in
`in_review` with nothing happening on it and no run working on it is **stalled**,
and a periodic sweep looks at it once the quiet passes the workspace's stall
threshold (Settings → Routing, default 24 hours):

- The default is to **wake the 验收席 and change no status** — reassign the seat,
  which starts its run, or tell a named person once and @ them.
- The status is moved to done **only** when the 验收席 already left a pass verdict
  on the ticket, and the comment recording it says which remark it read. With
  nothing from the reviewer on the ticket this branch cannot be taken at all.
- A ticket that reached `in_review` before its 验收席 was ever decided gets the
  slot filled now and is handed on — this is the same row as `in_review` above,
  and it runs even when the assignee is a person.
- With routing switched off, the sweep does not run.

验收席 is a native issue field, not a workspace property: `reviewer_type` +
`reviewer_id` on the issue, shaped exactly like `assignee_type` +
`assignee_id`, plus one extra type. Set it by hand with

```bash
multica issue update <issue-id> --reviewer <member-or-agent-name>
multica issue update <issue-id> --reviewer none   # 不需要验收
multica issue update <issue-id> --reviewer ""     # back to undecided
```

Because it is a reference and not a copy of a name, renaming the agent or
person it points at changes nothing on the issue, and archiving them releases
the slot so routing can pick a live seat next time the issue moves.

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
- **Low confidence dispatches anyway**, to the ladder's fallback rung (the
  generic strong seat) — `reason` reads `executor fell back to 孙悟空:
  confidence 47% < threshold 60%`. The reviewer slot falls back to one rung
  above the executor, one rung below when the executor is already the top
  rung, and 「不需要验收」 when the workspace has only one seat.
- The project -> direction table decides which direction SEAT on a rung gets
  the work; it never changes the rung or the confidence. It is workspace data:
  `multica workspace routing-projects list | set <project> <direction> | unset
  <project>` (exact name or `prefix*`; a listed direction, or `通用`).

When routing is **not working** — the model was rejected, is unreachable, the
breaker is cooling down after repeated failures, or the deployment never
configured a server-internal LLM — issues are left entirely
alone, exactly as if routing were off. Nothing is posted on a ticket about it.
The reason is shown in one place only: Settings → Routing, which reports the
state, the reason, when the model last answered, and offers a re-check. If
automatic dispatch seems to have stopped, that section is where to look.
