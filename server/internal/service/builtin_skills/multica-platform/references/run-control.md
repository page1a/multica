# Run control

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

## Message a reply that is still running

A chat message or an issue comment can reach an agent mid-reply in three ways.
`multica chat send` and `multica issue comment add` both take `--mode`:

| Mode | What happens | CLI process | Session |
| --- | --- | --- | --- |
| `steer` | Read after the current step; the original work continues | Same process | Same session |
| `queue` | Handled once this reply / run finishes (default for chat send) | New process | Resumes the same session |
| `restart` | Stops the reply now and starts over from this message; the half-done step is dropped | New process | Resumes the same session |

```bash
multica chat send --session <chat-id> --content-file ./msg.md --mode steer
multica issue comment add <issue-id> --content-file ./msg.md --mode restart
```

- `steer` reaches the run in one of two ways, decided by its CLI:
  - Claude, Codex, Grok, OpenCode 1.x and Pi read it in the running process
    after the current step.
  - One-shot CLIs (Cursor, Copilot, CodeArts, DevEco, Antigravity, OpenClaw)
    have no input channel while they work, so the daemon stops the CLI and
    resumes the same session at once with the message; the step in flight is
    cut off. The run, its receipt and the session ID stay the same.
    Antigravity can only do this on a run that resumed a session.
  - ACP CLIs (Hermes, Kimi, Kiro, Qoder, QwenPaw, Reasonix, Trae, ZeroClaw,
    Devin, DIM, MCode) cancel the current step and prompt the same session in
    the same process with the message; the step in flight is cut off.
  The chat pending task reports this as `steer_mode` and an issue task as
  `supplement_steer_mode` (`same`, `restart` or `handoff`).
- Any other CLI, or one too old to negotiate `steer`, is refused with exit status 1, the reason, and
  `available_modes: [queue, restart]` in the JSON body; nothing is posted, so
  resend with one of those.
- `steer` on a comment is for people only and is text-only (no attachments).
- With nothing running, every mode simply sends/posts and starts a run.
- `chat send` needs an explicit `--session`; it never falls back to the chat
  the current run belongs to, so a run cannot queue a turn for itself.
- Without `--mode`, `comment add` keeps its existing behaviour (a busy agent
  answers after its current run).

## Hand work to another agent

When agent A is running on an issue and the comment @-mentions a different
agent B, `comment add` takes two more modes:

| Mode | What happens |
| --- | --- |
| `handoff` | Every run on the issue whose agent the comment does not wake stops (your own run is spared). The comment becomes the state card's handoff note, and B starts a new session that opens with that card. |
| `parallel` | A keeps going; B starts its own run alongside it. |

```bash
multica issue comment add <issue-id> --content-file ./take-over.md --mode handoff
```

`handoff` without an @agent is refused. To give a chat to another agent, run
`multica chat handoff --to <agent> [--session <id|url>]`: it opens a new chat
with that agent whose first message summarises this one, and leaves the old
chat as it is. Only the chat's owner can hand it over.
